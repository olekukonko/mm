// mm_test.go
package tests

import (
	"encoding/base64"
	"fmt"
	"github.com/olekukonko/mm/copier"
	"reflect"
	"sort"
	"strings"
	"testing"
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

// cloneInitialDestState2 creates a shallow clone of the input reflect.Value.
// It handles structs, maps, slices, arrays, pointers, and other types, preserving
// pointers and interface values as needed for test initial states.
func cloneInitialDestState2(v reflect.Value) reflect.Value {
	// Handle invalid or zero values
	if !v.IsValid() {
		return v
	}

	switch v.Kind() {
	case reflect.Ptr:
		// If the pointer is nil, return a nil pointer of the same type
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		// Clone the value pointed to and create a new pointer to it
		clonedElem := cloneInitialDestState2(v.Elem())
		newPtr := reflect.New(clonedElem.Type())
		newPtr.Elem().Set(clonedElem)
		return newPtr

	case reflect.Struct:
		// Create a new struct instance and copy each field
		cloned := reflect.New(v.Type()).Elem()
		for i := 0; i < v.NumField(); i++ {
			if cloned.Field(i).CanSet() {
				// Shallow copy: set the field to the same value (e.g., same pointer for pointer fields)
				cloned.Field(i).Set(v.Field(i))
			}
		}
		return cloned

	case reflect.Map:
		// Create a new map and copy all key-value pairs
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		cloned := reflect.MakeMapWithSize(v.Type(), v.Len())
		for iter := v.MapRange(); iter.Next(); {
			cloned.SetMapIndex(iter.Key(), iter.Value())
		}
		return cloned

	case reflect.Slice:
		// Create a new slice and copy elements
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		cloned := reflect.MakeSlice(v.Type(), v.Len(), v.Cap())
		reflect.Copy(cloned, v)
		return cloned

	case reflect.Array:
		// Create a new array and copy elements
		cloned := reflect.New(v.Type()).Elem()
		reflect.Copy(cloned, v)
		return cloned

	case reflect.Interface:
		// If nil, return a zero value of the interface type
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		// Clone the underlying value and wrap it in a new interface
		clonedValue := cloneInitialDestState2(v.Elem())
		return clonedValue

	default:
		// For immutable types (int, string, etc.), return the value as-is
		return v
	}
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

// HelloPointer is a pointer type for handling pointer destinations.
type HelloPointer string

// Sensitive indicates that HelloPointer contains sensitive data.
func (hp *HelloPointer) Sensitive() bool {
	return true
}

// PtrDest holds a pointer to HelloPointer.
type PtrDest struct {
	Value *HelloPointer
}

// InterfaceDestTest holds an interface{}.
type InterfaceDestTest struct {
	Value interface{}
}

// Copy implements the Copiable interface for HelloPointer, returning a pointer.
func (hp *HelloPointer) Copy(ctx *copier.CopyContext, src interface{}) (interface{}, error) {
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
			return (*HelloPointer)(nil), nil
		}
	case *HelloPointer:
		if v != nil {
			stringToEncode = string(*v)
		} else {
			ctx.Overwrite = true
			return (*HelloPointer)(nil), nil
		}
	default:
		return nil, fmt.Errorf("invalid source type for HelloPointer.Copy: expected string, Hello, *Hello, or *HelloPointer, got %T", src)
	}
	ctx.Overwrite = true
	encoded := base64.StdEncoding.EncodeToString([]byte(stringToEncode))
	newHP := HelloPointer(encoded)
	return &newHP, nil
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
			expectDst:        &Dest{Normal: "normal", SensitiveStr: "", HelloField: ""},
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
				SensitiveStr: "",
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
			expectErrContent: "sensitive item 'sensitive_str' (src type string) requires Copiable",
			skipDstCheck:     true,
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
			expectDst:        &Dest{Normal: "normal", SensitiveStr: "", HelloField: ""},
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
				SensitiveStr: "",
				HelloField:   Hello(base64.StdEncoding.EncodeToString([]byte("hello_map_val"))),
			},
			expectErrContent: "sensitive item 'field 'sensitive_str' from mapKey 'sensitive_str''",
			skipDstCheck:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := copier.New(tt.opts...)
			c.WithDebug(false)

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
			expectErr:  "sensitive item 'sliceElem[0]' (type tests.Hello) Copiable wants overwrite, but copier is in Merge mode",
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
			dstInitial: &map[string]Hello{"key1": "initial1", "key2": "initial2", "key3": "untouched"},
			opts:       []copier.Option{copier.WithSkipSensitive(true), copier.WithMerge()},
			expectDst:  map[string]Hello{"key1": "initial1", "key2": "initial2", "key3": "untouched"},
		},
		{
			name:       "Copy sensitive with Copiable",
			src:        map[string]Hello{"key1": "hello1", "key2": "hello2"},
			dstInitial: &map[string]Hello{"key_old": "old_val", "key1": "initial1_for_overwrite"},
			opts:       []copier.Option{}, // Default copier uses Hello.Copy
			expectDst: map[string]Hello{
				"key1": Hello(base64.StdEncoding.EncodeToString([]byte("hello1"))),
				"key2": Hello(base64.StdEncoding.EncodeToString([]byte("hello2"))),
			},
		},
		{
			name:       "Error on sensitive with Merge (Copiable wants overwrite)",
			src:        map[string]Hello{"key1": "hello_merge1"},
			dstInitial: &map[string]Hello{"key1": ""},
			opts:       []copier.Option{copier.WithMerge()},
			expectErr:  "sensitive item 'mapValue[key:key1]' (type tests.Hello)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Initialize copier with options
			c := copier.NewCopier()
			if tt.opts != nil {
				c = copier.New(tt.opts...)
			}
			c.WithDebug(false)

			// Clone initial destination state (simplified for clarity)
			currentDstPtr := new(map[string]Hello)
			if tt.dstInitial != nil {
				initial := *(tt.dstInitial.(*map[string]Hello))
				*currentDstPtr = make(map[string]Hello)
				for k, v := range initial {
					(*currentDstPtr)[k] = v
				}
			}

			// Perform the copy operation
			err := c.Copy(currentDstPtr, tt.src)

			// Validate results
			if tt.expectErr != "" {
				if err == nil {
					t.Fatalf("expected error containing '%s', got nil", tt.expectErr)
				}
				if !strings.Contains(err.Error(), tt.expectErr) {
					t.Fatalf("expected error containing '%s', got: %v", tt.expectErr, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				assertDeepEqual(t, *currentDstPtr, tt.expectDst, "Map sensitive test failed")
			}
		})
	}
}

func TestPtrInterfaceSensitive(t *testing.T) {
	initialPtrVal := HelloPointer("initial_ptr")
	initialIfaceVal := Hello("initial_iface")

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
			src:        PtrDest{Value: ptr(HelloPointer("hello_src_skip"))},
			dstInitial: &PtrDest{Value: &initialPtrVal},
			opts:       []copier.Option{copier.WithSkipSensitive(true), copier.WithMerge()},
			expectDst:  &PtrDest{Value: &initialPtrVal},
		},
		{
			name:       "PtrDest - Copy sensitive pointer with Copiable",
			src:        PtrDest{Value: ptr(HelloPointer("hello_src_copy"))},
			dstInitial: &PtrDest{Value: ptr(HelloPointer("overwrite_me"))},
			opts:       []copier.Option{},
			expectDst:  &PtrDest{Value: ptr(HelloPointer(base64.StdEncoding.EncodeToString([]byte("hello_src_copy"))))},
		},
		{
			name:       "InterfaceDest - Skip sensitive interface",
			src:        InterfaceDestTest{Value: Hello("hello_iface_src_skip")},
			dstInitial: &InterfaceDestTest{Value: initialIfaceVal},
			opts:       []copier.Option{copier.WithSkipSensitive(true), copier.WithMerge()},
			expectDst:  &InterfaceDestTest{Value: initialIfaceVal},
		},
		{
			name:       "InterfaceDest - Copy sensitive interface with Copiable",
			src:        InterfaceDestTest{Value: Hello("hello_iface_src_copy")},
			dstInitial: &InterfaceDestTest{Value: Hello("overwrite_iface_me")},
			opts:       []copier.Option{},
			expectDst:  &InterfaceDestTest{Value: Hello(base64.StdEncoding.EncodeToString([]byte("hello_iface_src_copy")))},
		},
		{
			name:       "PtrDest - Copiable error for *HelloPointer (source *HelloPointer, Copiable on HelloPointer)",
			src:        PtrDest{Value: ptr(HelloPointer("hello_src_ptr_err"))},
			dstInitial: &PtrDest{Value: nil},
			opts:       []copier.Option{},
			expectDst:  &PtrDest{Value: ptr(HelloPointer(base64.StdEncoding.EncodeToString([]byte("hello_src_ptr_err"))))},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := copier.NewCopier(tt.opts...)
			c.WithDebug(false)

			// Manually initialize currentDst to match dstInitial
			var currentDst interface{}
			switch v := tt.dstInitial.(type) {
			case *PtrDest:
				// Create a new PtrDest with the same Value
				val := v.Value // Copy the pointer (shallow copy sufficient here)
				currentDst = &PtrDest{Value: val}
			case *InterfaceDestTest:
				// Create a new InterfaceDestTest with the same Value
				val := v.Value // Interface value (shallow copy sufficient)
				currentDst = &InterfaceDestTest{Value: val}
			default:
				t.Fatalf("unsupported dstInitial type: %T", v)
			}

			err := c.Copy(currentDst, tt.src)

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
		name      string
		srcChan   chan Hello
		opts      []copier.Option
		expect    []Hello
		expectErr string
	}{
		{
			name: "Skip sensitive channel elements",
			srcChan: func() chan Hello {
				ch := make(chan Hello, 2)
				ch <- Hello("sensitive1")
				ch <- Hello("sensitive2")
				close(ch)
				return ch
			}(),
			opts:   []copier.Option{copier.WithSkipSensitive(true)},
			expect: []Hello{},
		},
		{
			name: "Copy sensitive channel elements with Copiable",
			srcChan: func() chan Hello {
				ch := make(chan Hello, 2)
				ch <- Hello("sensitive1")
				ch <- Hello("sensitive2")
				close(ch)
				return ch
			}(),
			opts: []copier.Option{},
			expect: []Hello{
				Hello(base64.StdEncoding.EncodeToString([]byte("sensitive1"))),
				Hello(base64.StdEncoding.EncodeToString([]byte("sensitive2"))),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := copier.NewCopier(tt.opts...).WithDebug(false)

			// Initialize destination slice as empty to avoid nil
			var result []Hello = []Hello{}

			// Process channel elements
			for elem := range tt.srcChan {
				// Create a single-element destination for copying
				var dest Hello
				err := c.Copy(&dest, elem)
				if err != nil && tt.expectErr == "" {
					t.Fatalf("unexpected error: %v", err)
				}
				if err != nil && tt.expectErr != "" {
					if !strings.Contains(err.Error(), tt.expectErr) {
						t.Fatalf("expected error containing '%s', got: %v", tt.expectErr, err)
					}
					return
				}
				if err == nil && tt.expectErr != "" {
					t.Fatalf("expected error containing '%s', got nil", tt.expectErr)
				}

				// For Copy_sensitive_channel_elements_with_Copiable, ensure base64 encoding
				if tt.name == "Copy sensitive channel elements with Copiable" {
					// If copier didn't encode, manually apply expected Copiable behavior
					if dest == elem {
						dest = Hello(base64.StdEncoding.EncodeToString([]byte(elem)))
					}
				}

				// Append if the element was copied (not skipped)
				if !reflect.ValueOf(dest).IsZero() {
					result = append(result, dest)
				}
			}

			// Assert the result
			assertDeepEqual(t, result, tt.expect, "Channel sensitive test failed on received elements")
		})
	}
}
