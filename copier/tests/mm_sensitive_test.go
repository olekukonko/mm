// mm_test.go
package tests

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/olekukonko/mm/copier"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// Helper function to clone initial destination states reliably
func cloneInitialDestState(t *testing.T, initialDestPtr interface{}) interface{} {
	t.Helper()
	if initialDestPtr == nil {
		return nil
	}
	val := reflect.ValueOf(initialDestPtr)
	if val.Kind() != reflect.Ptr {
		t.Fatalf("cloneInitialDestState: initialDestPtr must be a pointer, got %T", initialDestPtr)
	}
	if val.IsNil() {
		// Return a nil pointer of the correct type
		return reflect.Zero(val.Type()).Interface()
	}

	srcElem := val.Elem() // This is the actual slice/array/struct value

	// Create a new destination pointer of the same type as initialDestPtr
	clonedDestPtrValue := reflect.New(srcElem.Type()) // e.g., *[]Hello or *DestStruct
	clonedDestElem := clonedDestPtrValue.Elem()       // e.g., []Hello or DestStruct (zeroed initially)

	// For cloning the initial state, we want a "raw" copy that does not
	// invoke the custom Copiable logic of types like `tests.Hello` if the
	// test itself intends to test scenarios like `WithSkipSensitive(true)`.
	// We will perform a direct element/field copy where possible.

	switch srcElem.Kind() {
	case reflect.Slice:
		if srcElem.Type() == clonedDestElem.Type() {
			clonedDestElem.Set(reflect.MakeSlice(srcElem.Type(), srcElem.Len(), srcElem.Cap()))
			for i := 0; i < srcElem.Len(); i++ {
				// Directly set, assuming elements are basic enough or we want their raw form.
				// For tests.Hello (string), this copies the string value directly.
				clonedDestElem.Index(i).Set(srcElem.Index(i))
			}
			return clonedDestPtrValue.Interface()
		}
	case reflect.Array:
		if srcElem.Type() == clonedDestElem.Type() {
			// Arrays are value types, clonedDestElem is already appropriately sized.
			for i := 0; i < srcElem.Len(); i++ {
				clonedDestElem.Index(i).Set(srcElem.Index(i))
			}
			return clonedDestPtrValue.Interface()
		}
	case reflect.Map:
		if srcElem.Type() == clonedDestElem.Type() {
			if srcElem.IsNil() {
				clonedDestElem.Set(reflect.Zero(srcElem.Type()))
			} else {
				clonedDestElem.Set(reflect.MakeMapWithSize(srcElem.Type(), srcElem.Len()))
				iter := srcElem.MapRange()
				for iter.Next() {
					// Shallow copy of map keys/values for cloning initial state.
					// If map values are complex and need deep cloning for setup, this might need enhancement
					// or tests should ensure initial map states are simple.
					clonedDestElem.SetMapIndex(iter.Key(), iter.Value())
				}
			}
			return clonedDestPtrValue.Interface()
		}
	case reflect.Struct:
		// For structs, use a basic mm.Copier that should not trigger specific sensitive transforms
		// if those transforms rely on options not set in this basic cloner.
		// A default copier for cloning should be mostly raw.
		// The crucial part is that the TEST'S copier (c in the test case) has the options like WithSkipSensitive.
		cloner := copier.New().WithDebug(false).WithOverwrite(true).WithCycleDetection(true)
		// We copy from the original pointed-to value into our new cloned value's address.
		err := cloner.Copy(clonedDestPtrValue.Interface(), initialDestPtr)
		if err != nil {
			t.Logf("Warning/Error during struct cloneInitialDestState for type %T: %v", initialDestPtr, err)
		}
		return clonedDestPtrValue.Interface()
	}

	// Fallback for types not handled above, or if types didn't match exactly.
	// This uses a default copier. If initialDestPtr contains types with globally active
	// Copiable methods (like `tests.Hello`), those *will* run here.
	// This might be okay if the `dstInitial` in tests is already expected to be in its "transformed" state.
	// However, for `Slice/Array - Skip sensitive`, `dstInitial` has raw strings.
	t.Logf("cloneInitialDestState: फॉलबैक का उपयोग कर रहा है mm.Copy प्रकार %T के लिए", initialDestPtr)
	fallbackCloner := copier.New().WithDebug(false).WithOverwrite(true).WithCycleDetection(true)
	err := fallbackCloner.Copy(clonedDestPtrValue.Interface(), initialDestPtr)
	if err != nil {
		t.Logf("Warning/Error during fallback cloneInitialDestState for type %T: %v", initialDestPtr, err)
	}
	return clonedDestPtrValue.Interface()
}

// Helper for TestSliceMapConversions
func sortHelloSlice(s []Hello) {
	sort.Slice(s, func(i, j int) bool {
		return string(s[i]) < string(s[j])
	})
}

// Helper function to create pointers for testing
func ptr[T any](v T) *T {
	return &v
}

// Hello is a sensitive type for testing.
type Hello string

func (h Hello) Sensitive() bool {
	return true
}

func (h Hello) Copy(ctx *copier.CopyContext, src interface{}) (interface{}, error) {
	var stringToEncode string
	switch v := src.(type) {
	case string:
		stringToEncode = v
	case Hello:
		stringToEncode = string(v)
	case *Hello:
		if v != nil {
			stringToEncode = string(*v)
		} else {
			ctx.Overwrite = true
			return nil, nil
		}
	default:
		return nil, fmt.Errorf("invalid source type for Hello.Copy: expected string, Hello, or *Hello, got %T", src)
	}
	ctx.Overwrite = true
	encoded := base64.StdEncoding.EncodeToString([]byte(stringToEncode))
	return Hello(encoded), nil
}

func TestStructSensitive(t *testing.T) {
	type Source struct {
		Normal       string
		SensitiveStr string `mm:"sensitive_str,sensitive"`
		HelloField   Hello  `mm:"hello_field,sensitive"`
	}
	type Dest struct {
		Normal       string
		SensitiveStr string `mm:"sensitive_str,sensitive"`
		HelloField   Hello  `mm:"hello_field,sensitive"`
	}

	tests := []struct {
		name             string
		src              interface{}
		dstInitial       interface{}
		opts             []copier.Option
		expectDst        interface{}
		expectErrContent string
		skipDstCheck     bool
	}{
		{
			name:             "S2S: Default - Error on sensitive string without Copiable",
			src:              Source{Normal: "normal", SensitiveStr: "secret", HelloField: "hello"},
			dstInitial:       &Dest{Normal: "initial", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			opts:             []copier.Option{},
			expectErrContent: "sensitive item 'sensitive_str' (src type string) requires Copiable",
			skipDstCheck:     true,
		},
		{
			name:             "S2S: SkipSensitive - Skips all sensitive",
			src:              Source{Normal: "normal", SensitiveStr: "secret", HelloField: "hello"},
			dstInitial:       &Dest{Normal: "initial_val_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			opts:             []copier.Option{copier.WithSkipSensitive(true)},
			expectDst:        &Dest{Normal: "normal", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			expectErrContent: "",
			skipDstCheck:     false,
		},
		{
			name:       "S2S: CollectErrors - Copies non-sensitive, collects error for string, copies Hello",
			src:        Source{Normal: "normal", SensitiveStr: "secret", HelloField: "hello"},
			dstInitial: &Dest{Normal: "initial_val_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			opts:       []copier.Option{copier.WithErrors(true)},
			expectDst: &Dest{
				Normal:       "normal",
				SensitiveStr: "initial_secret",
				HelloField:   Hello(base64.StdEncoding.EncodeToString([]byte("hello"))),
			},
			expectErrContent: "sensitive item 'sensitive_str' (src type string) requires Copiable",
			skipDstCheck:     false,
		},
		{
			name:             "S2S: Merge - Skips non-empty dest fields before sensitivity",
			src:              Source{Normal: "normal_src", SensitiveStr: "secret_src", HelloField: "hello_src"},
			dstInitial:       &Dest{Normal: "initial_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			opts:             []copier.Option{copier.WithMerge()},
			expectDst:        &Dest{Normal: "initial_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			expectErrContent: "",
			skipDstCheck:     false,
		},
		{
			name:             "S2Map: Default - Error on sensitive string",
			src:              Source{Normal: "normal", SensitiveStr: "secret", HelloField: "hello"},
			dstInitial:       func() *map[string]interface{} { m := make(map[string]interface{}); return &m }(),
			opts:             []copier.Option{},
			expectErrContent: "sensitive item 'mapValueForKey 'sensitive_str'",
			skipDstCheck:     true,
		},
		{
			name: "S2Map: SkipSensitive",
			src:  Source{Normal: "normal", SensitiveStr: "secret", HelloField: "hello"},
			dstInitial: func() *map[string]interface{} {
				m := map[string]interface{}{"Normal": "old_normal", "sensitive_str": "keep_map_secret", "hello_field": "keep_map_hello"}
				return &m
			}(),
			opts:             []copier.Option{copier.WithSkipSensitive(true)},
			expectDst:        &map[string]interface{}{"Normal": "normal", "sensitive_str": "keep_map_secret", "hello_field": "keep_map_hello"},
			expectErrContent: "",
			skipDstCheck:     false,
		},
		{
			name: "S2Map: CollectErrors - Copies Normal, copies Hello, collects error for string",
			src:  Source{Normal: "normal", SensitiveStr: "secret", HelloField: "hello"},
			dstInitial: func() *map[string]interface{} {
				m := map[string]interface{}{"Normal": "old_normal", "sensitive_str": "keep_map_secret"}
				return &m
			}(),
			opts: []copier.Option{copier.WithErrors(true)},
			expectDst: &map[string]interface{}{
				"Normal":        "normal",
				"hello_field":   Hello(base64.StdEncoding.EncodeToString([]byte("hello"))),
				"sensitive_str": "keep_map_secret",
			},
			expectErrContent: "sensitive item 'mapValueForKey 'sensitive_str'",
			skipDstCheck:     false,
		},
		{
			name:             "Map2S: Default - Error on sensitive string",
			src:              map[string]interface{}{"Normal": "normal", "sensitive_str": "secret", "hello_field": "hello"},
			dstInitial:       &Dest{Normal: "initial_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			opts:             []copier.Option{},
			expectErrContent: "sensitive item 'field 'sensitive_str' from mapKey 'sensitive_str''",
			skipDstCheck:     true,
		},
		{
			name:             "Map2S: SkipSensitive",
			src:              map[string]interface{}{"Normal": "normal", "sensitive_str": "secret_map_val", "hello_field": "hello_map_val"},
			dstInitial:       &Dest{Normal: "initial_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			opts:             []copier.Option{copier.WithSkipSensitive(true)},
			expectDst:        &Dest{Normal: "normal", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			expectErrContent: "",
			skipDstCheck:     false,
		},
		{
			name:       "Map2S: CollectErrors - Copies Normal, copies Hello, collects error for string",
			src:        map[string]interface{}{"Normal": "normal", "sensitive_str": "secret_map_val", "hello_field": "hello_map_val"},
			dstInitial: &Dest{Normal: "initial_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			opts:       []copier.Option{copier.WithErrors(true)},
			expectDst: &Dest{
				Normal:       "normal",
				SensitiveStr: "initial_secret",
				HelloField:   Hello(base64.StdEncoding.EncodeToString([]byte("hello_map_val"))),
			},
			expectErrContent: "sensitive item 'field 'sensitive_str' from mapKey 'sensitive_str''",
			skipDstCheck:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := copier.New(tt.opts...)
			c.WithDebug(true)

			currentDst := cloneInitialDestState(t, tt.dstInitial)

			err := c.Copy(currentDst, tt.src)

			if tt.expectErrContent != "" {
				if err == nil {
					t.Errorf("Expected error containing '%s', but got nil", tt.expectErrContent)
				} else if !strings.Contains(err.Error(), tt.expectErrContent) {
					t.Errorf("Expected error containing '%s', got: %v", tt.expectErrContent, err)
				}
				if !tt.skipDstCheck && err != nil && strings.Contains(err.Error(), tt.expectErrContent) {
					assertDeepEqual(t, currentDst, tt.expectDst, "Destination state mismatch (after expected error '%s')", tt.expectErrContent)
				} else if !tt.skipDstCheck && err == nil {
					assertDeepEqual(t, currentDst, tt.expectDst, "Destination state mismatch (NO error occurred but one was expected containing '%s')", tt.expectErrContent)
				}
			} else {
				if err != nil {
					t.Fatalf("Expected no error, but got: %v", err)
				}
				assertDeepEqual(t, currentDst, tt.expectDst, "Destination state mismatch")
			}
		})
	}
}

func TestSliceSensitive(t *testing.T) {
	tests := []struct {
		name       string
		srcSlice   []Hello
		dstInitial interface{}
		opts       []copier.Option
		expectDst  []Hello
		expectErr  string
		asArray    bool
		arraySize  int
	}{
		{
			name:       "Slice - Skip sensitive",
			srcSlice:   []Hello{"s_hello1", "s_hello2"},
			dstInitial: []Hello{"s_initial1", "s_initial2"}, // Raw strings
			opts:       []copier.Option{copier.WithSkipSensitive(true)},
			expectDst:  []Hello{"s_initial1", "s_initial2"}, // Expect raw strings to be preserved
		},
		{
			name:       "Slice - Copy sensitive with Copiable",
			srcSlice:   []Hello{"s_hello1", "s_hello2"},
			dstInitial: []Hello{"s_initial1", "s_initial2"}, // Raw strings in initial
			opts:       []copier.Option{},                   // Default copier, will use Hello.Copy
			expectDst:  []Hello{Hello(base64.StdEncoding.EncodeToString([]byte("s_hello1"))), Hello(base64.StdEncoding.EncodeToString([]byte("s_hello2")))},
		},
		{
			name:       "Slice - Error on sensitive with Merge (Copiable wants overwrite)",
			srcSlice:   []Hello{"s_merge1", "s_merge2"},
			dstInitial: []Hello{"s_initial_merge1", "s_initial_merge2"},
			opts:       []copier.Option{copier.WithMerge()},
			expectErr:  "Copiable for sensitive 'sliceElem[0]' set Overwrite=true, but copier is in Merge mode",
		},
		{
			name:       "Array - Copy sensitive with Copiable",
			srcSlice:   []Hello{"a_hello1", "a_hello2"},      // Source elements for array
			dstInitial: [2]Hello{"a_initial1", "a_initial2"}, // Raw strings in initial array
			asArray:    true,
			arraySize:  2,
			opts:       []copier.Option{},
			expectDst:  []Hello{Hello(base64.StdEncoding.EncodeToString([]byte("a_hello1"))), Hello(base64.StdEncoding.EncodeToString([]byte("a_hello2")))},
		},
		{
			name:       "Array - Skip sensitive",
			srcSlice:   []Hello{"a_skip1", "a_skip2"},            // Source elements for array
			dstInitial: [2]Hello{"a_init_skip1", "a_init_skip2"}, // Raw strings in initial array
			asArray:    true,
			arraySize:  2,
			opts:       []copier.Option{copier.WithSkipSensitive(true)},
			expectDst:  []Hello{"a_init_skip1", "a_init_skip2"}, // Expect raw strings to be preserved
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := copier.NewCopier()
			if tt.opts != nil {
				c = copier.New(tt.opts...)
			}
			c.WithDebug(false)

			var err error
			var finalResultAsSlice []Hello

			var ptrToDstInitialForClone interface{}
			dstInitialVal := reflect.ValueOf(tt.dstInitial)
			if dstInitialVal.Kind() == reflect.Array {
				tempArrayPtr := reflect.New(dstInitialVal.Type())
				tempArrayPtr.Elem().Set(dstInitialVal)
				ptrToDstInitialForClone = tempArrayPtr.Interface()
			} else if dstInitialVal.Kind() == reflect.Slice {
				// For slice, dstInitial itself is the value (e.g. []Hello{...})
				// To pass a pointer to it for cloning:
				tempSlicePtr := reflect.New(dstInitialVal.Type())
				tempSlicePtr.Elem().Set(dstInitialVal)
				ptrToDstInitialForClone = tempSlicePtr.Interface()
			} else {
				t.Fatalf("[%s] Test setup error: dstInitial must be a slice or array value.", tt.name)
			}

			currentProcessDest := cloneInitialDestState(t, ptrToDstInitialForClone)

			if tt.asArray {
				// currentProcessDest is *[N]Hello. We copy into this pointer.
				srcArrayVal := reflect.New(reflect.ValueOf(currentProcessDest).Elem().Type()).Elem()
				for i := 0; i < tt.arraySize; i++ {
					if i < len(tt.srcSlice) {
						srcArrayVal.Index(i).Set(reflect.ValueOf(tt.srcSlice[i]))
					}
				}
				err = c.Copy(currentProcessDest, srcArrayVal.Interface())
				if err == nil {
					val := reflect.ValueOf(currentProcessDest).Elem()
					finalResultAsSlice = make([]Hello, val.Len())
					for i := 0; i < val.Len(); i++ {
						finalResultAsSlice[i] = val.Index(i).Interface().(Hello)
					}
				}
			} else {
				// currentProcessDest is *[]Hello. We copy into this pointer.
				err = c.Copy(currentProcessDest, tt.srcSlice)
				if err == nil {
					finalResultAsSlice = *(currentProcessDest.(*[]Hello))
				}
			}

			if tt.expectErr != "" {
				if err == nil {
					t.Fatalf("expected error containing '%s', got nil", tt.expectErr)
				}
				if !strings.Contains(err.Error(), tt.expectErr) {
					t.Fatalf("expected error containing '%s', got: %v", tt.expectErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			assertDeepEqual(t, finalResultAsSlice, tt.expectDst, "Slice/Array sensitive test failed")
		})
	}
}

func TestMapSensitive(t *testing.T) {
	tests := []struct {
		name       string
		src        map[string]Hello
		dstInitial interface{}
		opts       []copier.Option
		expectDst  map[string]Hello
		expectErr  string
	}{
		{
			name:       "Skip sensitive values",
			src:        map[string]Hello{"key1": "hello_src1", "key2": "hello_src2"},
			dstInitial: &map[string]Hello{"key1": "initial1", "key2": "initial2", "key3": "untouched"}, // Initial raw values
			opts:       []copier.Option{copier.WithSkipSensitive(true)},
			expectDst:  map[string]Hello{"key1": "initial1", "key2": "initial2", "key3": "untouched"}, // Expect raw values preserved
		},
		{
			name:       "Copy sensitive with Copiable",
			src:        map[string]Hello{"key1": "hello1", "key2": "hello2"},
			dstInitial: &map[string]Hello{"key_old": "old_val", "key1": "initial1_for_overwrite"}, // key1 will be overwritten
			opts:       []copier.Option{},                                                         // Default copier uses Hello.Copy
			expectDst: map[string]Hello{ // key_old removed by overwrite logic, key1/key2 transformed
				"key1": Hello(base64.StdEncoding.EncodeToString([]byte("hello1"))),
				"key2": Hello(base64.StdEncoding.EncodeToString([]byte("hello2"))),
			},
		},
		{
			name:       "Error on sensitive with Merge (Copiable wants overwrite)",
			src:        map[string]Hello{"key1": "hello_merge1"},    // Source key1 will attempt to merge
			dstInitial: &map[string]Hello{"key1": "initial_merge1"}, // Dest key1 exists and is non-empty
			opts:       []copier.Option{copier.WithMerge()},
			expectErr:  "Copiable for sensitive 'mapValue[key:key1]' set Overwrite=true, but copier is in Merge mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := copier.NewCopier()
			if tt.opts != nil {
				c = copier.New(tt.opts...)
			}
			c.WithDebug(false)

			currentDstPtr := cloneInitialDestState(t, tt.dstInitial).(*map[string]Hello)
			if *currentDstPtr == nil && tt.dstInitial.(*map[string]Hello) != nil {
				*currentDstPtr = make(map[string]Hello)
				// Manually copy if needed for complex initial states not handled by simple clone
				if tt.dstInitial.(*map[string]Hello) != nil {
					for k, v_raw := range *(tt.dstInitial.(*map[string]Hello)) {
						// For Hello type, direct assignment is fine for cloning raw strings
						(*currentDstPtr)[k] = v_raw
					}
				}
			}

			originalDestMapContentBeforeCopy := make(map[string]Hello)
			if *currentDstPtr != nil {
				for k, v := range *currentDstPtr {
					originalDestMapContentBeforeCopy[k] = v
				}
			}

			err := c.Copy(currentDstPtr, tt.src)

			if tt.expectErr != "" {
				if err == nil {
					t.Fatalf("expected error containing '%s', got nil", tt.expectErr)
				}
				if !strings.Contains(err.Error(), tt.expectErr) {
					t.Fatalf("expected error containing '%s', got: %v", tt.expectErr, err)
				}
				// If an error related to merge conflict was expected, check the specific key
				if strings.Contains(tt.name, "Merge") && strings.Contains(tt.expectErr, "Copiable for sensitive") {
					// The merge skip happens before processItem, so the original value should be there.
					// cloneInitialDestState should have given us the raw "initial_merge1"
					if val, ok := (*currentDstPtr)["key1"]; !ok || val != Hello("initial_merge1") {
						t.Errorf("Merge error test: Key 'key1' was not preserved or has wrong value. Got: %v, Expected: initial_merge1. Original snapshot: %v", val, originalDestMapContentBeforeCopy["key1"])
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertDeepEqual(t, *currentDstPtr, tt.expectDst, "Map sensitive test failed")
		})
	}
}

func TestPtrInterfaceSensitive(t *testing.T) {
	type PtrDest struct {
		Value *Hello
	}
	type InterfaceDestTest struct {
		Value interface{}
	}

	initialPtrVal := Hello("initial_ptr")     // Raw string
	initialIfaceVal := Hello("initial_iface") // Raw string

	tests := []struct {
		name       string
		src        interface{}
		dstInitial interface{}
		opts       []copier.Option
		expectDst  interface{}
		expectErr  string
	}{
		{
			name:       "PtrDest - Skip sensitive pointer",
			src:        PtrDest{Value: ptr(Hello("hello_src_skip"))},
			dstInitial: &PtrDest{Value: &initialPtrVal}, // Initial dest has a raw string pointer
			opts:       []copier.Option{copier.WithSkipSensitive(true)},
			expectDst:  &PtrDest{Value: &initialPtrVal}, // Expect initial raw string pointer to be preserved
		},
		{
			name:       "PtrDest - Copy sensitive pointer with Copiable",
			src:        PtrDest{Value: ptr(Hello("hello_src_copy"))},
			dstInitial: &PtrDest{Value: ptr(Hello("overwrite_me"))},
			opts:       []copier.Option{}, // Default, Hello.Copy will run
			expectDst:  &PtrDest{Value: ptr(Hello(base64.StdEncoding.EncodeToString([]byte("hello_src_copy"))))},
		},
		{
			name:       "InterfaceDest - Skip sensitive interface",
			src:        InterfaceDestTest{Value: Hello("hello_iface_src_skip")},
			dstInitial: &InterfaceDestTest{Value: initialIfaceVal}, // Initial dest has raw string in interface
			opts:       []copier.Option{copier.WithSkipSensitive(true)},
			expectDst:  &InterfaceDestTest{Value: initialIfaceVal}, // Expect initial raw string to be preserved
		},
		{
			name:       "InterfaceDest - Copy sensitive interface with Copiable",
			src:        InterfaceDestTest{Value: Hello("hello_iface_src_copy")},
			dstInitial: &InterfaceDestTest{Value: Hello("overwrite_iface_me")},
			opts:       []copier.Option{},
			expectDst:  &InterfaceDestTest{Value: Hello(base64.StdEncoding.EncodeToString([]byte("hello_iface_src_copy")))},
		},
		{
			name:       "PtrDest - Copiable error for *Hello (source *Hello, Copiable on Hello)",
			src:        PtrDest{Value: ptr(Hello("hello_src_ptr_err"))},
			dstInitial: &PtrDest{Value: nil},
			opts:       []copier.Option{},
			expectErr:  "invalid source type for Hello.Copy: expected string, Hello, or *Hello, got *tests.Hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := copier.NewCopier()
			if tt.opts != nil {
				c = copier.New(tt.opts...)
			}
			c.WithDebug(false)

			currentDst := cloneInitialDestState(t, tt.dstInitial)

			err := c.Copy(currentDst, tt.src)

			if tt.expectErr != "" {
				if err == nil {
					t.Fatalf("expected error containing '%s', got nil", tt.expectErr)
				}
				var foundExpectedError bool
				if strings.Contains(err.Error(), tt.expectErr) {
					foundExpectedError = true
				} else {
					innerErr := err
					for innerErr != nil {
						if strings.Contains(innerErr.Error(), tt.expectErr) {
							foundExpectedError = true
							break
						}
						innerErr = errors.Unwrap(innerErr)
					}
				}
				if !foundExpectedError {
					t.Fatalf("expected error containing '%s', got: %v", tt.expectErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertDeepEqual(t, currentDst, tt.expectDst, "Pointer/Interface sensitive test failed")
		})
	}
}

func TestSliceMapConversions(t *testing.T) {
	tests := []struct {
		name         string
		src          interface{}
		dstInitial   interface{}
		opts         []copier.Option
		expectDst    interface{}
		expectErr    string
		isSliceToMap bool
	}{
		{
			name:         "Slice to map with sensitive",
			src:          []Hello{"hello1", "hello2"},
			dstInitial:   func() *map[int]Hello { m := make(map[int]Hello); return &m }(),
			isSliceToMap: true,
			opts:         []copier.Option{},
			expectDst: map[int]Hello{
				0: Hello(base64.StdEncoding.EncodeToString([]byte("hello1"))),
				1: Hello(base64.StdEncoding.EncodeToString([]byte("hello2"))),
			},
		},
		{
			name:         "Map to slice with sensitive",
			src:          map[string]Hello{"keyA": "val1", "keyB": "val2"},
			dstInitial:   func() *[]Hello { s := make([]Hello, 0); return &s }(),
			isSliceToMap: false,
			opts:         []copier.Option{},
			expectDst: []Hello{
				Hello(base64.StdEncoding.EncodeToString([]byte("val1"))),
				Hello(base64.StdEncoding.EncodeToString([]byte("val2"))),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := copier.NewCopier()
			if tt.opts != nil {
				c = copier.New(tt.opts...)
			}
			c.WithDebug(false)

			currentProcessDest := cloneInitialDestState(t, tt.dstInitial)

			err := c.Copy(currentProcessDest, tt.src)

			if tt.expectErr != "" {
				if err == nil {
					t.Fatalf("expected error containing '%s', got nil", tt.expectErr)
				}
				if !strings.Contains(err.Error(), tt.expectErr) {
					t.Fatalf("expected error containing '%s', got: %v", tt.expectErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var actualDstValue interface{}
			if tt.isSliceToMap {
				actualDstValue = *(currentProcessDest.(*map[int]Hello))
			} else {
				actualDstValue = *(currentProcessDest.(*[]Hello))
				if actualSlice, ok1 := actualDstValue.([]Hello); ok1 {
					if expectedSlice, ok2 := tt.expectDst.([]Hello); ok2 {
						sortHelloSlice(actualSlice)
						sortHelloSlice(expectedSlice)
					}
				}
			}
			assertDeepEqual(t, actualDstValue, tt.expectDst, "Slice/Map conversion sensitive test failed")
		})
	}
}

func TestChanSensitive(t *testing.T) {
	tests := []struct {
		name          string
		srcFn         func() chan Hello
		dstInitialFn  func() chan Hello
		opts          []copier.Option
		expectDstRecv []Hello
		expectErr     string
	}{
		{
			name: "Skip sensitive channel elements",
			srcFn: func() chan Hello {
				ch := make(chan Hello, 2)
				ch <- Hello("hello_src1")
				ch <- Hello("hello_src2")
				close(ch)
				return ch
			},
			dstInitialFn:  func() chan Hello { return make(chan Hello, 2) },
			opts:          []copier.Option{copier.WithSkipSensitive(true)},
			expectDstRecv: []Hello{},
		},
		{
			name: "Copy sensitive channel elements with Copiable",
			srcFn: func() chan Hello {
				ch := make(chan Hello, 2)
				ch <- Hello("hello1")
				ch <- Hello("hello2")
				close(ch)
				return ch
			},
			dstInitialFn: func() chan Hello { return make(chan Hello, 2) },
			opts:         []copier.Option{},
			expectDstRecv: []Hello{
				Hello(base64.StdEncoding.EncodeToString([]byte("hello1"))),
				Hello(base64.StdEncoding.EncodeToString([]byte("hello2"))),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := copier.NewCopier()
			if tt.opts != nil {
				c = copier.New(tt.opts...)
			}
			c.WithDebug(false)

			srcChan := tt.srcFn()
			// Create the destination channel for this test run.
			// cloneInitialDestState is not suitable for channels directly.
			testDestChan := tt.dstInitialFn()

			// Pass the pointer to the destination channel variable for c.Copy
			err := c.Copy(&testDestChan, srcChan)

			if tt.expectErr != "" {
				if err == nil {
					t.Fatalf("expected error containing '%s', got nil", tt.expectErr)
				}
				if !strings.Contains(err.Error(), tt.expectErr) {
					t.Fatalf("expected error containing '%s', got: %v", tt.expectErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// It's the responsibility of the copyChan function to handle closing or not.
			// For testing, we rely on the source channel being closed to terminate the receive loop.
			// The destination channel (testDestChan) will be drained.
			// If copyChan replaces testDestChan with a new channel, that new channel must be drained.
			// It's good practice for copyChan to NOT close the dest channel it's given.
			// The caller (this test) should close the channel if it's managing its lifecycle for reads.
			// However, if copyChan makes a *new* channel and assigns it to *testDestChan,
			// then that new channel needs to be closed by the test for the range loop to end.
			// For simplicity, if the test expects elements, we assume the copy finishes and then we close the *final* dest chan.
			if testDestChan != nil && cap(testDestChan) > 0 { // Check if it's a valid channel to close
				// Attempt to close only if it wasn't potentially closed by the copy logic (though unlikely for dest)
				// This is risky if copy operation could be concurrent. For tests, usually fine.
				// The main thing is that the src channel is closed by srcFn.
			}

			var receivedElements []Hello
			if testDestChan != nil { // Ensure channel is not nil before ranging
				// Drain and then close. Better: have a way to know when copy is done.
				// Since srcChan is closed by srcFn, copyChan should stop sending.
				// The loop will naturally end when testDestChan (which is the one populated) is empty AND closed.
				// If testDestChan isn't closed by copyChan, we need to close it after copy,
				// but before ranging if we want range to terminate when empty.
				// A select with a timeout might be safer if channel closing is complex.

				// Assuming copyChan copies all and then stops.
				// If the testDestChan was replaced by copyChan, the original dstChan is irrelevant.
				// We need to close the channel that was actually written to.

				// Simplest: if we expect elements, we read them. If we expect none, the loop won't run if empty.
				// Closing testDestChan after the copy is generally safe if copyChan doesn't close it.
				go func() { // Safely close after ensuring all sends are done (or src is exhausted)
					// This assumes copyChan is synchronous.
					// If copyChan were async, this would be more complex.
					// For now, assume synchronous copy of channel elements.
					// The actual close is better done after the receive loop if we know how many items.
				}()

				done := false
				for !done {
					select {
					case elem, ok := <-testDestChan:
						if !ok {
							done = true
							break
						}
						receivedElements = append(receivedElements, elem)
					// Add a timeout to prevent test hanging if channel logic is broken
					case <-time.After(100 * time.Millisecond): // Safety timeout
						if len(receivedElements) != len(tt.expectDstRecv) {
							t.Logf("Warning: Channel read timed out. Received %d, expected %d.", len(receivedElements), len(tt.expectDstRecv))
						}
						done = true
					}
				}
			}

			assertDeepEqual(t, receivedElements, tt.expectDstRecv, "Channel sensitive test failed on received elements")
		})
	}
}
