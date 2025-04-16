package mm

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// --- Test Struct Definitions (Same as previous version) ---

type SimpleSrc struct {
	Name string
	Age  int
}
type SimpleDest struct {
	Name string
	Age  int
}
type NestedSrc struct {
	ID    int
	Child SimpleSrc
}
type NestedDest struct {
	ID    int
	Child SimpleDest
}
type DifferentTypesSrc struct {
	Timestamp string
	Count     int
	Value     float32
}
type DifferentTypesDest struct {
	Timestamp time.Time
	Count     float64
	Value     float64
}
type UnexportedSrc struct {
	Public  string
	private string // lower case = unexported
}
type UnexportedDest struct {
	Public  string
	private string // lower case = unexported
}
type SliceSrc struct {
	Ints    []int
	Structs []SimpleSrc
	Ptrs    []*SimpleSrc
}
type SliceDest struct {
	Ints    []int
	Structs []SimpleDest
	Ptrs    []*SimpleDest
}
type MapSrc struct {
	StrInt map[string]int
	IntStr map[int]SimpleSrc
}
type MapDest struct {
	StrInt map[string]int
	IntStr map[int]SimpleDest
}
type InterfaceSrc struct {
	Any interface{}
}
type InterfaceDest struct {
	Any interface{}
}
type TagSource struct {
	FullName   string `mm:"Name"`          // Explicit mapping
	Years      int    `mm:"Age,omitempty"` // Mapping + omitempty
	ID         string `mm:"-"`             // Ignore this field
	NoTag      string // Direct match expected
	City       string `mm:",omitempty"` // No mapping name, just omitempty
	unexported string // Unexported, ignored by default
}
type TagDest struct {
	Name  string
	Age   int
	NoTag string
	City  string
}
type SkipZeroSrc struct {
	Name string // Zero: ""
	Age  int    // Zero: 0
	Num  *int   // Zero: nil
	Flag bool   // Zero: false
}
type SkipZeroDest struct {
	Name string
	Age  int
	Num  *int
	Flag bool
}
type Cyclic struct {
	Value int
	Self  *Cyclic
	Data  interface{}
}
type SourceWithExtras struct {
	ID      int
	Name    string `mm:"DestName"`
	Email   string // Will be unmapped
	Address string // Will be unmapped
}
type DestWithUnmapped struct {
	ID        int
	DestName  string
	ExtraData map[string]interface{} `mm:",unmaped"` // Collector field
}
type DestWithWrongUnmapped struct {
	ID        int
	DestName  string
	ExtraData []string `mm:",unmaped"` // Wrong type for unmapped
}
type MapSource struct { // For struct -> map tests
	FirstName string
	LastName  string
	Metadata  map[string]string
	Zip       string `mm:"-"`          // Ignore this
	Age       int    `mm:",omitempty"` // Omit if zero
}

// --- Test Cases ---

func TestSimpleCopy(t *testing.T) {
	src := SimpleSrc{Name: "Alice", Age: 30}
	var dest SimpleDest
	c := New()
	err := c.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo failed: %v", err)
	}
	if dest.Name != src.Name || dest.Age != src.Age {
		t.Errorf("Simple copy failed, got %+v, expected %+v", dest, src)
	}
}

func TestNestedStruct(t *testing.T) {
	src := NestedSrc{ID: 1, Child: SimpleSrc{Name: "Bob", Age: 25}}
	var dest NestedDest
	c := New()
	err := c.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo failed: %v", err)
	}
	if dest.ID != src.ID || dest.Child.Name != src.Child.Name || dest.Child.Age != src.Child.Age {
		t.Errorf("Nested copy failed, got %+v, expected %+v", dest, src)
	}
}

func TestFieldMappingAndTags(t *testing.T) {
	// Case 1: Years has value
	srcWithValue := TagSource{FullName: "Charlie", Years: 40, ID: "ignore123", NoTag: "copied", City: "Testville", unexported: "secret"}
	var dest1 TagDest
	c := New() //.EnableDebug(true)
	err := c.CopyTo(&dest1, srcWithValue)
	if err != nil {
		t.Fatalf("CopyTo failed: %v", err)
	}
	// Check mapped fields
	if dest1.Name != srcWithValue.FullName {
		t.Errorf("Tag mapping (FullName -> Name) failed, got '%s'", dest1.Name)
	}
	if dest1.Age != srcWithValue.Years {
		t.Errorf("Tag mapping (Years -> Age) failed, got %d", dest1.Age)
	}
	// Check unmapped field with same name
	if dest1.NoTag != srcWithValue.NoTag {
		t.Errorf("Direct mapping (NoTag) failed")
	}
	if dest1.City != srcWithValue.City {
		t.Errorf("Direct mapping (City) failed")
	}
	// Ignored field ID should not affect dest

	// Case 2: Years is 0 (should be omitted due to omitempty tag on Years)
	// City is "" (should be omitted due to omitempty tag on City)
	srcWithZero := TagSource{FullName: "David", Years: 0, ID: "ignore456", NoTag: "copied_again", City: ""}
	dest2 := TagDest{Age: 99, City: "KeepMe"} // Pre-fill Age and City to check omit
	err = c.CopyTo(&dest2, srcWithZero)
	if err != nil {
		t.Fatalf("CopyTo failed: %v", err)
	}
	if dest2.Name != srcWithZero.FullName {
		t.Errorf("Tag mapping (FullName -> Name) failed on zero case")
	}
	if dest2.Age != 99 {
		t.Errorf("omitempty failed for Age: expected 99 (original dest value), got %d", dest2.Age)
	} // Age should not be copied
	if dest2.NoTag != srcWithZero.NoTag {
		t.Errorf("Direct mapping (NoTag) failed on zero case")
	}
	if dest2.City != "KeepMe" {
		t.Errorf("omitempty failed for City: expected 'KeepMe' (original dest value), got '%s'", dest2.City)
	} // City should not be copied
}

func TestSkipFields(t *testing.T) {
	src := SimpleSrc{Name: "SourceValue", Age: 99}
	// Initialize dest with different values to ensure skip works
	dest := SimpleDest{Name: "DestOriginal", Age: 0}

	c := New().WithSkip("Name") //.EnableDebug(true)
	err := c.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo failed: %v", err)
	}

	// Name should NOT be copied from src, Age should be copied
	if dest.Name != "DestOriginal" { // Dest Name should remain unchanged
		t.Errorf("Skip field 'Name' failed, expected 'DestOriginal', got '%s'", dest.Name)
	}
	if dest.Age != src.Age { // Dest Age should be updated
		t.Errorf("Skip field check failed for 'Age', expected %d, got %d", src.Age, dest.Age)
	}
}

func TestTypeConversion(t *testing.T) {
	src := DifferentTypesSrc{Timestamp: "2023-01-01T00:00:00Z", Count: 42, Value: 3.14}
	var dest DifferentTypesDest
	c := New().
		WithTypeConverter(reflect.TypeOf(""), reflect.TypeOf(time.Time{}),
			func(src reflect.Value) (reflect.Value, error) {
				t, err := time.Parse(time.RFC3339, src.String())
				return reflect.ValueOf(t), err
			}).
		WithTypeConverter(reflect.TypeOf(0), reflect.TypeOf(0.0),
			func(src reflect.Value) (reflect.Value, error) { return reflect.ValueOf(float64(src.Int())), nil })

	err := c.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo failed: %v", err)
	}
	expectedTime, _ := time.Parse(time.RFC3339, src.Timestamp)
	if !dest.Timestamp.Equal(expectedTime) {
		t.Errorf("Type conversion (string -> time.Time) failed")
	}
	if dest.Count != 42.0 {
		t.Errorf("Type conversion (int -> float64) failed")
	}
	if dest.Value != float64(src.Value) {
		t.Errorf("Type conversion (float32 -> float64) failed")
	}
}

func TestUnexportedFields(t *testing.T) {
	src := UnexportedSrc{Public: "ok", private: "secret"}
	var dest UnexportedDest

	// Test without unexported enabled (DEFAULT)
	cDefault := New() //.EnableDebug(true)
	err := cDefault.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo (default: unexported=false) failed: %v", err)
	}
	if dest.Public != "ok" || dest.private != "" { // private should NOT be copied
		t.Errorf("Default (unexported=false) failed, got %+v, expected Public='ok', private=''", dest)
	}

	// Test with unexported enabled (requires unsafe)
	dest = UnexportedDest{}
	cWithUnsafe := New().WithUnexported(true).WithUnsafe(true) //.EnableDebug(true)
	err = cWithUnsafe.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo (unexported=true, unsafe=true) failed: %v", err)
	}
	if dest.Public != "ok" || dest.private != "secret" { // private SHOULD be copied
		t.Errorf("Unexported=true, unsafe=true failed, got %+v, expected Public='ok', private='secret'", dest)
	}

	// Test with unexported=true but unsafe=false (should fail private)
	dest = UnexportedDest{}
	cNoUnsafeButFlag := New().WithUnexported(true).WithUnsafe(false) //.EnableDebug(true)
	err = cNoUnsafeButFlag.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo (unexported=true, unsafe=false) failed: %v", err)
	}
	if dest.Public != "ok" || dest.private != "" { // private should NOT be copied
		t.Errorf("Unexported=true, unsafe=false failed, got %+v, expected Public='ok', private=''", dest)
	}
}

func TestNilSourceHandling(t *testing.T) {
	var src *SimpleSrc = nil
	dest := SimpleDest{Name: "Keep", Age: 1}

	// SkipNil(true)
	cSkip := New().WithSkipNil(true)
	err := cSkip.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo SkipNil(true) failed: %v", err)
	}
	if dest.Name != "Keep" || dest.Age != 1 {
		t.Errorf("SkipNil(true) failed, dest modified: %+v", dest)
	}

	// SkipNil(false), Overwrite(true)
	dest = SimpleDest{Name: "Overwrite", Age: 2}
	cOverwrite := New().WithSkipNil(false).WithOverwrite(true)
	err = cOverwrite.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo SkipNil(false)/Overwrite(true) failed: %v", err)
	}
	if dest.Name != "" || dest.Age != 0 {
		t.Errorf("SkipNil(false)/Overwrite(true) failed, dest not zeroed: %+v", dest)
	}

	// SkipNil(false), Overwrite(false)
	dest = SimpleDest{Name: "KeepAgain", Age: 3}
	cNoOverwrite := New().WithSkipNil(false).WithOverwrite(false)
	err = cNoOverwrite.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo SkipNil(false)/Overwrite(false) failed: %v", err)
	}
	if dest.Name != "KeepAgain" || dest.Age != 3 {
		t.Errorf("SkipNil(false)/Overwrite(false) failed, dest modified: %+v", dest)
	}
}

func TestNilPointerFieldHandling(t *testing.T) {
	type SourceWithPtr struct {
		Name string
		Ptr  *SimpleSrc
	}
	type DestWithPtr struct {
		Name string
		Ptr  *SimpleDest
	}
	src := SourceWithPtr{Name: "Source", Ptr: nil}
	dest := DestWithPtr{Name: "DestOrig", Ptr: &SimpleDest{Name: "Keep", Age: 1}}

	// SkipNil(true)
	cSkip := New().WithSkipNil(true) //.EnableDebug(true)
	err := cSkip.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo failed: %v", err)
	}
	// Dest Name should be copied, Ptr should be skipped
	if dest.Name != "Source" {
		t.Errorf("SkipNil(true): Name not copied. Got '%s'", dest.Name)
	}
	if dest.Ptr == nil || dest.Ptr.Name != "Keep" {
		t.Errorf("SkipNil(true) for field failed. Got Ptr: %+v", dest.Ptr)
	}

	// SkipNil(false), Overwrite(true)
	dest = DestWithPtr{Name: "DestOrig", Ptr: &SimpleDest{Name: "Overwrite", Age: 2}} // Reset dest
	cOverwrite := New().WithSkipNil(false).WithOverwrite(true)                        //.EnableDebug(true)
	err = cOverwrite.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo failed: %v", err)
	}
	// Dest Name should be copied, Ptr should become nil
	if dest.Name != "Source" {
		t.Errorf("SkipNil(false)/Overwrite(true): Name not copied. Got '%s'", dest.Name)
	}
	if dest.Ptr != nil {
		t.Errorf("SkipNil(false)/Overwrite(true) for field failed, Ptr not nil. Got: %+v", dest.Ptr)
	}

	// SkipNil(false), Overwrite(false)
	dest = DestWithPtr{Name: "DestOrig", Ptr: &SimpleDest{Name: "KeepAgain", Age: 3}} // Reset dest
	cNoOverwrite := New().WithSkipNil(false).WithOverwrite(false)                     //.EnableDebug(true)
	err = cNoOverwrite.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo failed: %v", err)
	}
	// Dest Name should be copied, Ptr should NOT be copied (because overwrite=false)
	if dest.Name != "Source" {
		t.Errorf("SkipNil(false)/Overwrite(false): Name not copied. Got '%s'", dest.Name)
	}
	if dest.Ptr == nil || dest.Ptr.Name != "KeepAgain" {
		t.Errorf("SkipNil(false)/Overwrite(false) for field failed. Got Ptr: %+v", dest.Ptr)
	}
}

func TestGenericCopy(t *testing.T) {
	// Test with non-pointer struct source
	srcStruct := SimpleSrc{Name: "Eve", Age: 28}
	c := New() //.EnableDebug(true)
	destStruct, errStruct := Copy[SimpleDest](c, srcStruct)
	if errStruct != nil {
		t.Fatalf("Generic Copy struct failed: %v", errStruct)
	}
	if destStruct.Name != srcStruct.Name || destStruct.Age != srcStruct.Age {
		t.Errorf("Generic copy struct failed, got %+v", destStruct)
	}

	// Test with pointer source
	srcPtr := &SimpleSrc{Name: "PtrEve", Age: 29}
	destPtr, errPtr := Copy[SimpleDest](c, srcPtr)
	if errPtr != nil {
		t.Fatalf("Generic Copy from pointer failed: %v", errPtr)
	}
	if destPtr.Name != srcPtr.Name || destPtr.Age != srcPtr.Age {
		t.Errorf("Generic copy from pointer failed, got %+v", destPtr)
	}

	// Test with nil source object
	var nilSrc *SimpleSrc = nil
	destNil, errNil := Copy[SimpleDest](c, nilSrc)
	// Expect error because src object itself is nil
	if errNil == nil {
		t.Fatalf("Generic Copy from nil source expected error, got nil")
	}
	if !errors.Is(errNil, ErrSourceIsNil) {
		t.Fatalf("Generic Copy from nil source expected ErrSourceIsNil, got %v", errNil)
	}
	// Check dest is zero value
	if destNil.Name != "" || destNil.Age != 0 {
		t.Errorf("Generic copy from nil source returned non-zero dest: %+v", destNil)
	}

	// Test with nil source pointer (different from nil source object)
	var nilPtrSrc *SimpleSrc = nil
	destNilPtr, errNilPtr := Copy[SimpleDest](c, nilPtrSrc)
	// Expect *no* error here, Copy should return zero value T
	if errNilPtr != nil {
		t.Fatalf("Generic Copy from nil pointer source expected nil error, got %v", errNilPtr)
	}
	if destNilPtr.Name != "" || destNilPtr.Age != 0 {
		t.Errorf("Generic copy from nil pointer source returned non-zero dest: %+v", destNilPtr)
	}
}

func TestCycleDetection(t *testing.T) {
	node1 := &Cyclic{Value: 1}
	node2 := &Cyclic{Value: 2}
	node1.Self = node2
	node2.Self = node1
	node1.Data = node2
	c := New().WithCycleDetection(true) //.EnableDebug(true)
	var dest1 Cyclic
	err := c.CopyTo(&dest1, node1)
	if err != nil {
		t.Fatalf("Cycle detection CopyTo failed: %v", err)
	}

	if dest1.Value != 1 {
		t.Errorf("dest1.Value mismatch: got %d", dest1.Value)
	}
	if dest1.Self == nil {
		t.Fatalf("dest1.Self is nil")
	}
	if dest1.Self.Value != 2 {
		t.Errorf("dest1.Self.Value mismatch: got %d", dest1.Self.Value)
	}
	if dest1.Self.Self == nil {
		t.Fatalf("dest1.Self.Self is nil")
	}
	if dest1.Self.Self != &dest1 {
		t.Errorf("Cycle failed: dest1.Self.Self (%p) != &dest1 (%p)", dest1.Self.Self, &dest1)
	}
	if dest1.Data == nil {
		t.Fatalf("dest1.Data is nil")
	}
	dest2Ref, ok := dest1.Data.(*Cyclic)
	if !ok {
		t.Fatalf("dest1.Data is not *Cyclic, type is %T", dest1.Data)
	}
	if dest2Ref != dest1.Self {
		t.Errorf("Cycle via interface failed: dest1.Data (%p) != dest1.Self (%p)", dest2Ref, dest1.Self)
	}

	genDest, err := Copy[Cyclic](c, node1)
	if err != nil {
		t.Fatalf("Cycle detection generic Copy failed: %v", err)
	}
	if genDest.Self == nil || genDest.Self.Self == nil {
		t.Fatalf("Generic copy cycle nil pointers")
	}
	if genDest.Self.Self != &genDest {
		t.Errorf("Generic cycle failed: genDest.Self.Self (%p) != &genDest (%p)", genDest.Self.Self, &genDest)
	}
}

func TestSkipZeroValue(t *testing.T) {
	num := 5
	src := SkipZeroSrc{Name: "Keep", Age: 0, Num: nil, Flag: false}         // Age, Num, Flag are zero
	dest := SkipZeroDest{Name: "Overwrite", Age: 99, Num: &num, Flag: true} // Pre-fill dest
	c := New().WithSkipZero(true)                                           //.EnableDebug(true)
	err := c.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo failed: %v", err)
	}
	if dest.Name != src.Name {
		t.Errorf("SkipZero failed for Name: got '%s'", dest.Name)
	}
	if dest.Age != 99 {
		t.Errorf("SkipZero failed for Age: expected 99 (original), got %d", dest.Age)
	} // Should not copy 0
	if dest.Num == nil || *dest.Num != 5 {
		t.Errorf("SkipZero failed for Num: expected ptr to 5 (original), got %v", dest.Num)
	} // Should not copy nil
	if dest.Flag != true {
		t.Errorf("SkipZero failed for Flag: expected true (original), got %t", dest.Flag)
	} // Should not copy false
}

func TestSkipEmptyValue(t *testing.T) {
	type EmptySrc struct {
		Name  string         // Empty: ""
		Age   int            // Empty: 0
		Num   *int           // Empty: nil
		Flag  bool           // Empty: false
		Items []string       // Empty: nil or empty slice
		Attrs map[string]int // Empty: nil or empty map
	}
	type EmptyDest struct {
		Name  string
		Age   int
		Num   *int
		Flag  bool
		Items []string
		Attrs map[string]int
	}

	num := 5
	tests := []struct {
		name string
		src  EmptySrc
		dest EmptyDest // Initial state
		exp  EmptyDest // Expected state after copy
	}{
		{
			name: "All Empty",
			src:  EmptySrc{Name: "", Age: 0, Num: nil, Flag: false, Items: []string{}, Attrs: nil},
			dest: EmptyDest{Name: "Overwrite", Age: 99, Num: &num, Flag: true, Items: []string{"a"}, Attrs: map[string]int{"b": 1}},
			exp:  EmptyDest{Name: "Overwrite", Age: 99, Num: &num, Flag: true, Items: []string{"a"}, Attrs: map[string]int{"b": 1}}, // Dest unchanged
		},
		{
			name: "Some NonEmpty",
			src:  EmptySrc{Name: "Keep", Age: 0, Num: nil, Flag: true, Items: nil, Attrs: map[string]int{}},
			dest: EmptyDest{Name: "Overwrite", Age: 99, Num: &num, Flag: false, Items: []string{"a"}, Attrs: map[string]int{"b": 1}},
			exp:  EmptyDest{Name: "Keep", Age: 99, Num: &num, Flag: true, Items: []string{"a"}, Attrs: map[string]int{"b": 1}}, // Name, Flag copied
		},
	}

	c := New().WithSkipEmpty(true) //.EnableDebug(true)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			currentDest := tt.dest // Copy initial dest state for test run
			err := c.CopyTo(&currentDest, tt.src)
			if err != nil {
				t.Fatalf("CopyTo failed: %v", err)
			}
			// Use assertDeepEqual for complex structs/slices/maps
			assertDeepEqual(t, currentDest, tt.exp, "SkipEmpty failed")
		})
	}
}

func TestSliceCopy(t *testing.T) {
	srcPtr1 := &SimpleSrc{Name: "Ptr1", Age: 1}
	srcPtr2 := &SimpleSrc{Name: "Ptr2", Age: 2}
	src := SliceSrc{Ints: []int{1, 2, 3}, Structs: []SimpleSrc{{Name: "S1"}, {Name: "S2"}}, Ptrs: []*SimpleSrc{srcPtr1, srcPtr2, srcPtr1}}
	var dest SliceDest
	c := New()
	err := c.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo failed for slices: %v", err)
	}

	if len(dest.Ints) != len(src.Ints) {
		t.Fatalf("Ints length")
	}
	src.Ints[0] = 999
	if dest.Ints[0] == 999 {
		t.Errorf("Ints slice shallow")
	}
	if len(dest.Structs) != len(src.Structs) {
		t.Fatalf("Structs length")
	}
	src.Structs[0].Name = "Changed"
	if dest.Structs[0].Name == "Changed" {
		t.Errorf("Structs slice shallow")
	}
	if len(dest.Ptrs) != len(src.Ptrs) {
		t.Fatalf("Ptrs length")
	}
	if dest.Ptrs[0] == nil || dest.Ptrs[1] == nil || dest.Ptrs[2] == nil {
		t.Fatalf("Nil pointer in dest Ptrs")
	}
	// Check pointer content equality
	if dest.Ptrs[0].Name != srcPtr1.Name || dest.Ptrs[1].Name != srcPtr2.Name {
		t.Errorf("Ptr content mismatch")
	}
	// Check pointer address distinction (deep copy)
	if pointsToSameAddress(dest.Ptrs[0], srcPtr1) {
		t.Errorf("Ptrs[0] shallow copy detected (same address)")
	}
	if pointsToSameAddress(dest.Ptrs[1], srcPtr2) {
		t.Errorf("Ptrs[1] shallow copy detected (same address)")
	}
	// Check pointer identity for duplicated source ptrs
	if dest.Ptrs[0] != dest.Ptrs[2] {
		t.Errorf("Ptrs[0] (%p) and Ptrs[2] (%p) should point to the same destination object, but don't", dest.Ptrs[0], dest.Ptrs[2])
	}
	// Check change isolation
	srcPtr1.Name = "ChangedPtr"
	if dest.Ptrs[0].Name == "ChangedPtr" {
		t.Errorf("Ptrs slice shallow change detected via Ptrs[0]")
	}
	if dest.Ptrs[2].Name == "ChangedPtr" {
		t.Errorf("Ptrs slice shallow change detected via Ptrs[2]")
	}
}

func TestMapCopy(t *testing.T) {
	srcVal1 := SimpleSrc{Name: "MapVal1", Age: 1}
	srcVal2 := SimpleSrc{Name: "MapVal2", Age: 2}
	src := MapSrc{StrInt: map[string]int{"one": 1, "two": 2}, IntStr: map[int]SimpleSrc{10: srcVal1, 20: srcVal2}}
	var dest MapDest
	c := New()
	err := c.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo failed for maps: %v", err)
	}

	if len(dest.StrInt) != len(src.StrInt) {
		t.Fatalf("StrInt length")
	}
	src.StrInt["one"] = 99
	if dest.StrInt["one"] == 99 {
		t.Errorf("StrInt map shallow")
	}
	if len(dest.IntStr) != len(src.IntStr) {
		t.Fatalf("IntStr length")
	}
	v := src.IntStr[10]
	v.Name = "Changed"
	src.IntStr[10] = v
	if dest.IntStr[10].Name == "Changed" {
		t.Errorf("IntStr map value shallow")
	}
	if fmt.Sprintf("%p", dest.IntStr) == fmt.Sprintf("%p", src.IntStr) {
		t.Errorf("IntStr map pointers same")
	}
}

func TestInterfaceCopy(t *testing.T) {
	srcSimple := SimpleSrc{Name: "Concrete", Age: 55}
	c := New()
	src := InterfaceSrc{Any: &srcSimple} // Pointer to struct
	var dest InterfaceDest

	err := c.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo failed: %v", err)
	}
	if dest.Any == nil {
		t.Fatalf("Dest interface is nil")
	}

	destConcrete, ok := dest.Any.(*SimpleDest) // Expect *SimpleDest
	if !ok {
		t.Fatalf("Dest is not *SimpleDest, but %T", dest.Any)
	}
	if destConcrete.Name != srcSimple.Name || destConcrete.Age != srcSimple.Age {
		t.Errorf("Interface content mismatch: %+v", destConcrete)
	}
	// Ensure deep copy of pointed-to value
	srcSimple.Name = "Changed" // Modify original source struct
	if destConcrete.Name == "Changed" {
		t.Errorf("Interface copy was shallow (change reflected)")
	}
}

func TestUnmappedFieldsStructToStruct(t *testing.T) {
	src := SourceWithExtras{ID: 123, Name: "Source Name", Email: "test@example.com", Address: "123 Main St"}
	var dest DestWithUnmapped
	dest.ExtraData = map[string]interface{}{"Existing": true} // Pre-fill map

	c := New() //.EnableDebug(true)
	err := c.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo with unmapped failed: %v", err)
	}

	// Check mapped fields
	if dest.ID != src.ID {
		t.Errorf("Mapped field ID mismatch: got %d, want %d", dest.ID, src.ID)
	}
	if dest.DestName != src.Name {
		t.Errorf("Mapped field DestName mismatch: got '%s', want '%s'", dest.DestName, src.Name)
	}

	// Check unmapped fields
	expUnmapped := map[string]interface{}{
		"Existing": true, // Should be preserved
		"Email":    "test@example.com",
		"Address":  "123 Main St",
	}
	assertDeepEqual(t, dest.ExtraData, expUnmapped, "Unmapped data mismatch")
}

func TestUnmappedFieldsMapToStruct(t *testing.T) {
	srcMap := map[string]interface{}{
		"ID":        456,                    // Mapped directly
		"DestName":  "Mapped From Map",      // Mapped directly
		"Email":     "map@example.com",      // Should go to unmapped
		"ExtraKey":  "Some Value",           // Should go to unmapped
		"NestedMap": map[string]int{"a": 1}, // Should go to unmapped
	}
	var dest DestWithUnmapped
	dest.ExtraData = map[string]interface{}{"KeepMe": 123} // Pre-fill

	c := New() //.EnableDebug(true)
	err := c.CopyTo(&dest, srcMap)
	if err != nil {
		t.Fatalf("CopyTo map->struct with unmapped failed: %v", err)
	}

	// Check mapped fields
	if dest.ID != 456 {
		t.Errorf("Mapped field ID mismatch: got %d", dest.ID)
	}
	if dest.DestName != "Mapped From Map" {
		t.Errorf("Mapped field DestName mismatch: got '%s'", dest.DestName)
	}

	// Check unmapped fields
	expUnmapped := map[string]interface{}{
		"KeepMe":    123, // Preserved
		"Email":     "map@example.com",
		"ExtraKey":  "Some Value",
		"NestedMap": map[string]interface{}{"a": float64(1)}, // Copying map usually results in map[string]interface, numbers often float64
	}
	assertDeepEqual(t, dest.ExtraData, expUnmapped, "Unmapped data mismatch")
}

func TestUnmappedFieldsWrongType(t *testing.T) {
	src := SourceWithExtras{ID: 1, Name: "N", Email: "E", Address: "A"}
	var dest DestWithWrongUnmapped // Has ExtraData []string `mm:",unmaped"`
	c := New()                     //.EnableDebug(true)
	err := c.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo with wrong unmapped type failed: %v", err)
	}
	if dest.ID != 1 {
		t.Errorf("ID mismatch")
	}
	if dest.DestName != "N" {
		t.Errorf("DestName mismatch")
	}
	// ExtraData should be nil because the copier ignores incorrectly typed unmapped fields
	if dest.ExtraData != nil {
		t.Errorf("ExtraData (wrong type) should be nil/ignored, got %v", dest.ExtraData)
	}
}

func TestStructToMap(t *testing.T) {
	src := MapSource{FirstName: "Test", LastName: "User", Metadata: map[string]string{"k": "v"}, Zip: "12345", Age: 30}
	dest := make(map[string]interface{})
	dest["existing"] = true // Pre-fill

	c := New() //.EnableDebug(true)
	err := c.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo struct->map failed: %v", err)
	}

	expMap := map[string]interface{}{
		"existing":  true, // Preserved
		"FirstName": "Test",
		"LastName":  "User",
		"Metadata":  map[string]interface{}{"k": "v"}, // Nested map copied
		"Age":       int64(30),                        // Copied (non-zero), type might convert to int64 via interface{}
		// Zip is ignored via "-" tag
	}
	assertDeepEqual(t, dest, expMap, "Struct to map copy mismatch")

	// Test omitempty for Age
	src.Age = 0
	dest = map[string]interface{}{"existing": true}
	err = c.CopyTo(&dest, src)
	if err != nil {
		t.Fatalf("CopyTo struct->map (omit age) failed: %v", err)
	}
	expMapOmitAge := map[string]interface{}{
		"existing":  true,
		"FirstName": "Test",
		"LastName":  "User",
		"Metadata":  map[string]interface{}{"k": "v"},
		// Age is omitted
	}
	assertDeepEqual(t, dest, expMapOmitAge, "Struct to map copy (omit age) mismatch")
}

func TestMergeTo(t *testing.T) {
	src := SimpleSrc{Name: "New Name", Age: 50}
	dest := SimpleDest{Name: "Keep Me", Age: 0} // Age is zero, Name is not

	c := New()
	err := c.MergeTo(&dest, src) // MergeTo uses overwrite=false internally
	if err != nil {
		t.Fatalf("MergeTo failed: %v", err)
	}

	if dest.Name != "Keep Me" {
		t.Errorf("MergeTo failed for Name: expected 'Keep Me', got '%s'", dest.Name)
	}
	if dest.Age != 50 {
		t.Errorf("MergeTo failed for Age: expected 50, got %d", dest.Age)
	}
}

func TestMustCopyTo(t *testing.T) {
	src := SimpleSrc{Name: "PanicTest", Age: 1}
	var dest SimpleDest
	c := New()

	// Test success case
	recovered := func() (r interface{}) {
		defer func() { r = recover() }()
		c.MustCopyTo(&dest, src)
		return nil
	}()
	if recovered != nil {
		t.Fatalf("MustCopyTo panicked unexpectedly: %v", recovered)
	}
	if dest.Name != src.Name || dest.Age != src.Age {
		t.Errorf("MustCopyTo success case failed: got %+v", dest)
	}

	// Test panic case (invalid destination)
	var invalidDest SimpleDest // Not a pointer
	recovered = func() (r interface{}) {
		defer func() { r = recover() }()
		c.MustCopyTo(invalidDest, src) // Should panic
		return nil
	}()
	if recovered == nil {
		t.Fatalf("MustCopyTo did not panic for invalid destination")
	}
	// Check if panic message contains expected error text
	if errMsg, ok := recovered.(string); !ok || !strings.Contains(errMsg, ErrDestNotPointer.Error()) {
		t.Errorf("MustCopyTo panic message unexpected: %v", recovered)
	}
}

// --- Benchmarks (No changes needed from previous version) ---

func BenchmarkSimpleCopy(b *testing.B) {
	src := SimpleSrc{Name: "Benchmark", Age: 100}
	var dest SimpleDest
	c := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CopyTo(&dest, src)
	}
}
func BenchmarkNestedCopy(b *testing.B) {
	src := NestedSrc{ID: 999, Child: SimpleSrc{Name: "NestedBench", Age: 99}}
	var dest NestedDest
	c := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CopyTo(&dest, src)
	}
}
func BenchmarkSliceCopy_Simple(b *testing.B) {
	src := SliceSrc{Ints: make([]int, 100)}
	for i := 0; i < 100; i++ {
		src.Ints[i] = i
	}
	var dest SliceDest
	c := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CopyTo(&dest, src)
	}
}
func BenchmarkSliceCopy_Complex(b *testing.B) {
	src := SliceSrc{Structs: make([]SimpleSrc, 50)}
	for i := 0; i < 50; i++ {
		src.Structs[i] = SimpleSrc{Name: "Bench", Age: i}
	}
	var dest SliceDest
	c := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CopyTo(&dest, src)
	}
}
func BenchmarkCycleCopy(b *testing.B) {
	node1 := &Cyclic{Value: 1}
	node2 := &Cyclic{Value: 2}
	node1.Self = node2
	node2.Self = node1
	var dest Cyclic
	c := New().WithCycleDetection(true)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CopyTo(&dest, node1)
	}
}
func BenchmarkUnsafeSimpleCopy(b *testing.B) {
	type BenchType struct {
		Name string
		Age  int
		Val  float64
	}
	src := BenchType{Name: "BenchmarkUnsafe", Age: 100, Val: 1.23}
	var dest BenchType
	c := New().WithUnsafe(true)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CopyTo(&dest, src)
	}
}
func BenchmarkReflectSimpleCopy(b *testing.B) {
	type BenchType struct {
		Name string
		Age  int
		Val  float64
	}
	src := BenchType{Name: "BenchmarkReflect", Age: 100, Val: 1.23}
	var dest BenchType
	c := New().WithUnsafe(false)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CopyTo(&dest, src)
	}
}
func BenchmarkMapToStruct(b *testing.B) {
	srcMap := map[string]interface{}{"ID": 456, "DestName": "Mapped Name", "Email": "map@example.com", "ExtraKey": "Some Value"}
	var dest DestWithUnmapped
	c := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CopyTo(&dest, srcMap)
	}
}
func BenchmarkStructToMap(b *testing.B) {
	src := SourceWithExtras{ID: 123, Name: "Source Name", Email: "test@example.com", Address: "123 Main St"}
	dest := make(map[string]interface{})
	c := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CopyTo(&dest, src)
	}
}

// --- Helper Functions ---

// Helper function to check if two interfaces point to the same memory address
func pointsToSameAddress(a, b interface{}) bool {
	valA := reflect.ValueOf(a)
	valB := reflect.ValueOf(b)
	if valA.Kind() != reflect.Ptr || valB.Kind() != reflect.Ptr {
		return false
	}
	if valA.IsNil() || valB.IsNil() {
		return valA.IsNil() && valB.IsNil()
	}
	return valA.Pointer() == valB.Pointer()
}

// Helper for comparing complex structures using JSON marshaling
func assertDeepEqual(t *testing.T, actual, expected interface{}, msgAndArgs ...interface{}) {
	t.Helper()
	// Use json marshal for a reasonable deep comparison, suitable for test structs.
	// Note: Handles basic types, slices, maps, structs. May not work for channels, funcs, etc.
	// Also, map key order is not guaranteed.
	actualJSON, errA := json.MarshalIndent(actual, "", "  ")
	expectedJSON, errE := json.MarshalIndent(expected, "", "  ")
	if errA != nil || errE != nil {
		// Fallback to reflect.DeepEqual if JSON fails (e.g., unsupported types)
		if !reflect.DeepEqual(actual, expected) {
			message := fmt.Sprintf("Mismatch (JSON marshal failed: ActErr=%v, ExpErr=%v).\nEXPECTED: %#v\nACTUAL:   %#v\n", errA, errE, expected, actual)
			if len(msgAndArgs) > 0 {
				message = fmt.Sprintf(msgAndArgs[0].(string), msgAndArgs[1:]...) + "\n" + message
			}
			t.Fatal(message)
		}
		return // DeepEqual passed
	}
	// Compare JSON strings
	if string(actualJSON) != string(expectedJSON) {
		message := fmt.Sprintf("Mismatch.\nEXPECTED:\n%s\nACTUAL:\n%s\n", string(expectedJSON), string(actualJSON))
		if len(msgAndArgs) > 0 {
			message = fmt.Sprintf(msgAndArgs[0].(string), msgAndArgs[1:]...) + "\n" + message
		}
		t.Fatal(message)
	}
}
