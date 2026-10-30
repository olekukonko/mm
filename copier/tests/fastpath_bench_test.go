// Benchmarks for the fast paths added in copier/plan.go. They are kept separate
// from the mapstructure comparison so the two can be read independently.
package tests

import (
	"testing"

	"github.com/mitchellh/mapstructure"
	"github.com/olekukonko/mm"
	"github.com/olekukonko/mm/copier"
)

type plainMapDest struct {
	ID    int
	Name  string
	Email string
	Note  string
}

// BenchmarkMapUnmapped_MM compares the map->struct path with an unmapped sink.
func BenchmarkMapUnmapped_MM(b *testing.B) {
	src := map[string]interface{}{
		"ID":       456,
		"DestName": "Mapped Name",
		"ExtraKey": "Some Value",
	}
	var dest unmappedDest
	c := copier.NewCopier()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, src)
	}
}

func BenchmarkMapUnmapped_Mapstructure(b *testing.B) {
	src := map[string]interface{}{
		"ID":       456,
		"DestName": "Mapped Name",
		"ExtraKey": "Some Value",
	}
	var dest unmappedDest
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mapstructure.Decode(src, &dest)
	}
}

// BenchmarkMapstructure_MapToStruct_Plain is the mapstructure counterpart of
// BenchmarkMMMapToStruct_Plain (same data, no unmapped field).
func BenchmarkMapstructure_MapToStruct_Plain(b *testing.B) {
	src := map[string]interface{}{
		"ID":    456,
		"Name":  "Mapped Name",
		"Email": "map@example.com",
		"Note":  "Some Value",
	}
	var dest plainMapDest
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mapstructure.Decode(src, &dest)
	}
}

// BenchmarkMMMapToStruct_Plain exercises the cached map->struct plan (no
// unmapped field, exact value types).
func BenchmarkMMMapToStruct_Plain(b *testing.B) {
	src := map[string]interface{}{
		"ID":    456,
		"Name":  "Mapped Name",
		"Email": "map@example.com",
		"Note":  "Some Value",
	}
	var dest plainMapDest
	c := copier.NewCopier()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, src)
	}
}

// BenchmarkMMStructToMap_Plain exercises the cached struct->map plan.
func BenchmarkMMStructToMap_Plain(b *testing.B) {
	src := plainMapDest{ID: 1, Name: "n", Email: "e", Note: "x"}
	dest := make(map[string]interface{})
	c := copier.NewCopier()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, src)
	}
}

// BenchmarkMMDeep_SameType exercises the identical-plain-type assignment path.
func BenchmarkMMDeep_SameType(b *testing.B) {
	src := plainMapDest{ID: 1, Name: "n", Email: "e", Note: "x"}
	c := copier.NewCopier()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = c.Deep(src)
	}
}

// BenchmarkMMGenericCopy_SameType exercises mm.Copy with a package-level copier.
func BenchmarkMMGenericCopy_SameType(b *testing.B) {
	src := plainMapDest{ID: 1, Name: "n", Email: "e", Note: "x"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = mm.Copy[plainMapDest](src)
	}
}
