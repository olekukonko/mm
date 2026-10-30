package tests

import (
	"testing"

	"github.com/olekukonko/mm/copier"
)

// The cached fast paths must produce exactly the same result as the generic
// field walk. These tests pin the cases where they are deliberately bypassed.

type fastSrc struct {
	Name   string
	Age    int
	Secret string `mm:"-"`
	Tags   []string
}

type fastDst struct {
	Name   string
	Age    int
	Secret string
	Tags   []string
}

func TestFastPathHonoursIgnoredTag(t *testing.T) {
	src := fastSrc{Name: "a", Age: 1, Secret: "should-not-copy"}

	// Overwrite mode: the ignored value must never reach the destination.
	dst := fastDst{}
	if err := copier.NewCopier().Copy(&dst, src); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if dst.Secret == "should-not-copy" {
		t.Fatalf("tag %q was ignored by a fast path: got %q", "mm:\"-\"", dst.Secret)
	}
	if dst.Name != "a" || dst.Age != 1 {
		t.Fatalf("plain fields not copied: %+v", dst)
	}

	// Merge mode: the destination value is preserved.
	merged := fastDst{Secret: "kept"}
	if err := copier.NewCopier(copier.WithMerge()).Copy(&merged, src); err != nil {
		t.Fatalf("merge copy failed: %v", err)
	}
	if merged.Secret != "kept" {
		t.Fatalf("merge should preserve the destination, got %q", merged.Secret)
	}
}

func TestFastPathDeepCopiesSlices(t *testing.T) {
	src := fastSrc{Name: "a", Age: 1, Tags: []string{"x", "y"}}
	dst := fastDst{}
	c := copier.NewCopier()
	if err := c.Copy(&dst, src); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if len(dst.Tags) != 2 {
		t.Fatalf("slice not copied: %+v", dst.Tags)
	}
	src.Tags[0] = "mutated"
	if dst.Tags[0] != "x" {
		t.Fatalf("slice aliased between source and destination (fast path must be disabled for reference types)")
	}
}

func TestFastPathNumericConversion(t *testing.T) {
	type src struct {
		Count int
		Ratio int
	}
	type dst struct {
		Count float64
		Ratio string
	}
	in := src{Count: 3, Ratio: 65}
	var out dst
	if err := copier.NewCopier().Copy(&out, in); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if out.Count != 3 {
		t.Fatalf("int -> float64 conversion failed: %v", out.Count)
	}
	if out.Ratio != "A" {
		t.Fatalf("int -> string conversion failed: %q", out.Ratio)
	}
}

func TestFastPathDisabledWhenMerging(t *testing.T) {
	type pair struct{ Name, City string }
	src := pair{Name: "src"}
	dst := pair{Name: "dst", City: "kept"}
	c := copier.NewCopier(copier.WithMerge())
	if err := c.Copy(&dst, src); err != nil {
		t.Fatalf("merge copy failed: %v", err)
	}
	if dst.Name != "dst" || dst.City != "kept" {
		t.Fatalf("merge overwrote existing values: %+v", dst)
	}
}

// unmappedDest supports both tag names so the two engines can be compared on
// equivalent semantics (mapstructure uses ",remain" for the same feature).
type unmappedDest struct {
	ID        int
	DestName  string
	ExtraData map[string]interface{} `mm:",unmapped" mapstructure:",remain"`
}

func TestFastPathUnmappedCollectsOnlyExtras(t *testing.T) {
	src := map[string]interface{}{
		"ID":       456,
		"DestName": "Mapped Name",
		"ExtraKey": "Some Value",
	}
	dest := unmappedDest{ExtraData: map[string]interface{}{"stale": true}}
	if err := copier.NewCopier().Copy(&dest, src); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if dest.ID != 456 || dest.DestName != "Mapped Name" {
		t.Fatalf("matched fields not copied: %+v", dest)
	}
	if len(dest.ExtraData) != 1 || dest.ExtraData["ExtraKey"] != "Some Value" {
		t.Fatalf("unmapped keys wrong (want only ExtraKey): %+v", dest.ExtraData)
	}
	if _, stale := dest.ExtraData["stale"]; stale {
		t.Fatalf("overwrite should replace the unmapped map, got %+v", dest.ExtraData)
	}
}

func TestFastPathUnmappedDeepCopiesReferenceValues(t *testing.T) {
	src := map[string]interface{}{
		"ID":    1,
		"Items": []string{"a", "b"},
	}
	var dest unmappedDest
	if err := copier.NewCopier().Copy(&dest, src); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	got, ok := dest.ExtraData["Items"].([]string)
	if !ok || len(got) != 2 {
		t.Fatalf("unmapped slice not copied: %#v", dest.ExtraData)
	}
	src["Items"].([]string)[0] = "mutated"
	if dest.ExtraData["Items"].([]string)[0] != "a" {
		t.Fatalf("unmapped value aliased the source slice")
	}
}

func TestFastPathUnmappedMergeKeepsDestination(t *testing.T) {
	src := map[string]interface{}{"ID": 2, "ExtraKey": "new"}
	dest := unmappedDest{
		ID:        1,
		DestName:  "keep",
		ExtraData: map[string]interface{}{"ExtraKey": "old"},
	}
	if err := copier.NewCopier(copier.WithMerge()).Copy(&dest, src); err != nil {
		t.Fatalf("merge copy failed: %v", err)
	}
	if dest.ID != 1 || dest.DestName != "keep" {
		t.Fatalf("merge overwrote destination fields: %+v", dest)
	}
	if dest.ExtraData["ExtraKey"] != "old" {
		t.Fatalf("merge overwrote unmapped value: %+v", dest.ExtraData)
	}
}

func TestCloneIsIsolated(t *testing.T) {
	type pair struct{ Name, City string }
	src := pair{Name: "n", City: "c"}

	base := copier.NewCopier()
	variant := base.Clone().WithSkip("Name")

	var fromVariant pair
	if err := variant.Copy(&fromVariant, src); err != nil {
		t.Fatalf("variant copy failed: %v", err)
	}
	if fromVariant.Name != "" {
		t.Fatalf("WithSkip on a clone should skip Name, got %q", fromVariant.Name)
	}

	var fromBase pair
	if err := base.Copy(&fromBase, src); err != nil {
		t.Fatalf("base copy failed: %v", err)
	}
	if fromBase.Name != "n" {
		t.Fatalf("mutating a clone changed the original copier: %q", fromBase.Name)
	}
}

func TestDeprecatedNewMatchesNewCopier(t *testing.T) {
	type pair struct{ Name string }
	src := pair{Name: "x"}
	var dst pair
	if err := copier.New().Copy(&dst, src); err != nil {
		t.Fatalf("copier.New() failed: %v", err)
	}
	if dst.Name != "x" {
		t.Fatalf("copier.New() did not copy: %+v", dst)
	}
}
