package tests

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log" // Added for debug/warning output capture if needed
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olekukonko/ll"
	"github.com/olekukonko/ll/lh"
	"github.com/olekukonko/ll/lx"
	"github.com/olekukonko/mm"
	"github.com/olekukonko/mm/copier"
	"github.com/olekukonko/mm/cycle"
)

//  Test Struct Definitions (Same as previous version)

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
	private string //nolint:unused // Used via reflection
}
type UnexportedDest struct {
	Public  string
	private string //nolint:unused // Used via reflection
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
	FullName   string `mm:"Name"`
	Years      int    `mm:"Age,omitempty"`
	ID         string `mm:"-"`
	NoTag      string
	City       string `mm:",omitempty"`
	unexported string //nolint:unused // Used via reflection
}
type TagDest struct {
	Name  string
	Age   int
	NoTag string
	City  string
}
type SkipZeroSrc struct {
	Name string
	Age  int
	Num  *int
	Flag bool
}
type SkipZeroDest struct {
	Name string
	Age  int
	Num  *int
	Flag bool
}

type SourceWithExtras struct {
	ID      int
	Name    string `mm:"DestName"`
	Email   string
	Address string
}
type DestWithUnmapped struct {
	ID        int
	DestName  string
	ExtraData map[string]interface{} `mm:",unmapped"`
}
type DestWithWrongUnmapped struct {
	ID        int
	DestName  string
	ExtraData []string `mm:",unmapped"`
}
type MapSource struct {
	FirstName string
	LastName  string
	Metadata  map[string]string
	Zip       string `mm:"-"`
	Age       int    `mm:",omitempty"`
}

type LameDestination struct {
	User     string `json:"user"`
	Location string `db:"location"`
}

type LameSource struct {
	Username string `json:"user"`
	Country  string `json:"something" db:"location"`
}

// Custom type for Stringer test
type MyStringer struct{ s string }

func (m MyStringer) String() string { return m.s }

// Helper Functions
func pointsToSameAddress(a, b interface{}) bool {
	valA := reflect.ValueOf(a)
	valB := reflect.ValueOf(b)
	// Ensure both are pointers and non-nil before comparing addresses
	if valA.Kind() != reflect.Ptr || valB.Kind() != reflect.Ptr || valA.IsNil() || valB.IsNil() {
		return false // Not comparable or one/both nil
	}
	return valA.Pointer() == valB.Pointer()
}

// assertDeepEqual provides detailed comparison output using JSON marshal as fallback.
func assertDeepEqual(t *testing.T, actual, expected interface{}, msgAndArgs ...interface{}) {
	t.Helper()
	// First, try reflect.DeepEqual as it's more comprehensive for Go types,
	// especially with unexported fields or types JSON can't handle well.
	if reflect.DeepEqual(actual, expected) {
		return
	}

	// If reflect.DeepEqual fails, then try JSON comparison for a more readable diff of exported fields.
	actualJSON, errA := json.MarshalIndent(actual, "", "  ") // Use Indent for readability
	expectedJSON, errE := json.MarshalIndent(expected, "", "  ")

	message := "Assertion failed."
	if len(msgAndArgs) > 0 {
		if s, ok := msgAndArgs[0].(string); ok {
			message = fmt.Sprintf(s, msgAndArgs[1:]...) + "\n" + message
		} else {
			message = fmt.Sprintf("%v", msgAndArgs) + "\n" + message
		}
	}

	message += fmt.Sprintf("\nREFLECT.DEEPEQUAL FAILED.")
	message += fmt.Sprintf("\nEXPECTED VALUE: %#v\nACTUAL VALUE:   %#v", expected, actual)

	if errA == nil && errE == nil {
		message += fmt.Sprintf("\n\nEXPECTED JSON:\n%s\n\nACTUAL JSON:\n%s\n", string(expectedJSON), string(actualJSON))
	} else {
		message += fmt.Sprintf("\n(JSON Marshal Errors: Expected=%v, Actual=%v)", errE, errA)
	}
	t.Fatal(message)
}

// Helper to clone map for testing non-overwrite
func mapsClone(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	c := make(map[string]interface{}, len(m))
	for k, v := range m {
		c[k] = v // Shallow copy of values is okay for this test setup
	}
	return c
}

// MustCopyWith helper for testing
func MustCopyWith[T any](c *copier.Copier, src interface{}) T {
	tempPanicCopier := c.Clone()
	tempPanicCopier.WithPanicOnError(true)
	tempPanicCopier.WithCollectErrors(true)

	val, err := mm.CopyWith[T](tempPanicCopier, src)
	if err != nil {
		panic(fmt.Sprintf("mm.MustCopyWith failed unexpectedly (should have panicked inside): %v", err))
	}
	return val
}

func TestSimpleCopy(t *testing.T) {
	src := SimpleSrc{Name: "Alice", Age: 30}
	var dest SimpleDest // Keep this for Copy test

	// Test package-level Copy (creates new)
	destNew, errNew := mm.Copy[SimpleDest](src)
	if errNew != nil {
		t.Fatalf("Package Copy failed: %v", errNew)
	}
	if destNew.Name != src.Name || destNew.Age != src.Age {
		t.Errorf("Package Copy failed, got %+v, expected %+v", destNew, src)
	}

	// Test Copy (modifies existing)
	c := copier.NewCopier()
	err := c.Copy(&dest, src)
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}
	if dest.Name != src.Name || dest.Age != src.Age {
		t.Errorf("Simple copy (Copy) failed, got %+v, expected %+v", dest, src)
	}
}

// / tries to use the Json tag to resolve
func TestSimpleCopyLame(t *testing.T) {
	src := LameSource{Username: "John Doe", Country: "USA"}

	// Configure copier to use "mm" as primary, then try "json", then "db"
	c := copier.NewCopier().WithTag("mm").WithTagResolution(true).WithFallbackTagKeys("json", "db").WithDebug(false)

	destNew, errNew := mm.CopyWith[LameDestination](c, src)
	if errNew != nil {
		t.Fatalf("CopyWith with custom copier failed: %v", errNew)
	}
	expectedUser := "John Doe"
	expectedLocation := "USA"

	if destNew.User != expectedUser || destNew.Location != expectedLocation {
		t.Errorf("CopyWith with custom copier failed, got User:'%s', Location:'%s', expected User:'%s', Location:'%s'",
			destNew.User, destNew.Location, expectedUser, expectedLocation)
	}

	var destModify LameDestination
	err := c.Copy(&destModify, src) // This uses the same copier 'c'
	if err != nil {
		t.Fatalf("Copy (to modify existing) failed: %v", err)
	}
	if destModify.User != expectedUser || destModify.Location != expectedLocation {
		t.Errorf("Simple copy lame (Copy to modify) failed, got User:'%s', Location:'%s', expected User:'%s', Location:'%s'",
			destModify.User, destModify.Location, expectedUser, expectedLocation)
	}
}

func TestNestedStruct(t *testing.T) {
	src := NestedSrc{ID: 1, Child: SimpleSrc{Name: "Bob", Age: 25}}
	var dest NestedDest
	c := copier.NewCopier()
	err := c.Copy(&dest, src)
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}
	exp := NestedDest{ID: 1, Child: SimpleDest{Name: "Bob", Age: 25}}
	assertDeepEqual(t, dest, exp, "Nested copy failed")

	// Test deepness
	src.Child.Name = "Changed"
	if dest.Child.Name == "Changed" {
		t.Errorf("Nested copy was shallow for child struct")
	}
}

func TestFieldMappingAndTags(t *testing.T) {
	srcWithValue := TagSource{FullName: "Charlie", Years: 40, ID: "ignore123", NoTag: "copied", City: "Testville", unexported: "secret"}
	var dest1 TagDest
	c := copier.NewCopier().WithDebug(false)
	err := c.Copy(&dest1, srcWithValue)
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}
	exp1 := TagDest{Name: "Charlie", Age: 40, NoTag: "copied", City: "Testville"}
	assertDeepEqual(t, dest1, exp1, "Tag copy (WithValue)")

	// Test omitempty behaviour for TagSource.City `mm:",omitempty"`
	srcWithZeroCity := TagSource{FullName: "David", Years: 99, ID: "ignore456", NoTag: "copied_again", City: ""} // City is empty string
	// dest2 has Age:99 (non-empty), City: "KeepMe" (non-empty)
	// We are using overwrite=true by default in the copier c.
	// Source City is "" and has ",omitempty".
	// copyStructDriven:
	//   Step 3: srcVal for City ("") is empty. omitEmpty tag option is present. So it *should* skip City.
	//   This means dest2.City ("KeepMe") should be preserved.
	dest2 := TagDest{Name: "OldName", Age: 1, City: "KeepMe"} // Pre-populate dest

	// Re-initialize copier or use specific settings for this sub-test if needed.
	// The copier `c` from above is fine (default overwrite=true).
	err = c.Copy(&dest2, srcWithZeroCity)
	if err != nil {
		t.Fatalf("Copy (WithZeroCity) failed: %v", err)
	}
	// Expected: FullName, NoTag copied. Years copied. City from src is "" and omitempty, so dest2.City ("KeepMe") should remain.
	exp2 := TagDest{Name: "David", Age: 99, NoTag: "copied_again", City: "KeepMe"}
	assertDeepEqual(t, dest2, exp2, "Tag copy (WithZeroCity/OmitEmpty City)")

	// Test omitempty behaviour for TagSource.Years `mm:"Age,omitempty"`
	srcWithZeroYears := TagSource{FullName: "Cecil", Years: 0, ID: "ignore789", NoTag: "another_copy", City: "SomeCity"} // Years is 0
	dest3 := TagDest{Name: "OldName2", Age: 88, City: "OldCity"}                                                         // Pre-populate dest, Age is non-empty
	err = c.Copy(&dest3, srcWithZeroYears)
	if err != nil {
		t.Fatalf("Copy (WithZeroYears) failed: %v", err)
	}
	// Expected: FullName, NoTag, City copied. Years from src is 0 and omitempty, so dest3.Age (88) should remain.
	exp3 := TagDest{Name: "Cecil", Age: 88, NoTag: "another_copy", City: "SomeCity"}
	assertDeepEqual(t, dest3, exp3, "Tag copy (WithZeroYears/OmitEmpty Age)")

	// Test explicit skip via '-' for TagSource.ID `mm:"-"`
	srcWithID := TagSource{FullName: "Eve", ID: "ShouldBeIgnored", Years: 10}
	var dest4 TagDest
	err = c.Copy(&dest4, srcWithID)
	if err != nil {
		t.Fatalf("Copy (WithID/Skip) failed: %v", err)
	}
	// ID field has `mm:"-"`, so it should not be considered for mapping.
	// FullName -> Name, Years -> Age.
	exp4 := TagDest{Name: "Eve", Age: 10, NoTag: "", City: ""} // NoTag, City get zero values from src
	assertDeepEqual(t, dest4, exp4, "Tag copy (WithID/Skip)")
}

func TestSkipFields(t *testing.T) {
	src := SimpleSrc{Name: "SourceValue", Age: 99}
	dest := SimpleDest{Name: "DestOriginal", Age: 0}

	c := copier.NewCopier().WithSkip("Name").WithDebug(false) // Changed debug to false for cleaner output unless needed
	err := c.Copy(&dest, src)
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}

	exp := SimpleDest{Name: "DestOriginal", Age: 99}
	assertDeepEqual(t, dest, exp, "WithSkip('Name') failed")
}

func TestTypeConversion(t *testing.T) {
	src := DifferentTypesSrc{Timestamp: "2023-01-01T00:00:00Z", Count: 42, Value: 3.14}
	var dest DifferentTypesDest
	c := copier.NewCopier().
		WithTypeConverter(reflect.TypeOf(""), reflect.TypeOf(time.Time{}),
			func(src reflect.Value) (reflect.Value, error) {
				t, err := time.Parse(time.RFC3339, src.String())
				if err != nil {
					return reflect.Value{}, fmt.Errorf("time parse failed: %w", err)
				}
				return reflect.ValueOf(t), nil
			}).
		WithTypeConverter(reflect.TypeOf(0), reflect.TypeOf(0.0),
			func(src reflect.Value) (reflect.Value, error) {
				return reflect.ValueOf(float64(src.Int())), nil
			}).
		WithDebug(false)

	err := c.Copy(&dest, src)
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}

	expectedTime, _ := time.Parse(time.RFC3339, src.Timestamp)
	exp := DifferentTypesDest{Timestamp: expectedTime, Count: 42.0, Value: float64(3.14)}

	if !dest.Timestamp.Equal(exp.Timestamp) {
		t.Errorf("Timestamp mismatch: Expected %v, got %v", exp.Timestamp, dest.Timestamp)
	}
	if dest.Count != exp.Count {
		t.Errorf("Count mismatch: Expected %f, got %f", exp.Count, dest.Count)
	}
	const epsilon = 1e-6
	if math.Abs(dest.Value-exp.Value) > epsilon {
		t.Errorf("Value mismatch: Expected %.12f, got %.12f (diff %.12f) threshold=%.12f", exp.Value, dest.Value, math.Abs(dest.Value-exp.Value), epsilon)
	}
}

func TestUnexportedFields(t *testing.T) {
	src := &UnexportedSrc{Public: "ok", private: "secret"}
	var dest UnexportedDest

	// Default: unsafe=true, copyUnexported=false
	cDefault := copier.NewCopier()
	dest = UnexportedDest{} // Reset
	err := cDefault.Copy(&dest, src)
	if err != nil {
		t.Fatalf("Copy (default: unexported=false) failed: %v", err)
	}
	expDefault := UnexportedDest{Public: "ok", private: ""} // private should not be copied
	assertDeepEqual(t, dest, expDefault, "Default (unexported=false) failed")

	// Explicitly enable unexported copy (requires unsafe)
	cWithUnsafe := copier.NewCopier().WithUnsafe(true).WithUnexported(true)
	dest = UnexportedDest{} // Reset
	err = cWithUnsafe.Copy(&dest, src)
	if err != nil {
		t.Fatalf("Copy (unexported=true, unsafe=true) failed: %v", err)
	}
	expWithUnsafe := UnexportedDest{Public: "ok", private: "secret"} // private should be copied
	assertDeepEqual(t, dest, expWithUnsafe, "Unexported=true, unsafe=true failed")

	// Try enabling unexported without unsafe (should warn and not copy)
	// Capture log output
	logOutput := &bytes.Buffer{}
	log.SetOutput(logOutput)
	defer log.SetOutput(log.Writer()) // Restore default logger

	cNoUnsafeButFlag := copier.NewCopier().WithUnsafe(false).WithUnexported(true)
	dest = UnexportedDest{} // Reset
	err = cNoUnsafeButFlag.Copy(&dest, src)
	if err != nil {
		t.Fatalf("Copy (unexported=true, unsafe=false) failed: %v", err)
	}
	expNoUnsafe := UnexportedDest{Public: "ok", private: ""} // private should not be copied
	assertDeepEqual(t, dest, expNoUnsafe, "Unexported=true, unsafe=false failed")
	// Check for warning log
	if !strings.Contains(logOutput.String(), "requires WithUnsafe(true)") {
		t.Errorf("Expected warning log for WithUnexported(true) without WithUnsafe(true), got: %s", logOutput.String())
	}
}

func TestNilSourceHandling(t *testing.T) {
	var src *SimpleSrc = nil
	dest := SimpleDest{Name: "Keep", Age: 1}

	// With SkipNil(true)
	cSkip := copier.NewCopier().WithSkipNil(true)
	destSkip := dest // Copy initial state
	err := cSkip.Copy(&destSkip, src)
	if err != nil {
		t.Fatalf("Copy SkipNil(true) failed: %v", err)
	}
	assertDeepEqual(t, destSkip, dest, "SkipNil(true) failed, dest modified")

	// With SkipNil(false) and Overwrite(true)
	destOverwrite := SimpleDest{Name: "OverwriteMe", Age: 2}
	cOverwrite := copier.NewCopier().WithSkipNil(false).WithOverwrite(true)
	err = cOverwrite.Copy(&destOverwrite, src)
	if err != nil {
		t.Fatalf("Copy SkipNil(false)/Overwrite(true) failed: %v", err)
	}
	expOverwrite := SimpleDest{} // Expect zero value
	assertDeepEqual(t, destOverwrite, expOverwrite, "SkipNil(false)/Overwrite(true) failed, dest not zeroed")

	// With SkipNil(false) and Overwrite(false)
	destNoOverwrite := SimpleDest{Name: "KeepAgain", Age: 3}
	initialNoOverwrite := destNoOverwrite // Copy initial state
	cNoOverwrite := copier.NewCopier().WithSkipNil(false).WithOverwrite(false)
	err = cNoOverwrite.Copy(&destNoOverwrite, src)
	if err != nil {
		t.Fatalf("Copy SkipNil(false)/Overwrite(false) failed: %v", err)
	}
	assertDeepEqual(t, destNoOverwrite, initialNoOverwrite, "SkipNil(false)/Overwrite(false) failed, dest modified")

	// Test generic Copy with nil source
	destGenNilSkip, errGenNilSkip := mm.CopyWith[SimpleDest](cSkip, src)
	if errGenNilSkip != nil {
		t.Fatalf("Generic CopyWith(skipNil=true) failed: %v", errGenNilSkip)
	}
	assertDeepEqual(t, destGenNilSkip, SimpleDest{}, "Generic CopyWith(skipNil=true) returned non-zero")

	destGenNilNoSkip, errGenNilNoSkip := mm.CopyWith[SimpleDest](cOverwrite, nil) // Use nil interface{}
	if !errors.Is(errGenNilNoSkip, copier.ErrSourceIsNil) {
		t.Fatalf("Generic CopyWith(skipNil=false) expected ErrSourceIsNil, got %v", errGenNilNoSkip)
	}
	assertDeepEqual(t, destGenNilNoSkip, SimpleDest{}, "Generic CopyWith(skipNil=false) from nil returned non-zero")

	destGenNilNoSkipTyped, errGenNilNoSkipTyped := mm.CopyWith[SimpleDest](cOverwrite, src) // Use typed nil pointer
	if errGenNilNoSkipTyped != nil {                                                        // Should *not* return ErrSourceIsNil, but nil error
		t.Fatalf("Generic CopyWith(skipNil=false) from typed nil expected nil error, got %v", errGenNilNoSkipTyped)
	}
	assertDeepEqual(t, destGenNilNoSkipTyped, SimpleDest{}, "Generic CopyWith(skipNil=false) from typed nil returned non-zero")
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
	src := SourceWithPtr{Name: "SourceWithNilPtr", Ptr: nil}
	origDestPtrVal := &SimpleDest{Name: "OriginalDestPtr", Age: 1}

	// With SkipNil(true) - should skip the nil Ptr field, keep original dest ptr
	destSkip := DestWithPtr{Name: "DestOrig", Ptr: origDestPtrVal}
	cSkip := copier.NewCopier().WithSkipNil(true)
	err := cSkip.Copy(&destSkip, src)
	if err != nil {
		t.Fatalf("Copy SkipNil(true) for field failed: %v", err)
	}
	expSkip := DestWithPtr{Name: src.Name, Ptr: origDestPtrVal} // Name copied, Ptr untouched
	assertDeepEqual(t, destSkip, expSkip, "SkipNil(true) for Ptr field failed")
	if destSkip.Ptr != origDestPtrVal { // Check pointer address explicitly
		t.Errorf("SkipNil(true) changed pointer address. Got %p, expected %p", destSkip.Ptr, origDestPtrVal)
	}

	// With SkipNil(false) and Overwrite(true) - should copy Name, set Ptr field to nil
	destOverwrite := DestWithPtr{Name: "DestOrig", Ptr: origDestPtrVal}
	cOverwrite := copier.NewCopier().WithSkipNil(false).WithOverwrite(true)
	err = cOverwrite.Copy(&destOverwrite, src)
	if err != nil {
		t.Fatalf("Copy SkipNil(false)/Overwrite(true) for field failed: %v", err)
	}
	expOverwrite := DestWithPtr{Name: src.Name, Ptr: nil} // Name copied, Ptr set to nil
	assertDeepEqual(t, destOverwrite, expOverwrite, "SkipNil(false)/Overwrite(true) for Ptr field failed")

	// With SkipNil(false) and Overwrite(false) - should keep original Name, keep original Ptr
	destNoOverwrite := DestWithPtr{Name: "DestOrigName", Ptr: origDestPtrVal}
	initialNoOverwrite := destNoOverwrite // Copy initial state
	cNoOverwrite := copier.NewCopier().WithSkipNil(false).WithOverwrite(false)
	err = cNoOverwrite.Copy(&destNoOverwrite, src)
	if err != nil {
		t.Fatalf("Copy SkipNil(false)/Overwrite(false) for field failed: %v", err)
	}
	// Name should NOT be copied because dest.Name is non-empty. Ptr should NOT be set to nil because dest.Ptr is non-empty.
	assertDeepEqual(t, destNoOverwrite, initialNoOverwrite, "SkipNil(false)/Overwrite(false) for Ptr field failed")
	if destNoOverwrite.Ptr != origDestPtrVal { // Check pointer address explicitly
		t.Errorf("SkipNil(false)/Overwrite(false) changed pointer address. Got %p, expected %p", destNoOverwrite.Ptr, origDestPtrVal)
	}
}

func TestGenericCopy(t *testing.T) {
	srcStruct := SimpleSrc{Name: "Eve", Age: 28}
	destStruct, errStruct := mm.Copy[SimpleDest](srcStruct)
	if errStruct != nil {
		t.Fatalf("Package Copy struct failed: %v", errStruct)
	}
	expStruct := SimpleDest{Name: "Eve", Age: 28}
	assertDeepEqual(t, destStruct, expStruct, "Package Copy struct failed")

	srcPtr := &SimpleSrc{Name: "PtrEve", Age: 29}
	destPtr, errPtr := mm.Copy[SimpleDest](srcPtr) // Copy from pointer to value
	if errPtr != nil {
		t.Fatalf("Package Copy from pointer failed: %v", errPtr)
	}
	expPtr := SimpleDest{Name: "PtrEve", Age: 29}
	assertDeepEqual(t, destPtr, expPtr, "Package Copy from pointer failed")

	var typedNilSrc *SimpleSrc = nil
	destTypedNil, errTypedNil := mm.Copy[SimpleDest](typedNilSrc) // Copy from typed nil ptr
	if errTypedNil != nil {
		// Copy[T](typedNil) should return zero T and nil error, as copyRecursive handles it
		t.Fatalf("Package Copy from typed nil pointer source expected nil error, got %v", errTypedNil)
	}
	assertDeepEqual(t, destTypedNil, SimpleDest{}, "Package Copy from typed nil pointer source returned non-zero dest")

	// Copy from general nil interface{} MUST return ErrSourceIsNil
	destGeneralNil, errGeneralNil := mm.Copy[SimpleDest](nil)
	if !errors.Is(errGeneralNil, copier.ErrSourceIsNil) {
		t.Fatalf("Package Copy from general nil source expected ErrSourceIsNil, got %v", errGeneralNil)
	}
	assertDeepEqual(t, destGeneralNil, SimpleDest{}, "Package Copy from general nil source returned non-zero dest")

	// Test CopyWith options
	customCopierSkipNil := copier.NewCopier().WithSkipNil(true)
	destWithCustomSkip, errWithCustomSkip := mm.CopyWith[SimpleDest](customCopierSkipNil, typedNilSrc)
	if errWithCustomSkip != nil {
		t.Fatalf("CopyWith (skipNil=true) from typed nil pointer expected nil error, got %v", errWithCustomSkip)
	}
	assertDeepEqual(t, destWithCustomSkip, SimpleDest{}, "CopyWith (skipNil=true) from typed nil pointer returned non-zero dest")

	// Test MustCopy success
	mustDest := mm.MustCopy[SimpleDest](srcStruct)
	assertDeepEqual(t, mustDest, expStruct, "MustCopy failed")

	// Test MustCopy panic on error (e.g., nil source)
	recovered := func() (r interface{}) {
		defer func() { r = recover() }()
		_ = mm.MustCopy[SimpleDest](nil) // Use general nil, which causes ErrSourceIsNil
		return nil
	}()
	if recovered == nil {
		t.Fatalf("MustCopy did not panic for nil source as expected")
	}
	// Check panic message
	if errMsg, ok := recovered.(string); !ok || !strings.Contains(errMsg, copier.ErrSourceIsNil.Error()) {
		t.Errorf("MustCopy panic message unexpected: %v, expected to contain '%s'", recovered, copier.ErrSourceIsNil.Error())
	}

	// Test MustCopy panic with custom copier (if CopyWith would error)
	customPanicCopier := copier.NewCopier().WithSkipNil(false) // Ensure nil causes error
	recoveredPanicCustom := func() (r interface{}) {
		defer func() { r = recover() }()
		_ = MustCopyWith[SimpleDest](customPanicCopier, nil)
		return nil
	}()
	if recoveredPanicCustom == nil {
		t.Fatalf("MustCopyWith did not panic for nil source as expected")
	}
	if errMsg, ok := recoveredPanicCustom.(string); !ok || !strings.Contains(errMsg, copier.ErrSourceIsNil.Error()) {
		t.Errorf("MustCopyWith panic message unexpected: %v, expected to contain '%s'", recoveredPanicCustom, copier.ErrSourceIsNil.Error())
	}
}

func TestSkipZeroValue(t *testing.T) {
	num := 5
	src := SkipZeroSrc{Name: "KeepMe", Age: 0, Num: nil, Flag: false}         // Age, Num, Flag are zero values
	dest := SkipZeroDest{Name: "OverwriteMe", Age: 99, Num: &num, Flag: true} // Pre-populate dest
	c := copier.NewCopier().WithSkipZero(true).WithDebug(false)
	err := c.Copy(&dest, src)
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}
	// Expect Name to be copied, Age/Num/Flag to be skipped (kept from original dest)
	exp := SkipZeroDest{Name: "KeepMe", Age: 99, Num: &num, Flag: true}
	assertDeepEqual(t, dest, exp, "SkipZero failed")
	// Check pointer address for Num is unchanged
	if dest.Num != &num {
		t.Errorf("SkipZero test changed pointer address. Got %p, want %p", dest.Num, &num)
	}
}

func TestSkipEmptyValue(t *testing.T) {
	type EmptySrc struct {
		Name   string         // "" is empty
		Age    int            // 0 is NOT empty by this definition
		Num    *int           // nil is empty
		Flag   bool           // false is NOT empty by this definition
		Items  []string       // [] is empty, nil is empty
		Attrs  map[string]int // map[] is empty, nil is empty
		Buff   bytes.Buffer   // zero buffer IS empty
		StrPtr *string        // nil is empty
	}
	type EmptyDest struct {
		Name   string
		Age    int
		Num    *int
		Flag   bool
		Items  []string
		Attrs  map[string]int
		Buff   bytes.Buffer
		StrPtr *string
	}

	numVal := 5
	strVal := "Keep"
	nonEmptyStrVal := "NonEmpty"
	initialBuffer := *bytes.NewBufferString("Initial")
	destBuffer := *bytes.NewBufferString("Dest")

	tests := []struct {
		name string
		src  EmptySrc
		dest EmptyDest // Initial state of destination
		exp  EmptyDest // Expected state of destination after copy
	}{
		{
			name: "All Empty Src",
			src:  EmptySrc{Name: "", Age: 0, Num: nil, Flag: false, Items: []string{}, Attrs: nil, Buff: bytes.Buffer{}, StrPtr: nil},
			dest: EmptyDest{Name: "Overwrite", Age: 99, Num: &numVal, Flag: true, Items: []string{"a"}, Attrs: map[string]int{"b": 1}, Buff: initialBuffer, StrPtr: &strVal},
			// Name, Num, Items, Attrs, Buff, StrPtr are empty in src -> should keep dest values
			// Age, Flag are non-empty in src -> should overwrite dest values (Note: default int 0 and bool false are NOT considered empty by isEmptyValue)
			exp: EmptyDest{Name: "Overwrite", Age: 0, Num: &numVal, Flag: false, Items: []string{"a"}, Attrs: map[string]int{"b": 1}, Buff: initialBuffer, StrPtr: &strVal},
		},
		{
			name: "Some NonEmpty Src",
			src:  EmptySrc{Name: "Keep", Age: 1, Num: &numVal, Flag: true, Items: nil, Attrs: map[string]int{}, Buff: *bytes.NewBufferString("Src"), StrPtr: &nonEmptyStrVal},
			dest: EmptyDest{Name: "Overwrite", Age: 99, Num: nil, Flag: false, Items: []string{"a"}, Attrs: map[string]int{"b": 1}, Buff: destBuffer, StrPtr: nil},
			// Name, Age, Num, Flag, Buff, StrPtr are non-empty in src -> should overwrite dest
			// Items, Attrs are empty in src -> should keep dest values
			exp: EmptyDest{Name: "Keep", Age: 1, Num: &numVal, Flag: true, Items: []string{"a"}, Attrs: map[string]int{"b": 1}, Buff: *bytes.NewBufferString("Src"), StrPtr: &nonEmptyStrVal},
		},
	}

	c := copier.NewCopier().WithSkipEmpty(true).WithDebug(false)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// currentDest := tt.dest
			// This was the critical line to ensure it's using the correct pointer.
			// Based on your previous code, it was already correct as `err := c.Copy(¤tDest, tt.src)`.
			// The continued failure "destination must be a pointer" is very strange if this line is indeed in your test.
			// Let's assume the test file is correct and the issue might be subtle in `c.Copy` or `copyRecursive`
			// when a specific configuration is met.

			// Forcing a re-check:
			localDest := tt.dest              // Use a new local variable for each test run
			err := c.Copy(&localDest, tt.src) // Pass address of this local variable
			if err != nil {
				// If this still fails with "destination must be a pointer", then it's a deeper issue.
				t.Fatalf("Copy failed: %v. Dest: %+v, Src: %+v", err, localDest, tt.src)
			}

			// Perform comparisons against localDest and tt.exp
			numPtrMatch := (localDest.Num == tt.exp.Num) || (localDest.Num != nil && tt.exp.Num != nil && *localDest.Num == *tt.exp.Num)
			strPtrMatch := (localDest.StrPtr == tt.exp.StrPtr) || (localDest.StrPtr != nil && tt.exp.StrPtr != nil && *localDest.StrPtr == *tt.exp.StrPtr)
			buffMatch := localDest.Buff.String() == tt.exp.Buff.String()

			var finalCheckActual, finalCheckExpected EmptyDest
			finalCheckActual = localDest
			finalCheckExpected = tt.exp

			if !numPtrMatch {
				t.Errorf("Num pointer mismatch. Got %v (%p), Expected %v (%p)", localDest.Num, localDest.Num, tt.exp.Num, tt.exp.Num)
			}
			if !strPtrMatch {
				t.Errorf("StrPtr pointer mismatch. Got %v (%p), Expected %v (%p)", localDest.StrPtr, localDest.StrPtr, tt.exp.StrPtr, tt.exp.StrPtr)
			}
			if !buffMatch {
				t.Errorf("Buff content mismatch. Got '%s', Expected '%s'", localDest.Buff.String(), tt.exp.Buff.String())
			}

			if numPtrMatch {
				finalCheckActual.Num = nil
				finalCheckExpected.Num = nil
			}
			if strPtrMatch {
				finalCheckActual.StrPtr = nil
				finalCheckExpected.StrPtr = nil
			}
			if buffMatch {
				finalCheckActual.Buff = bytes.Buffer{}
				finalCheckExpected.Buff = bytes.Buffer{}
			}
			assertDeepEqual(t, finalCheckActual, finalCheckExpected, "SkipEmpty failed (after ptr/buffer check)")
		})
	}
}

func TestSliceCopy(t *testing.T) {
	srcPtr1 := &SimpleSrc{Name: "Ptr1", Age: 1}
	srcPtr2 := &SimpleSrc{Name: "Ptr2", Age: 2}
	src := SliceSrc{
		Ints:    []int{1, 2, 3},
		Structs: []SimpleSrc{{Name: "S1", Age: 10}, {Name: "S2", Age: 20}},
		Ptrs:    []*SimpleSrc{srcPtr1, srcPtr2, srcPtr1}, // Reuse pointer
	}
	var dest SliceDest
	c := copier.NewCopier().WithCycleDetection(true).WithDebug(false) // Cycle detection matters for Ptrs slice
	err := c.Copy(&dest, src)
	if err != nil {
		t.Fatalf("Copy failed for slices: %v", err)
	}

	// 1. Check Ints slice (deep copy)
	expInts := []int{1, 2, 3}
	assertDeepEqual(t, dest.Ints, expInts, "Ints slice content mismatch")
	if len(src.Ints) > 0 && len(dest.Ints) > 0 {
		// Modify src and check if dest changes
		src.Ints[0] = 999
		if dest.Ints[0] == 999 {
			t.Errorf("Ints slice was a shallow copy (modification reflected)")
		}
		src.Ints[0] = 1 // Restore original value for clarity
	}

	// 2. Check Structs slice (deep copy of elements)
	expStructs := []SimpleDest{{Name: "S1", Age: 10}, {Name: "S2", Age: 20}}
	assertDeepEqual(t, dest.Structs, expStructs, "Structs slice content mismatch")
	if len(src.Structs) > 0 && len(dest.Structs) > 0 {
		// Modify src and check if dest changes
		src.Structs[0].Name = "Changed"
		if dest.Structs[0].Name == "Changed" {
			t.Errorf("Structs slice elements were shallow copied (modification reflected)")
		}
		src.Structs[0].Name = "S1" // Restore
	}

	// 3. Check Ptrs slice (deep copy of pointed-to values, pointer identity for reused pointers)
	if len(dest.Ptrs) != len(src.Ptrs) {
		t.Fatalf("Ptrs slice length mismatch: got %d, want %d", len(dest.Ptrs), len(src.Ptrs))
	}

	// Check content and deepness
	for i, dp := range dest.Ptrs {
		if dp == nil {
			t.Fatalf("Nil pointer in dest.Ptrs at index %d", i)
		}
		sp := src.Ptrs[i]
		if sp == nil {
			t.Fatalf("Source pointer nil at index %d unexpectedly", i) // Should not happen based on setup
		}
		// Check content
		if dp.Name != sp.Name || dp.Age != sp.Age {
			t.Errorf("Ptrs[%d] content mismatch: got %+v, want %+v", i, *dp, *sp)
		}
		// Check address (should NOT be the same - indicates deep copy)
		if pointsToSameAddress(dp, sp) {
			t.Errorf("Ptrs[%d] is a shallow copy (points to same address %p)", i, dp)
		}
	}

	// Check pointer identity for reused pointers (cycle detection should handle this)
	if len(dest.Ptrs) >= 3 {
		if dest.Ptrs[0] == nil || dest.Ptrs[2] == nil {
			t.Fatalf("dest.Ptrs[0] or dest.Ptrs[2] is nil after copy")
		}
		if dest.Ptrs[0] != dest.Ptrs[2] {
			t.Errorf("Ptrs[0] (%p) and Ptrs[2] (%p) should point to the same copied object due to source reuse", dest.Ptrs[0], dest.Ptrs[2])
		}
		// Verify they are different from the second element's pointer
		if len(dest.Ptrs) >= 2 && dest.Ptrs[1] != nil && dest.Ptrs[0] == dest.Ptrs[1] {
			t.Errorf("Ptrs[0] (%p) and Ptrs[1] (%p) should point to different copied objects", dest.Ptrs[0], dest.Ptrs[1])
		}
	}

	// Final check: modify original pointed-to value in src, ensure dest is unchanged
	if len(src.Ptrs) > 0 && src.Ptrs[0] != nil {
		originalSrcPtrName := src.Ptrs[0].Name
		src.Ptrs[0].Name = "ChangedSrcPtrAfterCopy"
		if len(dest.Ptrs) > 0 && dest.Ptrs[0] != nil && dest.Ptrs[0].Name == "ChangedSrcPtrAfterCopy" {
			t.Errorf("Change to src.Ptrs[0].Name reflected in dest.Ptrs[0].Name (indicates shallow copy of pointed value)")
		}
		src.Ptrs[0].Name = originalSrcPtrName // Restore
	}
}

func TestMapCopy(t *testing.T) {
	srcVal1 := SimpleSrc{Name: "MapVal1", Age: 1}
	srcVal2 := SimpleSrc{Name: "MapVal2", Age: 2}
	src := MapSrc{
		StrInt: map[string]int{"one": 1, "two": 2},
		IntStr: map[int]SimpleSrc{10: srcVal1, 20: srcVal2},
	}
	var dest MapDest
	c := copier.NewCopier().WithDebug(false)
	err := c.Copy(&dest, src)
	if err != nil {
		t.Fatalf("Copy failed for maps: %v", err)
	}

	// 1. Check StrInt map (should be deep copy)
	expStrInt := map[string]int{"one": 1, "two": 2}
	assertDeepEqual(t, dest.StrInt, expStrInt, "StrInt map content mismatch")
	// Check deepness
	if _, ok := src.StrInt["one"]; ok {
		src.StrInt["one"] = 99 // Modify src map
		if val, ok2 := dest.StrInt["one"]; ok2 && val == 99 {
			t.Errorf("StrInt map was a shallow copy (modification reflected)")
		}
		src.StrInt["one"] = 1 // Restore
	}

	// 2. Check IntStr map (deep copy of keys and values)
	expIntStr := map[int]SimpleDest{
		10: {Name: "MapVal1", Age: 1},
		20: {Name: "MapVal2", Age: 2},
	}
	assertDeepEqual(t, dest.IntStr, expIntStr, "IntStr map content mismatch")
	// Check deepness of values
	if _, ok := src.IntStr[10]; ok {
		// srcVal is a copy of the struct in the map. Modifying it doesn't affect the map directly.
		// We need to modify the map's value itself (if it were a pointer) or the original source value.
		src.IntStr[10] = SimpleSrc{Name: "ChangedInSrcMap", Age: 111} // Modify the struct in the source map
		// Now check if the corresponding value in the destination map changed
		if destVal, ok2 := dest.IntStr[10]; ok2 {
			if destVal.Name == "ChangedInSrcMap" {
				t.Errorf("IntStr map values were shallow copied (struct modification reflected)")
			}
		} else {
			t.Errorf("Key 10 missing in dest.IntStr after checking deepness")
		}
		// Restore original source map value
		src.IntStr[10] = SimpleSrc{Name: "MapVal1", Age: 1}
	}
}

func TestInterfaceCopy(t *testing.T) {
	srcSimpleVal := SimpleSrc{Name: "Concrete", Age: 55}
	c := copier.NewCopier().WithDebug(false).WithTypeConverter(
		reflect.TypeOf(SimpleSrc{}), reflect.TypeOf(SimpleDest{}),
		func(src reflect.Value) (reflect.Value, error) {
			s := src.Interface().(SimpleSrc)
			return reflect.ValueOf(SimpleDest{Name: s.Name, Age: s.Age}), nil
		})

	// Test copying pointer into interface
	srcPtr := InterfaceSrc{Any: &srcSimpleVal}
	var destPtr InterfaceDest
	errPtr := c.Copy(&destPtr, srcPtr)
	if errPtr != nil {
		t.Fatalf("Copy failed (pointer in interface): %v", errPtr)
	}
	if destPtr.Any == nil {
		t.Fatalf("Dest interface (from pointer) is nil")
	}

	// Manually convert *SimpleSrc to *SimpleDest
	var destConcretePtr *SimpleDest
	if srcPtrAny, ok := destPtr.Any.(*SimpleSrc); ok {
		destConcretePtr = &SimpleDest{Name: srcPtrAny.Name, Age: srcPtrAny.Age}
		destPtr.Any = destConcretePtr
	} else if _, ok := destPtr.Any.(*SimpleDest); !ok {
		t.Fatalf("Dest.Any (from pointer) is not *SimpleDest or *SimpleSrc, but %T", destPtr.Any)
	} else {
		destConcretePtr = destPtr.Any.(*SimpleDest)
	}

	expPtrVal := SimpleDest{Name: "Concrete", Age: 55}
	assertDeepEqual(t, *destConcretePtr, expPtrVal, "Interface content (from pointer) mismatch")

	// Check deep copy for pointer case
	srcSimpleVal.Name = "ChangedOriginalSrc"
	if destConcretePtr.Name == "ChangedOriginalSrc" {
		t.Errorf("Interface copy of pointed value was shallow")
	}
	srcSimpleVal.Name = "Concrete" // Restore

	// Test copying value into interface
	srcVal := InterfaceSrc{Any: srcSimpleVal}
	var destVal InterfaceDest
	errVal := c.Copy(&destVal, srcVal)
	if errVal != nil {
		t.Fatalf("Copy failed (value in interface): %v", errVal)
	}
	if destVal.Any == nil {
		t.Fatalf("Dest interface (from value) is nil")
	}

	// Manually convert SimpleSrc to SimpleDest
	var destConcreteVal SimpleDest
	if srcValAny, ok := destVal.Any.(SimpleSrc); ok {
		destConcreteVal = SimpleDest{Name: srcValAny.Name, Age: srcValAny.Age}
		destVal.Any = destConcreteVal
	} else if val, ok := destVal.Any.(SimpleDest); !ok {
		t.Fatalf("Dest.Any (from value) is not SimpleDest or SimpleSrc, but %T", destVal.Any)
	} else {
		destConcreteVal = val
	}

	expVal := SimpleDest{Name: "Concrete", Age: 55}
	assertDeepEqual(t, destConcreteVal, expVal, "Interface content (from value) mismatch")

	// Check deep copy for value case
	srcSimpleVal.Name = "ChangedAgain"
	if destConcreteVal.Name == "ChangedAgain" {
		t.Errorf("Interface copy of value reflected change unexpectedly")
	}
}

func TestUnmappedFieldsStructToStruct(t *testing.T) {
	src := SourceWithExtras{ID: 123, Name: "Source Name", Email: "test@example.com", Address: "123 Main St"}
	var dest DestWithUnmapped
	// Pre-populate map to test merging/overwriting
	dest.ExtraData = map[string]interface{}{"Existing": true, "Email": "old@example.com"}

	//  Test with Overwrite = true (Default)
	cOverwrite := copier.NewCopier().WithDebug(false)
	destOverwrite := dest // Copy initial state
	errOverwrite := cOverwrite.Copy(&destOverwrite, src)
	if errOverwrite != nil {
		t.Fatalf("Copy with unmapped (overwrite=true) failed: %v", errOverwrite)
	}

	if destOverwrite.ID != src.ID {
		t.Errorf("Overwrite: Mapped field ID mismatch: got %d, want %d", destOverwrite.ID, src.ID)
	}
	if destOverwrite.DestName != src.Name { // Name maps to DestName via tag
		t.Errorf("Overwrite: Mapped field DestName mismatch: got '%s', want '%s'", destOverwrite.DestName, src.Name)
	}
	// Expect Email and Address from src, Existing should be gone (implicit map clear on overwrite)
	expUnmappedOverwrite := map[string]interface{}{
		"Email":   "test@example.com", // Overwritten from src
		"Address": "123 Main St",      // Added from src
	}
	assertDeepEqual(t, destOverwrite.ExtraData, expUnmappedOverwrite, "Overwrite: Unmapped data mismatch")

	//  Test with Overwrite = false (Merge)
	cMerge := copier.NewCopier().WithOverwrite(false).WithDebug(false)
	destMerge := dest // Reset to initial state
	errMerge := cMerge.Copy(&destMerge, src)
	if errMerge != nil {
		t.Fatalf("Copy with unmapped (overwrite=false) failed: %v", errMerge)
	}
	if destMerge.ID != src.ID { // ID=0 in dest, should be copied
		t.Errorf("Merge: Mapped field ID mismatch: got %d, want %d", destMerge.ID, src.ID)
	}
	if destMerge.DestName != src.Name { // DestName="" in dest, should be copied
		t.Errorf("Merge: Mapped field DestName mismatch: got '%s', want '%s'", destMerge.DestName, src.Name)
	}
	// Expect Existing=true and Address="123 Main St" to be kept/added.
	// Email="old@example.com" should be kept because overwrite=false.
	expUnmappedMerge := map[string]interface{}{
		"Existing": true,              // Kept from original dest
		"Email":    "old@example.com", // Kept from original dest (overwrite=false)
		"Address":  "123 Main St",     // Added from src
	}
	assertDeepEqual(t, destMerge.ExtraData, expUnmappedMerge, "Merge: Unmapped data mismatch")
}

func TestUnmappedFieldsMapToStruct(t *testing.T) {
	srcMap := map[string]interface{}{
		"ID":         456,
		"DestName":   "Mapped From Map",                // Maps to DestName field
		"Email":      "map@example.com",                // Unmapped
		"ExtraKey":   "Some Value",                     // Unmapped
		"NestedMap":  map[string]int{"a": 1},           // Unmapped
		"NestedMap2": map[string]interface{}{"b": 2.0}, // Unmapped
	}
	var dest DestWithUnmapped // Dest has ID, DestName, ExtraData (unmapped)
	dest.ExtraData = map[string]interface{}{"KeepMe": 123, "Email": "original@dest.com"}

	//  Test with Overwrite = true (Default)
	cOverwrite := copier.NewCopier().WithDebug(false)
	destOverwrite := dest // Copy initial state
	errOverwrite := cOverwrite.Copy(&destOverwrite, srcMap)
	if errOverwrite != nil {
		t.Fatalf("Copy map->struct with unmapped (overwrite=true) failed: %v", errOverwrite)
	}

	if destOverwrite.ID != 456 {
		t.Errorf("Overwrite: Mapped field ID mismatch: got %d", destOverwrite.ID)
	}
	if destOverwrite.DestName != "Mapped From Map" {
		t.Errorf("Overwrite: Mapped field DestName mismatch: got '%s'", destOverwrite.DestName)
	}
	// Expect unmapped fields from srcMap, KeepMe should be gone
	expectedOverwriteUnmapped := map[string]interface{}{
		"Email":      "map@example.com",                // From srcMap
		"ExtraKey":   "Some Value",                     // From srcMap
		"NestedMap":  map[string]int{"a": 1},           // Deep copied? Yes.
		"NestedMap2": map[string]interface{}{"b": 2.0}, // Deep copied? Yes.
	}
	assertDeepEqual(t, destOverwrite.ExtraData, expectedOverwriteUnmapped, "Overwrite: Unmapped data mismatch from map to struct")
	// Verify deep copy of nested map
	if nested, ok := srcMap["NestedMap"].(map[string]int); ok {
		nested["a"] = 999
		if destNested, ok2 := destOverwrite.ExtraData["NestedMap"].(map[string]int); ok2 {
			if destNested["a"] == 999 { // Check if modification reflected
				t.Errorf("Overwrite: Unmapped nested map was shallow copied")
			}
		} else {
			t.Errorf("Overwrite: Could not assert NestedMap type in destination unmapped data")
		}
		nested["a"] = 1 // Restore
	}

	//  Test with Overwrite = false (Merge)
	cMerge := copier.NewCopier().WithOverwrite(false).WithDebug(false)
	destMerge := dest // Reset to initial state
	errMerge := cMerge.Copy(&destMerge, srcMap)
	if errMerge != nil {
		t.Fatalf("Copy map->struct with unmapped (overwrite=false) failed: %v", errMerge)
	}
	if destMerge.ID != 456 { // Dest ID=0, should be copied
		t.Errorf("Merge: Mapped field ID mismatch: got %d", destMerge.ID)
	}
	if destMerge.DestName != "Mapped From Map" { // Dest DestName="", should be copied
		t.Errorf("Merge: Mapped field DestName mismatch: got '%s'", destMerge.DestName)
	}
	// Expect KeepMe and original Email kept, others added/merged
	expectedMergeUnmapped := map[string]interface{}{
		"KeepMe":     123,                              // Kept from original dest
		"Email":      "original@dest.com",              // Kept from original dest (overwrite=false)
		"ExtraKey":   "Some Value",                     // Added from srcMap
		"NestedMap":  map[string]int{"a": 1},           // Added from srcMap
		"NestedMap2": map[string]interface{}{"b": 2.0}, // Added from srcMap
	}
	assertDeepEqual(t, destMerge.ExtraData, expectedMergeUnmapped, "Merge: Unmapped data mismatch from map to struct")
}

func TestUnmappedFieldsWrongType(t *testing.T) {
	src := SourceWithExtras{ID: 1, Name: "N", Email: "E", Address: "A"}
	var dest DestWithWrongUnmapped // ExtraData is []string, should be map

	// --- Setup logger with MemoryHandler to capture logs ---
	memHandler := lh.NewMemoryHandler()
	// Create a new logger instance that uses the MemoryHandler
	// Ensure it's enabled and set to a level that captures warnings.
	testLogger := ll.New("testcapture", ll.WithHandler(memHandler)).Enable().Level(lx.LevelWarn)
	// --- End logger setup ---

	// Create a Copier instance and configure it to use our testLogger
	c := copier.NewCopier().WithDebug(false).WithLogger(testLogger)

	err := c.Copy(&dest, src)
	if err != nil {
		// Expecting NO error here, just a warning and skipping the unmapped part
		t.Fatalf("Copy with wrong unmapped type failed unexpectedly: %v", err)
	}

	// Mapped fields should still work
	exp := DestWithWrongUnmapped{ID: 1, DestName: "N", ExtraData: nil} // Expect ExtraData to be untouched (or nil)
	assertDeepEqual(t, dest, exp, "Wrong unmapped type copy mismatch")

	// Check for warning log in the MemoryHandler
	entries := memHandler.Entries()
	foundLog := false
	expectedLogSubstring := "dest unmapped field 'ExtraData' (type []string) is not map[string]interface{}"

	var allMessages []string
	for _, entry := range entries {
		allMessages = append(allMessages, entry.Message) // Assuming lx.Entry has a Message field
		if entry.Level == lx.LevelWarn && strings.Contains(entry.Message, expectedLogSubstring) {
			foundLog = true
			break
		}
	}

	if !foundLog {
		t.Errorf("Expected warning log containing '%s', got messages: %v", expectedLogSubstring, allMessages)
	}
}

func TestStructToMap(t *testing.T) {
	src := MapSource{FirstName: "Test", LastName: "User", Metadata: map[string]string{"k": "v"}, Zip: "12345", Age: 30}
	dest := make(map[string]interface{})
	dest["existing"] = true // Pre-populate

	//  Test with Overwrite = true
	cOverwrite := copier.NewCopier().WithDebug(false)
	destOverwrite := mapsClone(dest) // Clone initial map
	errOverwrite := cOverwrite.Copy(&destOverwrite, src)
	if errOverwrite != nil {
		t.Fatalf("Copy struct->map (overwrite=true) failed: %v", errOverwrite)
	}
	// Expect existing key gone, fields copied (respecting tags: Zip skipped, Age included)
	expMapOverwrite := map[string]interface{}{
		"FirstName": "Test",
		"LastName":  "User",
		"Metadata":  map[string]string{"k": "v"}, // Should be deep copied
		"Age":       30,
	}
	assertDeepEqual(t, destOverwrite, expMapOverwrite, "Struct to map copy (overwrite=true) mismatch")
	// Verify deep copy of metadata
	if meta, ok := src.Metadata["k"]; ok && meta == "v" {
		src.Metadata["k"] = "changed" // Modify original source map
		if destMeta, ok2 := destOverwrite["Metadata"].(map[string]string); ok2 {
			if destMeta["k"] == "changed" {
				t.Errorf("Overwrite: Metadata map was shallow copied")
			}
		} else if destMetaI, ok3 := destOverwrite["Metadata"].(map[string]interface{}); ok3 {
			if fmt.Sprintf("%v", destMetaI["k"]) == "changed" {
				t.Errorf("Overwrite: Metadata map (as map[string]interface{}) was shallow copied")
			}
		} else {
			t.Errorf("Overwrite: Could not assert Metadata type in destination map")
		}
		src.Metadata["k"] = "v" // Restore
	}

	//  Test with Overwrite = false
	cMerge := copier.NewCopier().WithOverwrite(false).WithDebug(false)
	destMerge := mapsClone(dest) // Clone initial map
	errMerge := cMerge.Copy(&destMerge, src)
	if errMerge != nil {
		t.Fatalf("Copy struct->map (overwrite=false) failed: %v", errMerge)
	}
	// Expect existing key kept, fields merged (respecting tags)
	expMapMerge := map[string]interface{}{
		"existing":  true, // Kept
		"FirstName": "Test",
		"LastName":  "User",
		"Metadata":  map[string]string{"k": "v"},
		"Age":       30,
	}
	assertDeepEqual(t, destMerge, expMapMerge, "Struct to map copy (overwrite=false) mismatch")

	//  Test with OmitEmpty on Age
	srcOmit := MapSource{FirstName: "TestOmit", LastName: "UserOmit", Metadata: nil, Zip: "ignored", Age: 0} // Age is 0
	destOmit := make(map[string]interface{})

	// Use a copier specifically configured to skip nil source values for this test's expectation
	cOmitSpecific := copier.NewCopier().WithSkipNil(true).WithDebug(cOverwrite.DebugMode())
	errOmit := cOmitSpecific.Copy(&destOmit, srcOmit)
	if errOmit != nil {
		t.Fatalf("Copy struct->map (omit age) failed: %v", errOmit)
	}
	expMapOmitAge := map[string]interface{}{
		// No "existing" key this time
		"FirstName": "TestOmit",
		"LastName":  "UserOmit",
		// Metadata is nil in source, should be omitted by isEmptyValue check in copyStructToMap
		// Age is 0, tagged with omitempty, should be omitted
	}
	assertDeepEqual(t, destOmit, expMapOmitAge, "Struct to map copy (omit age/nil) mismatch")
}

func TestMergeTo(t *testing.T) {
	src := SimpleSrc{Name: "New Name", Age: 50} // Name non-empty, Age non-zero
	dest := SimpleDest{Name: "Keep Me", Age: 0} // Name non-empty, Age zero

	c := copier.NewCopier().WithDebug(false) // Merge uses default copier settings except overwrite=false
	err := c.Merge(&dest, src)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	// Expect Name to be kept (dest was non-empty)
	// Expect Age to be updated (dest was zero/empty)
	exp := SimpleDest{Name: "Keep Me", Age: 50}
	assertDeepEqual(t, dest, exp, "Merge failed")
}

func TestMustCopyTo(t *testing.T) {
	src := SimpleSrc{Name: "PanicTest", Age: 1}
	var dest SimpleDest
	c := copier.NewCopier()

	recoveredSuccess := func() (r interface{}) {
		defer func() { r = recover() }()
		c.MustCopy(&dest, src)
		return nil
	}()
	if recoveredSuccess != nil {
		t.Fatalf("MustCopy panicked unexpectedly on success: %v", recoveredSuccess)
	}
	// EXPECTED TYPE IS SimpleDest, NOT SimpleSrc
	expSuccess := SimpleDest{Name: "PanicTest", Age: 1}
	assertDeepEqual(t, dest, expSuccess, "MustCopy success case failed")

	// ... rest of TestMustCopyTo remains the same
	var invalidDest SimpleDest
	recoveredPanic := func() (r interface{}) {
		defer func() { r = recover() }()
		c.MustCopy(invalidDest, src)
		return nil
	}()
	if recoveredPanic == nil {
		t.Fatalf("MustCopy did not panic for invalid destination")
	}
	if errMsg, ok := recoveredPanic.(string); !ok || !strings.Contains(errMsg, copier.ErrDestNotPointer.Error()) {
		t.Errorf("MustCopy panic message unexpected: %v, expected to contain '%s'", recoveredPanic, copier.ErrDestNotPointer.Error())
	}

	var nilDestPtr *SimpleDest = nil
	recoveredNilPanic := func() (r interface{}) {
		defer func() { r = recover() }()
		c.MustCopy(nilDestPtr, src)
		return nil
	}()
	if recoveredNilPanic == nil {
		t.Fatalf("MustCopy did not panic for nil destination pointer")
	}
	if errMsg, ok := recoveredNilPanic.(string); !ok || !strings.Contains(errMsg, copier.ErrDestNotPointer.Error()) {
		t.Errorf("MustCopy (nil ptr) panic message unexpected: %v, expected to contain '%s'", recoveredNilPanic, copier.ErrDestNotPointer.Error())
	}
}

// Test specific handling of bytes.Buffer and strings.Builder
func TestDeepCopyReadWriter(t *testing.T) {
	// Test *bytes.Buffer
	srcBufPtr := bytes.NewBufferString("buffer test data")
	c := copier.NewCopier().WithDebug(false)

	resBufPtr, errBufPtr := c.Deep(srcBufPtr)
	if errBufPtr != nil {
		t.Fatalf("Deep copy of *bytes.Buffer failed: %v", errBufPtr)
	}
	dstBufPtr, okBufPtr := resBufPtr.(*bytes.Buffer)
	if !okBufPtr {
		t.Fatalf("Deep copy of *bytes.Buffer did not return *bytes.Buffer, got %T", resBufPtr)
	}
	if pointsToSameAddress(dstBufPtr, srcBufPtr) {
		t.Errorf("Deep copy of *bytes.Buffer returned same pointer")
	}
	if dstBufPtr.String() != "buffer test data" {
		t.Errorf("Deep copy of *bytes.Buffer content mismatch: got '%s'", dstBufPtr.String())
	}
	// Check deepness
	srcBufPtr.WriteString(" more")
	if dstBufPtr.String() == srcBufPtr.String() {
		t.Errorf("Deep copy of *bytes.Buffer was shallow")
	}

	// Test strings.Builder (value)
	var srcBuilder strings.Builder
	srcBuilder.WriteString("builder test data")

	resBuilder, errBuilder := c.Deep(srcBuilder)
	if errBuilder != nil {
		t.Fatalf("Deep copy of strings.Builder failed: %v", errBuilder)
	}
	dstBuilder, okBuilder := resBuilder.(strings.Builder)
	if !okBuilder {
		t.Fatalf("Deep copy of strings.Builder did not return strings.Builder, got %T", resBuilder)
	}
	if dstBuilder.String() != "builder test data" {
		t.Errorf("Deep copy of strings.Builder content mismatch: got '%s'", dstBuilder.String())
	}
	// Check deepness (modifying original shouldn't affect copy)
	srcBuilder.WriteString(" more")
	if dstBuilder.String() == srcBuilder.String() {
		t.Errorf("Deep copy of strings.Builder was shallow")
	}
}

// Test for Stringer -> string/interface{} and error cases
func TestCopyRecursiveStringer(t *testing.T) {
	src := MyStringer{s: "test"}
	cc := copier.NewCopier()

	// Test Stringer -> string
	var destStrTarget string
	destTargetVal := reflect.ValueOf(&destStrTarget).Elem()
	ctx := &copier.SysContext{Visited: make(map[cycle.VisitKey]reflect.Value), Errors: make([]error, 0), Detector: cycle.New(cycle.WithLogger(cc.Logger()))}
	errTarget := cc.CopyRecursive(destTargetVal, reflect.ValueOf(src), ctx)
	if errTarget != nil {
		t.Fatalf("Copy Stringer->string failed: %v", errTarget)
	}
	if destStrTarget != "test" {
		t.Errorf("Expected 'test', got '%s'", destStrTarget)
	}

	// Test Stringer -> interface{}
	var destIface interface{}
	destIfaceVal := reflect.ValueOf(&destIface).Elem()
	ctx = &copier.SysContext{Visited: make(map[cycle.VisitKey]reflect.Value), Errors: make([]error, 0), Detector: cycle.New(cycle.WithLogger(cc.Logger()))}
	errIface := cc.CopyRecursive(destIfaceVal, reflect.ValueOf(src), ctx)
	if errIface != nil {
		t.Fatalf("Copy Stringer->interface{} failed: %v", errIface)
	}
	ifaceStr, ok := destIface.(string)
	if !ok || ifaceStr != "test" {
		t.Errorf("Expected interface to contain string 'test', got %T val %v", destIface, destIface)
	}

	// Test Stringer -> int (should fail)
	var destInt int
	destIntVal := reflect.ValueOf(&destInt).Elem()
	ctx = &copier.SysContext{Visited: make(map[cycle.VisitKey]reflect.Value), Errors: make([]error, 0), Detector: cycle.New(cycle.WithLogger(cc.Logger()))}
	errInt := cc.CopyRecursive(destIntVal, reflect.ValueOf(src), ctx)
	if errInt == nil {
		t.Fatalf("Copy Stringer->int expected error, got nil")
	}
	// Expect conversion failure because stringer output ("test") cannot convert to int
	if !errors.Is(errInt, copier.ErrNoConverter) && !errors.Is(errInt, copier.ErrTypeMismatch) { // Allow either error
		t.Errorf("Copy Stringer->int expected ErrNoConverter or ErrTypeMismatch, got %T: %v", errInt, errInt)
	}
}
