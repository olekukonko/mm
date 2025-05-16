// fieldx_test.go
package ex

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// --- Test Helper Structs (no changes needed) ---
type SimpleStruct struct {
	ID   int    `db:"id"`
	Name string `db:"name"`
	Age  int
	City string `db:"-"`
	Zip  string `json:"zip"`
}
type PointersStruct struct {
	CountPtr *int    `db:"count_ptr"`
	LabelPtr *string `db:"label_ptr"`
	Value    *float64
	NilPtr   *int `db:"nil_ptr"`
}
type SpecialTypesStruct struct {
	Timestamp time.Time           `db:"created_at"`
	Bytes     []byte              `db:"data"`
	Valuer    CustomValuer        `db:"valuer_field"`
	Marshaler CustomTextMarshaler `db:"marshaler_field"`
	NilTime   *time.Time          `db:"nil_time"`
	ZeroTime  time.Time           `db:"zero_time"`
}
type CustomValuer struct{ value string }

func (cv CustomValuer) Value() (driver.Value, error) {
	if cv.value == "error" {
		return nil, errors.New("valuer error requested")
	}
	return "valued_" + cv.value, nil
}

type CustomTextMarshaler struct{ Text string }

func (ctm CustomTextMarshaler) MarshalText() ([]byte, error) {
	if ctm.Text == "error" {
		return nil, errors.New("marshaler error requested")
	}
	return []byte("marshaled_" + ctm.Text), nil
}

type Base struct {
	BaseID    int    `db:"base_id"`
	BaseField string `db:"base_field"`
}
type EmbeddedStruct struct {
	Base
	TaggedBase Base   `db:"tagged_base"`
	OuterField string `db:"outer_field"`
	BaseField  string `db:"base_field"`
}
type OmitStruct struct {
	Name     string    `db:"name,omitempty"`
	Age      int       `db:"age,omitzero"`
	Count    *int      `db:"count,omitempty"`
	Value    float64   `db:"value,omitempty"`
	IsSet    bool      `db:"is_set,omitempty"`
	Items    []string  `db:"items,omitempty"`
	ZeroTime time.Time `db:"zero_time,omitempty"`
	ZeroInt  int       `db:"zero_int,omitempty,omitzero"`
}

// --- Test Helper Functions (no changes needed) ---
func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Expected no error, but got: %v", err)
	}
}
func assertError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Expected an error containing %q, but got nil", contains)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Errorf("Expected error message to contain %q, but got: %v", contains, err)
	}
}
func assertDeepEqual(t *testing.T, expected, actual any) {
	t.Helper()
	if !reflect.DeepEqual(expected, actual) {
		t.Errorf("DeepEqual failed:\nExpected:\n%#v\nActual:\n%#v", expected, actual)
	}
}
func assertFieldInfo(t *testing.T, infos []Field, name string, exists bool, omitted *bool) {
	t.Helper()
	found := false
	for _, info := range infos {
		if info.Name == name {
			found = true
			if omitted != nil && info.Omitted != *omitted {
				t.Errorf("Field for %q: expected Omitted=%v, got Omitted=%v", name, *omitted, info.Omitted)
			}
			break
		}
	}
	if exists != found {
		t.Errorf("Field for %q: expected Exists=%v, got Exists=%v", name, exists, found)
	}
}
func boolPtr(b bool) *bool { return &b }
func pint(i int) *int      { p := new(int); *p = i; return p }

// --- Tests (UPDATED EXPECTATIONS & OPTIONS) ---

func TestNewExtractorDefaults(t *testing.T) {
	opts := Options{}
	extractor := New[any](opts) // Apply defaults in New
	if extractor.opts.Tag != "db" {
		t.Errorf("Expected Tag 'db', got %q", extractor.opts.Tag)
	}
	if extractor.opts.UseFieldName == nil || !*extractor.opts.UseFieldName {
		t.Errorf("Expected default UseFieldName true")
	}
}

func TestExtractSimpleStruct_Any(t *testing.T) {
	input := SimpleStruct{ID: 101, Name: "Test Name", Age: 30, City: "SkipMe", Zip: "90210"}
	// EXPECTED: Age included (default UseFieldName=true), Zip excluded (json only, no fallback), City excluded (-)
	expectedMap := map[string]any{"id": 101, "name": "Test Name", "Age": 30}
	opts := Options{} // Use defaults
	extractor := New[any](opts)
	resultMap, infos, err := extractor.Extract(&input)
	assertNoError(t, err)
	assertDeepEqual(t, expectedMap, resultMap)
	assertFieldInfo(t, infos, "Age", true, boolPtr(false))
	assertFieldInfo(t, infos, "zip", false, nil) // Zip has json tag only, no db tag, no fallback -> skipped
}

func TestExtractSimpleStruct_String(t *testing.T) {
	input := SimpleStruct{ID: 101, Name: "Test Name", Age: 30}
	opts := Options{}
	extractor := New[string](opts)
	_, _, err := extractor.Extract(input)
	assertError(t, err, "cannot convert") // Check substring
}

func TestExtractWithOptions_IncludeExclude(t *testing.T) {
	input := SimpleStruct{ID: 101, Name: "Test Name", Age: 30}
	expectedMap := map[string]any{"name": "Test Name"} // Only name included
	opts := Options{IncludeOnly: []string{"id", "name"}, ExcludeFields: []string{"id"}}
	extractor := New[any](opts)
	resultMap, _, err := extractor.Extract(input)
	assertNoError(t, err)
	assertDeepEqual(t, expectedMap, resultMap)
}

func TestExtractWithOptions_CustomTagAndFallback(t *testing.T) {
	input := SimpleStruct{ID: 101, Name: "Test Name", Age: 30, Zip: "90210"}
	// EXPECTED: Only zip included (json fallback), others ignored (no custom/json tag)
	expectedMap := map[string]any{"zip": "90210"}
	// *** IMPORTANT: Disable UseFieldName for this test ***
	useFieldNameFalse := false
	opts := Options{Tag: "custom", UseJSONFallback: true, UseFieldName: &useFieldNameFalse}
	extractor := New[any](opts)
	resultMap, infos, err := extractor.Extract(input)
	assertNoError(t, err)
	assertDeepEqual(t, expectedMap, resultMap)
	assertFieldInfo(t, infos, "zip", true, boolPtr(false))
	assertFieldInfo(t, infos, "id", false, nil)
	assertFieldInfo(t, infos, "Age", false, nil)
}

func TestExtractWithOptions_NoUseFieldName(t *testing.T) {
	input := SimpleStruct{ID: 101, Name: "Test Name", Age: 30}
	// EXPECTED: Age excluded because UseFieldName is false
	expectedMap := map[string]any{"id": 101, "name": "Test Name"}
	useFieldNameFalse := false
	opts := Options{UseFieldName: &useFieldNameFalse}
	extractor := New[any](opts)
	resultMap, infos, err := extractor.Extract(input)
	assertNoError(t, err)
	assertDeepEqual(t, expectedMap, resultMap)
	assertFieldInfo(t, infos, "Age", false, nil) // Should not exist
}

func TestExtractPointers(t *testing.T) {
	count := 5
	label := "hello"
	var value float64 = 1.23
	input := PointersStruct{CountPtr: &count, LabelPtr: &label, Value: &value, NilPtr: nil}
	// EXPECTED: Value included (default UseFieldName=true), NilPtr omitted (default omission)
	expectedMap := map[string]any{"count_ptr": 5, "label_ptr": "hello", "Value": 1.23}
	opts := Options{} // Defaults
	extractor := New[any](opts)
	resultMap, infos, err := extractor.Extract(&input)
	assertNoError(t, err)
	assertDeepEqual(t, expectedMap, resultMap)
	assertFieldInfo(t, infos, "Value", true, boolPtr(false))
	assertFieldInfo(t, infos, "nil_ptr", true, boolPtr(true)) // Omitted

	// Test with SkipNil = true
	optsSkip := Options{SkipNil: true}
	extractorSkip := New[any](optsSkip)
	resultMapSkipNil, infosSkipNil, errSkipNil := extractorSkip.Extract(&input)
	assertNoError(t, errSkipNil)
	assertDeepEqual(t, expectedMap, resultMapSkipNil)
	assertFieldInfo(t, infosSkipNil, "nil_ptr", true, boolPtr(true)) // Omitted by SkipNil

	// Test with AllowZeroValues = true
	optsInclude := Options{AllowZeroValues: true}
	extractorInclude := New[any](optsInclude)
	resultMapIncludeZero, infosIncludeZero, errIncludeZero := extractorInclude.Extract(&input)
	expectedMapIncludeZero := map[string]any{"count_ptr": 5, "label_ptr": "hello", "Value": 1.23, "nil_ptr": nil}
	assertNoError(t, errIncludeZero)
	assertDeepEqual(t, expectedMapIncludeZero, resultMapIncludeZero)
	assertFieldInfo(t, infosIncludeZero, "nil_ptr", true, boolPtr(false)) // Not omitted
}

func TestExtractSpecialTypes(t *testing.T) {
	ts := time.Date(2023, 10, 27, 10, 30, 0, 0, time.UTC)
	nilTimePtr := (*time.Time)(nil)
	input := SpecialTypesStruct{Timestamp: ts, Bytes: []byte("hello"), Valuer: CustomValuer{value: "test1"}, Marshaler: CustomTextMarshaler{Text: "test2"}, NilTime: nilTimePtr, ZeroTime: time.Time{}}
	// EXPECTED: NilTime and ZeroTime omitted by default
	expectedMap := map[string]any{"created_at": ts, "data": []byte("hello"), "valuer_field": "valued_test1", "marshaler_field": "marshaled_test2"}
	opts := Options{}
	extractor := New[any](opts)
	resultMap, infos, err := extractor.Extract(&input)
	assertNoError(t, err)
	assertDeepEqual(t, expectedMap, resultMap)
	assertFieldInfo(t, infos, "nil_time", true, boolPtr(true))
	assertFieldInfo(t, infos, "zero_time", true, boolPtr(true))

	// Test with AllowZeroValues = true
	optsInclude := Options{AllowZeroValues: true}
	extractorInclude := New[any](optsInclude)
	resultMapIncludeZero, infosIncludeZero, errIncludeZero := extractorInclude.Extract(&input)
	expectedMapIncludeZero := map[string]any{"created_at": ts, "data": []byte("hello"), "valuer_field": "valued_test1", "marshaler_field": "marshaled_test2", "nil_time": nil, "zero_time": time.Time{}}
	assertNoError(t, errIncludeZero)
	assertDeepEqual(t, expectedMapIncludeZero, resultMapIncludeZero)
	assertFieldInfo(t, infosIncludeZero, "nil_time", true, boolPtr(false))
	assertFieldInfo(t, infosIncludeZero, "zero_time", true, boolPtr(false))
}

func TestExtractValuerMarshalerErrors(t *testing.T) {
	input1 := SpecialTypesStruct{Valuer: CustomValuer{value: "error"}}
	extractor := New[any](Options{})
	_, _, err1 := extractor.Extract(&input1)
	assertError(t, err1, "valuer_field")
	assertError(t, err1, "valuer error requested")

	input2 := SpecialTypesStruct{Marshaler: CustomTextMarshaler{Text: "error"}}
	_, _, err2 := extractor.Extract(&input2)
	assertError(t, err2, "marshaler_field")
	assertError(t, err2, "marshaler error requested")
}

func TestExtractEmbeddedStruct(t *testing.T) {
	input := EmbeddedStruct{Base: Base{BaseID: 1, BaseField: "base value"}, TaggedBase: Base{BaseID: 2, BaseField: "tagged base value"}, OuterField: "outer value", BaseField: "override value"}
	taggedBaseExpected := map[string]any{"base_id": 2, "base_field": "tagged base value"}
	// EXPECTED: base_id merged, base_field overridden
	expectedMap := map[string]any{"base_id": 1, "tagged_base": taggedBaseExpected, "outer_field": "outer value", "base_field": "override value"}
	opts := Options{}
	extractor := New[any](opts)
	resultMap, infos, err := extractor.Extract(&input)
	assertNoError(t, err)
	assertDeepEqual(t, expectedMap, resultMap)
	// Check override value
	foundNonOmittedBaseField := false
	for _, info := range infos {
		if info.Name == "base_field" && !info.Omitted {
			assertDeepEqual(t, "override value", info.Value)
			foundNonOmittedBaseField = true
		}
	}
	if !foundNonOmittedBaseField {
		t.Error("Expected non-omitted 'base_field' w/ override value")
	}
	// Check merged value
	assertFieldInfo(t, infos, "base_id", true, boolPtr(false))
}

func TestExtractOmitOptions(t *testing.T) {
	omitInput := OmitStruct{Name: "", Age: 0, Count: pint(0), Value: 0.0, IsSet: false, Items: []string{}, ZeroTime: time.Time{}, ZeroInt: 0}
	filledInput := OmitStruct{Name: "Test", Age: 30, Count: pint(5), Value: 1.23, IsSet: true, Items: []string{"a"}, ZeroTime: time.Now(), ZeroInt: 1}
	opts := Options{} // Defaults
	extractor := New[any](opts)

	// Test omitted by tags
	resultMapOmit, infosOmit, errOmit := extractor.Extract(omitInput)
	assertNoError(t, errOmit)
	assertDeepEqual(t, map[string]any{}, resultMapOmit)
	assertFieldInfo(t, infosOmit, "name", true, boolPtr(true))
	assertFieldInfo(t, infosOmit, "age", true, boolPtr(true))
	assertFieldInfo(t, infosOmit, "count", true, boolPtr(true))
	assertFieldInfo(t, infosOmit, "value", true, boolPtr(true))
	assertFieldInfo(t, infosOmit, "is_set", true, boolPtr(true))
	assertFieldInfo(t, infosOmit, "items", true, boolPtr(true))
	assertFieldInfo(t, infosOmit, "zero_time", true, boolPtr(true))
	assertFieldInfo(t, infosOmit, "zero_int", true, boolPtr(true))

	// Test included
	expectedMapFilled := map[string]any{"name": "Test", "age": 30, "count": 5, "value": 1.23, "is_set": true, "items": []any{"a"}, "zero_time": filledInput.ZeroTime, "zero_int": 1}
	resultMapFilled, _, errFilled := extractor.Extract(filledInput)
	assertNoError(t, errFilled)
	filledTime, ftOK := resultMapFilled["zero_time"].(time.Time)
	expectedTime, etOK := expectedMapFilled["zero_time"].(time.Time)
	if !(ftOK && etOK && filledTime.Equal(expectedTime)) {
		t.Errorf("Time mismatch: Exp %v, Got %v", expectedTime, filledTime)
	}
	delete(resultMapFilled, "zero_time")
	delete(expectedMapFilled, "zero_time")
	assertDeepEqual(t, expectedMapFilled, resultMapFilled)
}

func TestExtractFromMap(t *testing.T) {
	count := 5
	inputMap := map[string]any{"id": 123, "name": "Map Test", "enabled": true, "value_ptr": &count, "nested": map[string]any{"key": "nested_value"}, "tags": []string{"a", "b"}, "nil_val": nil}
	// EXPECTED: nil_val omitted by default
	expectedMap := map[string]any{"id": 123, "name": "Map Test", "enabled": true, "value_ptr": 5, "nested": map[string]any{"key": "nested_value"}, "tags": []any{"a", "b"}}
	opts := Options{}
	extractor := New[any](opts)
	resultMap, infos, err := extractor.Extract(inputMap)
	assertNoError(t, err)
	assertDeepEqual(t, expectedMap, resultMap)
	assertFieldInfo(t, infos, "nil_val", true, boolPtr(true)) // omitted

	// Test map with SkipNil = true
	optsSkip := Options{SkipNil: true}
	extractorSkip := New[any](optsSkip)
	resultMapSkipNil, infosSkipNil, errSkipNil := extractorSkip.Extract(inputMap)
	assertNoError(t, errSkipNil)
	assertDeepEqual(t, expectedMap, resultMapSkipNil)
	assertFieldInfo(t, infosSkipNil, "nil_val", true, boolPtr(true)) // omitted

	// Test map with AllowZeroValues = true
	optsInclude := Options{AllowZeroValues: true}
	extractorInclude := New[any](optsInclude)
	resultMapIncludeZero, infosIncludeZero, errIncludeZero := extractorInclude.Extract(inputMap)
	expectedMapIncludeZero := map[string]any{"id": 123, "name": "Map Test", "enabled": true, "value_ptr": 5, "nested": map[string]any{"key": "nested_value"}, "tags": []any{"a", "b"}, "nil_val": nil}
	assertNoError(t, errIncludeZero)
	assertDeepEqual(t, expectedMapIncludeZero, resultMapIncludeZero)
	assertFieldInfo(t, infosIncludeZero, "nil_val", true, boolPtr(false)) // not omitted
}

func TestStrictMode(t *testing.T) {
	// Struct test - field missing from struct definition
	inputStruct := SimpleStruct{ID: 1, Name: "A"}
	optsStruct := Options{IncludeOnly: []string{"id", "name", "nonexistent"}, StrictMode: true}
	_, _, errStruct := New[any](optsStruct).Extract(inputStruct)
	// Check specific error message structure from diff
	assertError(t, errStruct, "strict mode: fields specified in IncludeOnly not found")
	assertError(t, errStruct, "nonexistent")

	// Map test - key missing from map
	inputMap := map[string]any{"id": 1, "name": "A"}
	optsMap := Options{IncludeOnly: []string{"id", "name", "nonexistent"}, StrictMode: true}
	_, _, errMap := New[any](optsMap).Extract(inputMap)
	assertError(t, errMap, "strict mode: required map keys missing/excluded") // Check specific map wording
	assertError(t, errMap, "nonexistent")

	// Test StrictMode with ExcludeFields taking precedence
	optsExclude := Options{IncludeOnly: []string{"id", "name"}, ExcludeFields: []string{"name"}, StrictMode: true}
	_, _, errExclude := New[any](optsExclude).Extract(inputStruct)
	// Use the consolidated error message wording
	assertError(t, errExclude, "strict mode: required fields missing, excluded, or omitted")
	assertError(t, errExclude, "name")

	// Test StrictMode with field omitted due to value
	optsOmit := Options{IncludeOnly: []string{"count_ptr", "nil_ptr"}, StrictMode: true}
	inputOmit := PointersStruct{CountPtr: pint(5), NilPtr: nil}
	_, _, errOmit := New[any](optsOmit).Extract(inputOmit)
	// Use the consolidated error message wording
	assertError(t, errOmit, "strict mode: required fields missing, excluded, or omitted")
	assertError(t, errOmit, "nil_ptr")
}

func TestErrors(t *testing.T) {
	extractor := New[any](Options{}) // Defaults
	_, _, err := extractor.Extract(nil)
	assertError(t, err, "nil input") // Nil input
	var nilStruct *SimpleStruct = nil
	resNilPtr, infoNilPtr, errNilPtr := extractor.Extract(nilStruct)
	assertNoError(t, errNilPtr)
	assertDeepEqual(t, map[string]any{}, resNilPtr)
	assertDeepEqual(t, []Field{}, infoNilPtr) // Nil pointer input
	_, _, err = extractor.Extract(123)
	assertError(t, err, "unsupported input type") // Unsupported type
	_, _, err = extractor.Extract(map[int]string{1: "one"})
	assertError(t, err, "map keys must be strings") // Map with non-string keys

	// Conversion error for specific T
	stringExtractor := New[string](Options{})
	inputStruct := SimpleStruct{ID: 123, Name: "Test"}
	_, _, err = stringExtractor.Extract(inputStruct)
	assertError(t, err, "cannot convert") // Check substring
}

//func TestHelperFunctions(t *testing.T) {
//	input := SimpleStruct{ID: 1, Name: "Helper", Age: 40}
//	// ExtractToMap (EXPECTED: Age included)
//	expectedMapAny := map[string]any{"id": 1, "name": "Helper", "Age": 40}
//	resMapAny, _, errAny := mm.ExtractToMap(&input, Options{})
//	assertNoError(t, errAny)
//	assertDeepEqual(t, expectedMapAny, resMapAny)
//	// ExtractToStringMap (expect error)
//	_, _, errStr := mm.ExtractToStringMap(&input, Options{})
//	assertError(t, errStr, "cannot convert")
//	// ExtractWithNames
//	expectedMapNames := map[string]any{"name": "Helper"}
//	resMapNames, _, errNames := mm.ExtractWithNames(&input, []string{"name"}, Options{})
//	assertNoError(t, errNames)
//	assertDeepEqual(t, expectedMapNames, resMapNames)
//	// ExtractWithNames (missing field, strict mode)
//	_, _, errNamesMissing := mm.ExtractWithNames(&input, []string{"name", "nonexistent"}, Options{})
//	assertError(t, errNamesMissing, "strict mode") // Top-level helper enables strict mode
//	assertError(t, errNamesMissing, "nonexistent") // Check specific error substring from struct logic
//}

func TestCacheConsistency(t *testing.T) {
	// Simple test to ensure caching doesn't break basic extraction
	input1 := SimpleStruct{ID: 1, Name: "First", Age: 10}
	input2 := SimpleStruct{ID: 2, Name: "Second", Age: 20}
	opts := Options{}
	extractor := New[any](opts)
	res1, _, err1 := extractor.Extract(input1)
	assertNoError(t, err1)
	res2, _, err2 := extractor.Extract(input2)
	assertNoError(t, err2) // Should use cache for metadata
	expected1 := map[string]any{"id": 1, "name": "First", "Age": 10}
	expected2 := map[string]any{"id": 2, "name": "Second", "Age": 20}
	assertDeepEqual(t, expected1, res1)
	assertDeepEqual(t, expected2, res2)

	// Test cache key varies with UseJSONFallback
	optsFallback := Options{UseJSONFallback: true}
	extractorFallback := New[any](optsFallback)
	input3WithZip := SimpleStruct{ID: 3, Name: "Third", Zip: "123", Age: 30}
	resFallbackZip, _, errFallbackZip := extractorFallback.Extract(input3WithZip)
	assertNoError(t, errFallbackZip)
	expectedFallbackZip := map[string]any{"id": 3, "name": "Third", "Age": 30, "zip": "123"}
	assertDeepEqual(t, expectedFallbackZip, resFallbackZip)

	// Test cache key varies with UseFieldName
	useFieldNameFalse := false
	optsNoFieldName := Options{UseFieldName: &useFieldNameFalse}
	extractorNoFieldName := New[any](optsNoFieldName)
	input4 := SimpleStruct{ID: 4, Name: "Fourth", Age: 40}
	resNoFieldName, _, errNoFieldName := extractorNoFieldName.Extract(input4)
	assertNoError(t, errNoFieldName)
	expectedNoFieldName := map[string]any{"id": 4, "name": "Fourth"} // Age NOT included
	assertDeepEqual(t, expectedNoFieldName, resNoFieldName)
}

func TestExtractWithCustomSorter(t *testing.T) {
	input := SimpleStruct{ID: 101, Name: "Test", Age: 30}

	t.Run("NoSorter", func(t *testing.T) {
		opts := Options{}
		extractor := New[any](opts)
		resultMap, infos, err := extractor.Extract(input)
		assertNoError(t, err)
		expectedMap := map[string]any{"id": 101, "name": "Test", "Age": 30}
		assertDeepEqual(t, expectedMap, resultMap)
		expectedOrder := []string{"id", "name", "Age"}
		actualOrder := make([]string, len(infos))
		for i, info := range infos {
			if !info.Omitted {
				actualOrder[i] = info.Name
			}
		}
		assertDeepEqual(t, expectedOrder, actualOrder)
	})

	t.Run("Alphabetical", func(t *testing.T) {
		opts := Options{
			FieldSorter: func(infos []Field) []Field {
				sorted := make([]Field, len(infos))
				copy(sorted, infos)
				sort.Slice(sorted, func(i, j int) bool {
					return sorted[i].Name < sorted[j].Name
				})
				return sorted
			},
		}
		extractor := New[any](opts)
		resultMap, infos, err := extractor.Extract(input)
		assertNoError(t, err)
		expectedMap := map[string]any{"id": 101, "name": "Test", "Age": 30}
		assertDeepEqual(t, expectedMap, resultMap)
		expectedOrder := []string{"Age", "id", "name"}
		actualOrder := make([]string, len(infos))
		for i, info := range infos {
			if !info.Omitted {
				actualOrder[i] = info.Name
			}
		}
		assertDeepEqual(t, expectedOrder, actualOrder)
	})

	t.Run("ReverseAlphabetical", func(t *testing.T) {
		opts := Options{
			FieldSorter: func(infos []Field) []Field {
				sorted := make([]Field, len(infos))
				copy(sorted, infos)
				sort.Slice(sorted, func(i, j int) bool {
					return sorted[i].Name > sorted[j].Name
				})
				return sorted
			},
		}
		extractor := New[any](opts)
		resultMap, infos, err := extractor.Extract(input)
		assertNoError(t, err)
		expectedMap := map[string]any{"id": 101, "name": "Test", "Age": 30}
		assertDeepEqual(t, expectedMap, resultMap)
		expectedOrder := []string{"name", "id", "Age"}
		actualOrder := make([]string, len(infos))
		for i, info := range infos {
			if !info.Omitted {
				actualOrder[i] = info.Name
			}
		}
		assertDeepEqual(t, expectedOrder, actualOrder)
	})

	t.Run("RankerLike", func(t *testing.T) {
		opts := Options{
			FieldSorter: func(infos []Field) []Field {
				sorted := make([]Field, len(infos))
				copy(sorted, infos)
				// Mock DisableRanker: prioritize "id", then "Age", then "name"
				priorities := map[string]int{
					"id":   1, // Like RankPrimaryKey
					"Age":  2, // Like RankSmallInt
					"name": 3, // Like RankStrings
				}
				sort.Slice(sorted, func(i, j int) bool {
					return priorities[sorted[i].Name] < priorities[sorted[j].Name]
				})
				return sorted
			},
		}
		extractor := New[any](opts)
		resultMap, infos, err := extractor.Extract(input)
		assertNoError(t, err)
		expectedMap := map[string]any{"id": 101, "name": "Test", "Age": 30}
		assertDeepEqual(t, expectedMap, resultMap)
		expectedOrder := []string{"id", "Age", "name"}
		actualOrder := make([]string, len(infos))
		for i, info := range infos {
			if !info.Omitted {
				actualOrder[i] = info.Name
			}
		}
		assertDeepEqual(t, expectedOrder, actualOrder)
	})
}

func TestShouldOmit(t *testing.T) {
	type TestStruct struct {
		ID        int             `db:"id"`
		IsActive  bool            `db:"is_active"`
		NullInfo  sql.NullString  `db:"null_info"`
		NullPrice sql.NullFloat64 `db:"null_price"`
	}

	tests := []struct {
		name            string
		input           TestStruct
		opts            Options
		expectedOmitted map[string]bool
	}{
		{
			name: "AllowZeroValues_AllowSQLNull_SkipNilFalse",
			input: TestStruct{
				ID:        1,
				IsActive:  false,
				NullInfo:  sql.NullString{String: "", Valid: false},
				NullPrice: sql.NullFloat64{Float64: 0, Valid: false},
			},
			opts: Options{
				Tag:             "db",
				SkipNil:         false,
				AllowZeroValues: true,
				AllowSQLNull:    true,
			},
			expectedOmitted: map[string]bool{
				"id":         false,
				"is_active":  false,
				"null_info":  false,
				"null_price": false,
			},
		},
		{
			name: "SkipNilTrue_AllowZeroValuesFalse",
			input: TestStruct{
				ID:        1,
				IsActive:  false,
				NullInfo:  sql.NullString{String: "", Valid: false},
				NullPrice: sql.NullFloat64{Float64: 0, Valid: false},
			},
			opts: Options{
				Tag:             "db",
				SkipNil:         true,
				AllowZeroValues: false,
				AllowSQLNull:    false,
			},
			expectedOmitted: map[string]bool{
				"id":         false,
				"is_active":  true,
				"null_info":  true,
				"null_price": true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := New[any](tt.opts)
			rv := reflect.ValueOf(tt.input)
			rt := rv.Type()
			currentLevelFields, err := extractor.parseStruct(rt)
			if err != nil {
				t.Fatalf("parseStruct: %v", err)
			}
			for _, f := range currentLevelFields {
				fieldValue := rv.FieldByIndex(f.index)
				processedValue, err := extractor.processValue(fieldValue)
				if err != nil {
					t.Fatalf("processValue for %s: %v", f.name, err)
				}
				omitted, err := extractor.shouldOmit(processedValue, fieldValue, f)
				if err != nil {
					t.Fatalf("shouldOmit for %s: %v", f.name, err)
				}
				if omitted != tt.expectedOmitted[f.name] {
					t.Errorf("shouldOmit for %s: expected omitted=%v, got=%v", f.name, tt.expectedOmitted[f.name], omitted)
				}
			}
		})
	}
}
