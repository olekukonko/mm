// mm_test.go
package mm

import (
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Helper function to clone initial destination states reliably
// mm_test.go
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
		return reflect.Zero(val.Type()).Interface() // Return a nil pointer of the same type
	}

	elemType := val.Type().Elem()
	clonedDestPtr := reflect.New(elemType) // *T

	// For cloning, we want a basic, faithful field copy if possible,
	// without triggering advanced sensitive/Copiable logic of the main library,
	// as we are just setting up the initial state.
	// A direct value copy if types are identical is simplest.
	// If initialDestPtr is *Dest, and clonedDestPtr is *Dest:
	if val.Type() == clonedDestPtr.Type() {
		// Simple field-by-field reflection copy for same types.
		// This bypasses the mm.Copier for setting up initial state.
		srcElem := val.Elem()
		dstElem := clonedDestPtr.Elem()
		if srcElem.Type() == dstElem.Type() && srcElem.Kind() == reflect.Struct {
			for i := 0; i < srcElem.NumField(); i++ {
				if dstElem.Field(i).CanSet() {
					dstElem.Field(i).Set(srcElem.Field(i))
				}
			}
			return clonedDestPtr.Interface()
		}
	}

	// Fallback to using a simple mm.Copier if direct reflection copy is not straightforward
	// This copier should be as "dumb" as possible for cloning.
	cloner := NewCopier().WithDebug(false).WithSkipSensitive(false).WithErrors(true) // Process sensitive, collect errors
	err := cloner.Copy(clonedDestPtr.Interface(), initialDestPtr)
	if err != nil {
		// If cloning fails even with a permissive copier, there's a deeper issue or test setup problem.
		// For sensitive fields that error during cloning, they might not be set in clonedDestPtr.
		// This might be acceptable if the test's main operation will overwrite them anyway.
		// For now, we'll log a warning if the clone had errors.
		t.Logf("Warning during cloneInitialDestState for type %T (errors collected): %v", initialDestPtr, err)
	}
	return clonedDestPtr.Interface()
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

func (h Hello) Copy(ctx *CopyContext, src interface{}) (interface{}, error) {
	var stringToEncode string
	switch v := src.(type) {
	case string:
		stringToEncode = v
	case Hello:
		stringToEncode = string(v)
	case *Hello: // Handle *Hello
		if v != nil {
			stringToEncode = string(*v)
		} else {
			ctx.Overwrite = true // Indicate intent to set
			return nil, nil      // Copiable returning nil + Overwrite=true means set dest to zero/nil
		}
	default:
		return nil, fmt.Errorf("invalid source type for Hello.Copy: expected string, Hello, or *Hello, got %T", src)
	}
	ctx.Overwrite = true // Always intend to overwrite if conversion happens
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
		opts             []Option
		expectDst        interface{}
		expectErrContent string
		skipDstCheck     bool // True if we shouldn't check dest state after an expected error
	}{
		{
			name:             "S2S: Default - Error on sensitive string without Copiable",
			src:              Source{Normal: "normal", SensitiveStr: "secret", HelloField: "hello"},
			dstInitial:       &Dest{Normal: "initial", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			opts:             []Option{},
			expectErrContent: "sensitive item 'sensitive_str' (src type string) requires Copiable", // General check
			skipDstCheck:     true,
		},
		{
			name:             "S2S: SkipSensitive - Skips all sensitive",
			src:              Source{Normal: "normal", SensitiveStr: "secret", HelloField: "hello"},
			dstInitial:       &Dest{Normal: "initial_val_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			opts:             []Option{WithSkipSensitive(true)},
			expectDst:        &Dest{Normal: "normal", SensitiveStr: "initial_secret", HelloField: "initial_hello"}, // Sensitive fields from dstInitial preserved
			expectErrContent: "",
			skipDstCheck:     false,
		},
		{
			name:       "S2S: CollectErrors - Copies non-sensitive, collects error for string, copies Hello",
			src:        Source{Normal: "normal", SensitiveStr: "secret", HelloField: "hello"},
			dstInitial: &Dest{Normal: "initial_val_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			opts:       []Option{WithErrors(true)},
			expectDst: &Dest{ // Normal copied, Hello copied, SensitiveStr (error) from dstInitial preserved
				Normal:       "normal",
				SensitiveStr: "initial_secret",
				HelloField:   Hello(base64.StdEncoding.EncodeToString([]byte("hello"))),
			},
			expectErrContent: "sensitive item 'sensitive_str' (src type string) requires Copiable", // General check
			skipDstCheck:     false,
		},
		{
			name:             "S2S: Merge - Skips non-empty dest fields before sensitivity",
			src:              Source{Normal: "normal_src", SensitiveStr: "secret_src", HelloField: "hello_src"},
			dstInitial:       &Dest{Normal: "initial_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"}, // All dest fields non-empty
			opts:             []Option{WithMerge()},
			expectDst:        &Dest{Normal: "initial_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"}, // All should be skipped by merge
			expectErrContent: "",
			skipDstCheck:     false,
		},
		// --- Struct to Map --- (expectErrContent adjusted)
		{
			name:             "S2Map: Default - Error on sensitive string",
			src:              Source{Normal: "normal", SensitiveStr: "secret", HelloField: "hello"},
			dstInitial:       func() *map[string]interface{} { m := make(map[string]interface{}); return &m }(),
			opts:             []Option{},
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
			opts: []Option{WithSkipSensitive(true)},
			// Normal WILL be overwritten. Sensitive fields are skipped from src, so their values in dstInitial map remain.
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
			opts: []Option{WithErrors(true)},
			expectDst: &map[string]interface{}{ // Normal & Hello copied, sensitive_str from dstInitial preserved
				"Normal":        "normal",
				"hello_field":   Hello(base64.StdEncoding.EncodeToString([]byte("hello"))),
				"sensitive_str": "keep_map_secret",
			},
			expectErrContent: "sensitive item 'mapValueForKey 'sensitive_str'",
			skipDstCheck:     false,
		},
		// --- Map to Struct --- (expectErrContent adjusted)
		{
			name:             "Map2S: Default - Error on sensitive string",
			src:              map[string]interface{}{"Normal": "normal", "sensitive_str": "secret", "hello_field": "hello"},
			dstInitial:       &Dest{Normal: "initial_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			opts:             []Option{},
			expectErrContent: "sensitive item 'field 'sensitive_str' from mapKey 'sensitive_str''",
			skipDstCheck:     true,
		},
		{
			name:             "Map2S: SkipSensitive",
			src:              map[string]interface{}{"Normal": "normal", "sensitive_str": "secret_map_val", "hello_field": "hello_map_val"},
			dstInitial:       &Dest{Normal: "initial_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			opts:             []Option{WithSkipSensitive(true)},
			expectDst:        &Dest{Normal: "normal", SensitiveStr: "initial_secret", HelloField: "initial_hello"}, // Sensitive fields in Dest remain
			expectErrContent: "",
			skipDstCheck:     false,
		},
		{
			name:       "Map2S: CollectErrors - Copies Normal, copies Hello, collects error for string",
			src:        map[string]interface{}{"Normal": "normal", "sensitive_str": "secret_map_val", "hello_field": "hello_map_val"},
			dstInitial: &Dest{Normal: "initial_norm", SensitiveStr: "initial_secret", HelloField: "initial_hello"},
			opts:       []Option{WithErrors(true)},
			expectDst: &Dest{ // Normal & Hello copied, SensitiveStr from dstInitial preserved
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
			c := New(tt.opts...)
			c.WithDebug(true) // Enable debug for detailed logs from these tests

			currentDst := cloneInitialDestState(t, tt.dstInitial)

			err := c.Copy(currentDst, tt.src)

			if tt.expectErrContent != "" {
				if err == nil {
					t.Errorf("Expected error containing '%s', but got nil", tt.expectErrContent)
				} else if !strings.Contains(err.Error(), tt.expectErrContent) {
					t.Errorf("Expected error containing '%s', got: %v", tt.expectErrContent, err)
				}
				// If an error was expected and it occurred, check dest state if not skipped
				if !tt.skipDstCheck && err != nil && strings.Contains(err.Error(), tt.expectErrContent) {
					assertDeepEqual(t, currentDst, tt.expectDst, "Destination state mismatch (after expected error '%s')", tt.expectErrContent)
				} else if !tt.skipDstCheck && err == nil { // Error expected but none occurred
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
	// ... (tests table definition as before, with corrected expectErrContent)
	tests := []struct {
		name       string
		srcSlice   []Hello
		dstInitial interface{} // Use interface{} to hold either []Hello or [N]Hello
		opts       []Option
		expectDst  []Hello
		expectErr  string
		asArray    bool
		arraySize  int
	}{
		{
			name:       "Slice - Skip sensitive",
			srcSlice:   []Hello{"s_hello1", "s_hello2"},
			dstInitial: []Hello{"s_initial1", "s_initial2"},
			opts:       []Option{WithSkipSensitive(true)},
			expectDst:  []Hello{"s_initial1", "s_initial2"},
		},
		{
			name:       "Slice - Copy sensitive with Copiable",
			srcSlice:   []Hello{"s_hello1", "s_hello2"},
			dstInitial: []Hello{"s_initial1", "s_initial2"},
			opts:       []Option{},
			expectDst:  []Hello{Hello(base64.StdEncoding.EncodeToString([]byte("s_hello1"))), Hello(base64.StdEncoding.EncodeToString([]byte("s_hello2")))},
		},
		{
			name:       "Slice - Error on sensitive with Merge (Copiable wants overwrite)",
			srcSlice:   []Hello{"s_merge1", "s_merge2"},
			dstInitial: []Hello{"s_initial_merge1", "s_initial_merge2"},
			opts:       []Option{WithMerge()},
			expectErr:  "Copiable for sensitive 'sliceElem[0]' set Overwrite=true, but copier is in Merge mode",
		},
		{
			name:       "Array - Copy sensitive with Copiable",
			srcSlice:   []Hello{"a_hello1", "a_hello2"},
			dstInitial: [2]Hello{"a_initial1", "a_initial2"},
			asArray:    true,
			arraySize:  2,
			opts:       []Option{},
			expectDst:  []Hello{Hello(base64.StdEncoding.EncodeToString([]byte("a_hello1"))), Hello(base64.StdEncoding.EncodeToString([]byte("a_hello2")))},
		},
		{
			name:       "Array - Skip sensitive",
			srcSlice:   []Hello{"a_skip1", "a_skip2"},
			dstInitial: [2]Hello{"a_init_skip1", "a_init_skip2"},
			asArray:    true,
			arraySize:  2,
			opts:       []Option{WithSkipSensitive(true)},
			expectDst:  []Hello{"a_init_skip1", "a_init_skip2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCopier()
			if tt.opts != nil {
				c = New(tt.opts...)
			}
			c.WithDebug(true)

			var err error
			var finalResultAsSlice []Hello

			// currentProcessDest will be a pointer to the actual slice or array type
			// For dstInitial which is a value (e.g. [2]Hello or []Hello), we need its address for cloning.
			var ptrToDstInitial interface{}
			dstInitialVal := reflect.ValueOf(tt.dstInitial)
			if dstInitialVal.Kind() == reflect.Array {
				tempArray := reflect.New(dstInitialVal.Type()) // *[N]Hello
				tempArray.Elem().Set(dstInitialVal)
				ptrToDstInitial = tempArray.Interface()
			} else if dstInitialVal.Kind() == reflect.Slice {
				tempSlice := reflect.New(dstInitialVal.Type()) // *[]Hello
				tempSlice.Elem().Set(dstInitialVal)            // Set the slice header
				ptrToDstInitial = tempSlice.Interface()
			} else {
				t.Fatalf("[%s] Test setup error: dstInitial must be a slice or array value.", tt.name)
			}

			currentProcessDest := cloneInitialDestState(t, ptrToDstInitial)

			if tt.asArray {
				// currentProcessDest is *[N]Hello.
				// Source tt.srcSlice needs to be converted to an array for c.Copy
				srcArrayVal := reflect.New(reflect.ValueOf(currentProcessDest).Elem().Type()).Elem() // Creates [N]Hello
				for i := 0; i < tt.arraySize; i++ {
					if i < len(tt.srcSlice) {
						srcArrayVal.Index(i).Set(reflect.ValueOf(tt.srcSlice[i]))
					}
				}
				err = c.Copy(currentProcessDest, srcArrayVal.Interface())
				if err == nil {
					// Convert array pointer back to slice for comparison
					val := reflect.ValueOf(currentProcessDest).Elem() // Get [N]Hello from *[N]Hello
					finalResultAsSlice = make([]Hello, val.Len())
					for i := 0; i < val.Len(); i++ {
						finalResultAsSlice[i] = val.Index(i).Interface().(Hello)
					}
				}
			} else {
				// currentProcessDest is *[]Hello.
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
		dstInitial interface{} // Should be *map[string]Hello for cloneInitialDestState
		opts       []Option
		expectDst  map[string]Hello // For direct comparison after c.Copy modifies the map
		expectErr  string
	}{
		{
			name:       "Skip sensitive values",
			src:        map[string]Hello{"key1": "hello_src1", "key2": "hello_src2"},
			dstInitial: &map[string]Hello{"key1": "initial1", "key2": "initial2", "key3": "untouched"},
			opts:       []Option{WithSkipSensitive(true)},
			expectDst:  map[string]Hello{"key1": "initial1", "key2": "initial2", "key3": "untouched"},
		},
		{
			name:       "Copy sensitive with Copiable",
			src:        map[string]Hello{"key1": "hello1", "key2": "hello2"},
			dstInitial: &map[string]Hello{"key_old": "old_val"},
			opts:       []Option{},
			expectDst: map[string]Hello{
				"key1": Hello(base64.StdEncoding.EncodeToString([]byte("hello1"))),
				"key2": Hello(base64.StdEncoding.EncodeToString([]byte("hello2"))),
			},
		},
		{
			name:       "Error on sensitive with Merge (Copiable wants overwrite)",
			src:        map[string]Hello{"key1": "hello_merge1"},
			dstInitial: &map[string]Hello{"key1": "initial_merge1"},
			opts:       []Option{WithMerge()},
			expectErr:  "Copiable for sensitive 'mapValue[key:key1]' set Overwrite=true, but copier is in Merge mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCopier()
			if tt.opts != nil {
				c = New(tt.opts...)
			}
			c.WithDebug(true)

			// currentDst will be *map[string]Hello, cloned from tt.dstInitial
			currentDstPtr := cloneInitialDestState(t, tt.dstInitial).(*map[string]Hello)
			if *currentDstPtr == nil && tt.dstInitial.(*map[string]Hello) != nil { // Ensure map is initialized if dstInitial was non-nil map
				*currentDstPtr = make(map[string]Hello)
				// If dstInitial pointed to a map, clone its contents manually or via another copy
				// For simplicity, if dstInitial was a pointer to a non-nil map, we ensure currentDstPtr points to an allocated map.
				// A full clone of map contents for dstInitial would be more robust here if needed.
				// The current cloneInitialDestState copies the map header, so if original map was nil, this is nil.
				// If original map was not nil, this will also be not nil.
			}

			err := c.Copy(currentDstPtr, tt.src) // Pass pointer to the map

			if tt.expectErr != "" {
				if err == nil {
					t.Fatalf("expected error containing '%s', got nil", tt.expectErr)
				}
				if !strings.Contains(err.Error(), tt.expectErr) {
					t.Fatalf("expected error containing '%s', got: %v", tt.expectErr, err)
				}
				// Optionally check state of *currentDstPtr if error was expected but partial copy might occur
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

	initialPtrVal := Hello("initial_ptr")
	initialIfaceVal := Hello("initial_iface")

	tests := []struct {
		name       string
		src        interface{}
		dstInitial interface{} // Should be *PtrDest or *InterfaceDestTest
		opts       []Option
		expectDst  interface{}
		expectErr  string
	}{
		{
			name:       "PtrDest - Skip sensitive pointer",
			src:        PtrDest{Value: ptr(Hello("hello_src_skip"))},
			dstInitial: &PtrDest{Value: &initialPtrVal},
			opts:       []Option{WithSkipSensitive(true)},
			expectDst:  &PtrDest{Value: &initialPtrVal},
		},
		{
			name:       "PtrDest - Copy sensitive pointer with Copiable",
			src:        PtrDest{Value: ptr(Hello("hello_src_copy"))},
			dstInitial: &PtrDest{Value: ptr(Hello("overwrite_me"))},
			opts:       []Option{},
			expectDst:  &PtrDest{Value: ptr(Hello(base64.StdEncoding.EncodeToString([]byte("hello_src_copy"))))},
		},
		{
			name:       "InterfaceDest - Skip sensitive interface",
			src:        InterfaceDestTest{Value: Hello("hello_iface_src_skip")},
			dstInitial: &InterfaceDestTest{Value: initialIfaceVal},
			opts:       []Option{WithSkipSensitive(true)},
			expectDst:  &InterfaceDestTest{Value: initialIfaceVal},
		},
		{
			name:       "InterfaceDest - Copy sensitive interface with Copiable",
			src:        InterfaceDestTest{Value: Hello("hello_iface_src_copy")},
			dstInitial: &InterfaceDestTest{Value: Hello("overwrite_iface_me")},
			opts:       []Option{},
			expectDst:  &InterfaceDestTest{Value: Hello(base64.StdEncoding.EncodeToString([]byte("hello_iface_src_copy")))},
		},
		{
			name:       "PtrDest - Copiable error for *Hello (source *Hello, Copiable on Hello)",
			src:        PtrDest{Value: ptr(Hello("hello_src_ptr_err"))},
			dstInitial: &PtrDest{Value: nil},
			opts:       []Option{},
			expectErr:  "invalid source type for Hello.Copy: expected string, Hello, or *Hello, got *mm.Hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCopier()
			if tt.opts != nil {
				c = New(tt.opts...)
			}
			c.WithDebug(true)

			currentDst := cloneInitialDestState(t, tt.dstInitial)

			err := c.Copy(currentDst, tt.src)

			if tt.expectErr != "" {
				if err == nil {
					t.Fatalf("expected error containing '%s', got nil", tt.expectErr)
				}
				unwrappedErr := errors.Unwrap(err)
				if unwrappedErr == nil {
					unwrappedErr = err
				}
				if !strings.Contains(unwrappedErr.Error(), tt.expectErr) {
					t.Fatalf("expected error containing '%s', got: %v (unwrapped: %v)", tt.expectErr, err, unwrappedErr)
				}
				// If error expected, and it matches, assert destination state if needed (especially for collectErrors)
				// For now, just return if error matches.
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
		dstInitial   interface{} // For map dest: *map[int]Hello; For slice dest: *[]Hello
		opts         []Option
		expectDst    interface{} // Actual map or slice value for comparison
		expectErr    string
		isSliceToMap bool
	}{
		{
			name:         "Slice to map with sensitive",
			src:          []Hello{"hello1", "hello2"},
			dstInitial:   func() *map[int]Hello { m := make(map[int]Hello); return &m }(), // Initial empty map
			isSliceToMap: true,
			opts:         []Option{},
			expectDst: map[int]Hello{
				0: Hello(base64.StdEncoding.EncodeToString([]byte("hello1"))),
				1: Hello(base64.StdEncoding.EncodeToString([]byte("hello2"))),
			},
		},
		{
			name:         "Map to slice with sensitive",
			src:          map[string]Hello{"keyA": "val1", "keyB": "val2"},       // Keys don't matter for values
			dstInitial:   func() *[]Hello { s := make([]Hello, 0); return &s }(), // Initial empty slice
			isSliceToMap: false,
			opts:         []Option{},
			expectDst: []Hello{ // Order might vary for map iteration, so test needs to be robust
				Hello(base64.StdEncoding.EncodeToString([]byte("val1"))),
				Hello(base64.StdEncoding.EncodeToString([]byte("val2"))),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCopier()
			if tt.opts != nil {
				c = New(tt.opts...)
			}
			c.WithDebug(false)

			// dstInitial is already a pointer to map or slice from the test table
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

			// Dereference pointer to get actual map/slice for comparison
			var actualDstValue interface{}
			if tt.isSliceToMap {
				actualDstValue = *(currentProcessDest.(*map[int]Hello))
			} else {
				actualDstValue = *(currentProcessDest.(*[]Hello))
				// For map to slice, sort both actual and expected if order isn't guaranteed
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
	// Note: Cloning initial state for channels is tricky if they contain data.
	// c.Copy for channels sends from src to dest.
	// For these tests, dstInitial is usually an empty channel.
	tests := []struct {
		name          string
		srcFn         func() chan Hello // Function to create and populate src channel
		dstInitialFn  func() chan Hello // Function to create dest channel
		opts          []Option
		expectDstRecv []Hello // Elements expected to be received from destination
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
			opts:          []Option{WithSkipSensitive(true)},
			expectDstRecv: []Hello{}, // No elements should be sent if skipped
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
			opts:         []Option{},
			expectDstRecv: []Hello{
				Hello(base64.StdEncoding.EncodeToString([]byte("hello1"))),
				Hello(base64.StdEncoding.EncodeToString([]byte("hello2"))),
			},
		},
		// Add test for Copiable on channel type itself if desired later
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCopier()
			if tt.opts != nil {
				c = New(tt.opts...)
			}
			c.WithDebug(true)

			srcChan := tt.srcFn()
			dstChan := tt.dstInitialFn()

			// c.Copy for channels expects channel values, not pointers to them
			err := c.Copy(dstChan, srcChan)
			// Ensure destination channel is closed after copy to terminate range loop
			// This is important if copyChan doesn't close it and src was closed.
			// However, copyChan reads with TryRecv, so it stops when src is exhausted/closed.
			// The receiver of dstChan needs to know when to stop.
			// If c.Copy guarantees dstChan is closed if srcChan is, then this explicit close is not needed.
			// For testing, it's safer to close it after the copy operation if we are ranging over it.
			// Let's assume copyChan doesn't close dest.
			// If copy operation itself could be short-circuited by an error, closing might be an issue.
			// For now, we'll collect and then close.

			if tt.expectErr != "" {
				if err == nil {
					t.Fatalf("expected error containing '%s', got nil", tt.expectErr)
				}
				if !strings.Contains(err.Error(), tt.expectErr) {
					t.Fatalf("expected error containing '%s', got: %v", tt.expectErr, err)
				}
				// It's hard to check channel content reliably after an error during copy.
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			close(dstChan) // Close dstChan to allow ranging to terminate

			var receivedElements []Hello
			for elem := range dstChan {
				receivedElements = append(receivedElements, elem)
			}

			assertDeepEqual(t, receivedElements, tt.expectDstRecv, "Channel sensitive test failed on received elements")
		})
	}
}
