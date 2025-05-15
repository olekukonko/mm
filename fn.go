// fn.go
package mm

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
)

// parseTagOptions correctly parses struct tag options.
// Handles name as the first part unless it's a known flag.
// Subsequent parts are treated as flags (e.g., "omitempty", "sensitive").
func parseTagOptions(tag string) map[string]string {
	opts := make(map[string]string)
	parts := strings.Split(tag, ",")
	knownFlags := map[string]bool{ // Define known flags here
		"omitempty": true,
		"sensitive": true,
		"unmapped":  true,
		"-":         true,
	}

	if len(parts) > 0 {
		// Handle the first part
		part0 := strings.TrimSpace(parts[0])
		if part0 != "" {
			if _, isFlag := knownFlags[part0]; isFlag {
				// First part is a flag (e.g., `mm:"sensitive,omitempty"`)
				opts[part0] = "" // Store with empty value to indicate presence of flag
			} else {
				// First part is the name (e.g., `mm:"newName,omitempty"`)
				opts["name"] = part0
			}
		} // If empty first part (e.g., `,omitempty`), ignore it

		// Handle subsequent parts (always flags)
		for _, opt := range parts[1:] {
			trimmedOpt := strings.TrimSpace(opt)
			if trimmedOpt != "" {
				// Store flag options with an empty string value to indicate presence
				opts[trimmedOpt] = ""
			}
		}
	}

	// If ignore tag "-" is present, ensure name is removed if it was also parsed.
	if _, ignorePresent := opts["-"]; ignorePresent {
		delete(opts, "name")
	}

	return opts
}

func isMemCopySuitable(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128:
		return true
	case reflect.Array:
		return isMemCopySuitable(typ.Elem())
	case reflect.Struct:
		return !checkContainsPointers(typ)
	default:
		return false
	}
}
func checkContainsPointers(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Ptr, reflect.UnsafePointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface, reflect.String:
		return true
	case reflect.Array:
		return checkContainsPointers(typ.Elem())
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			if checkContainsPointers(typ.Field(i).Type) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// isEmptyValue checks if a reflect.Value is considered empty.
// This definition differs slightly from IsZero() for certain types.
// Interfaces and pointers are empty if nil.
// Slices, maps, strings, arrays are empty if their length is 0.
// For structs like bytes.Buffer, strings.Builder, it checks Len() if possible.
// Other structs check sql.NullXXX pattern or fall back to IsZero().
// Basic types (bool, numbers) are *never* considered empty by this definition, only zero.
func isEmptyValue(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Interface, reflect.Ptr, reflect.Chan, reflect.Func:
		return v.IsNil()
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128:
		return false // Numeric and bool types are not "empty", only potentially "zero"
	case reflect.Struct:
		// Handle common types with Len() method that have pointer receivers.
		if v.CanAddr() {
			addrVal := v.Addr()
			if addrVal.CanInterface() { // Check if *T can be interfaced
				switch ptr := addrVal.Interface().(type) {
				case *bytes.Buffer:
					return ptr.Len() == 0
				case *strings.Builder:
					return ptr.Len() == 0
				}
			}
		}
		// If not addressable or not one of the above, try sql.NullXXX pattern
		validField := v.FieldByName("Valid")
		if validField.IsValid() && validField.Kind() == reflect.Bool {
			return !validField.Bool()
		}
		// Fallback for other structs: empty if zero.
		// This applies if v.CanAddr() was false for Buffer/Builder,
		// or if it's another struct type.
		return v.IsZero()
	default:
		// For any other kinds not explicitly handled, consider empty if zero.
		return v.IsZero()
	}
}

// srcValInterface is a helper for logging, attempts to get the interface value.
func srcValInterface(val reflect.Value) interface{} {
	if !val.IsValid() {
		return "<invalid_val>"
	}
	if val.CanInterface() {
		return val.Interface()
	}
	// For unexported fields, val.Interface() panics.
	// Try to show type and that it's unexported/uninterfaceable.
	// Using Addr() might also panic if not addressable.
	return fmt.Sprintf("<uninterfaceable %s>", val.Type())
}

func isNilable(v reflect.Value) bool {
	if !v.IsValid() {
		return true // Invalid value can be considered nil-like for skipNilSrc
	}
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return true
	}
	return false
}

// getSensitivity tries to get the Sensitivity interface implementation from a value.
func getSensitivity(v reflect.Value) (Sensitivity, bool) {
	if !v.IsValid() {
		return nil, false
	}

	// Check direct value (for value receivers or if v is already an interface holding Sensitivity)
	if v.CanInterface() {
		if s, ok := v.Interface().(Sensitivity); ok {
			return s, true
		}
	}

	// Check pointer to value (for pointer receivers)
	if v.CanAddr() {
		addrVal := v.Addr()
		if addrVal.CanInterface() {
			if s, ok := addrVal.Interface().(Sensitivity); ok {
				return s, true
			}
		}
	}

	// Type-based check (if T or *T implements Sensitivity)
	// This is useful if the value 'v' itself isn't interfaceable directly
	// but its type definition implements the interface.
	// We create a zero value of T or *T to call the method.
	valueType := v.Type()
	ptrType := reflect.PtrTo(valueType)

	// Check T
	if valueType.Implements(reflect.TypeOf((*Sensitivity)(nil)).Elem()) {
		// Create a zero value of T to call Sensitive()
		// This requires T to be constructible this way.
		// reflect.New(valueType).Elem() gives T
		zeroT := reflect.New(valueType).Elem()
		if zeroT.CanInterface() {
			if s, ok := zeroT.Interface().(Sensitivity); ok {
				return s, true
			}
		}
	}
	// Check *T
	if ptrType.Implements(reflect.TypeOf((*Sensitivity)(nil)).Elem()) {
		// Create a zero value of *T to call Sensitive()
		// reflect.New(valueType) gives *T
		zeroPtrT := reflect.New(valueType)
		if zeroPtrT.CanInterface() {
			if s, ok := zeroPtrT.Interface().(Sensitivity); ok {
				return s, true
			}
		}
	}

	return nil, false
}

// isEmptyValueForOmit checks if a value is empty according to Go's `omitempty` logic.
func isEmptyValueForOmit(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Complex64, reflect.Complex128:
		return v.Complex() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	case reflect.Chan, reflect.Func: // Added Chan, Func
		return v.IsNil()
	case reflect.Struct:
		// Special case for sql.NullXXX or similar patterns
		// This should be robust enough even if Valid field is unexported,
		// as FieldByName still returns it, just CanInterface might be false.
		// IsZero below will handle unexported Valid field correctly if it's zero.
		validField := v.FieldByName("Valid")
		if validField.IsValid() && validField.Kind() == reflect.Bool {
			// We must be able to read the bool
			if validField.CanInterface() { // Or some other way to get bool if unexported
				return !validField.Bool()
			}
			// If Valid field is unexported and we can't read it, fallback to IsZero() for the struct.
			// This is safer than assuming.
		}
		// Standard omitempty for struct checks IsZero()
		return v.IsZero()
	default:
		// Fallback: Check IsZero for any other kinds
		return v.IsZero()
	}
}

// getNilLikeValue returns a typed nil or an invalid Value depending on the original value's type.
func getNilLikeValue(originalVal reflect.Value) reflect.Value {
	if !originalVal.IsValid() {
		return reflect.Value{} // Return invalid Value
	}
	t := originalVal.Type()
	k := t.Kind()
	// For kinds that can be nil, return a typed nil.
	if k == reflect.Ptr || k == reflect.Interface || k == reflect.Slice || k == reflect.Map || k == reflect.Chan || k == reflect.Func {
		return reflect.Zero(t)
	}
	// For other types, returning Zero might be confusing (e.g., 0 for int).
	// Returning an invalid Value is safer to signal "nil" specifically if the intent is to represent absence.
	// However, for processItem, if Copiable returns nil, we usually want to set the zero value of dest type.
	// This helper is more about what "nil" means for source-side checks or formatter returns.
	return reflect.Value{} // Represent "absence" rather than typed zero for non-nilable types.
}

// isBasicTypeZero checks IsZero only for basic scalar types (bool, numbers, string).
func isBasicTypeZero(v reflect.Value) bool {
	if !v.IsValid() {
		return true // Invalid value is considered zero-like for overwrite checks
	}
	switch v.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.String:
		return v.IsZero()
	}
	return false // Composite types (structs, slices, maps etc.) are not "basic types" here
}

// srcKeyInterface robustly gets interface from map key for logging.
func srcKeyInterface(key reflect.Value) interface{} {
	if !key.IsValid() {
		return "<invalid_key>"
	}
	if key.CanInterface() {
		return key.Interface()
	}
	// Fallback for unexported key types (less common for map keys)
	return fmt.Sprintf("<uninterfaceable_key_type_%s>", key.Type())
}

// srcKeyValToString converts a map key reflect.Value to a string for logging/lookup.
func srcKeyValToString(keyVal reflect.Value) string {
	if !keyVal.IsValid() {
		return "<invalid_key_value>"
	}
	if keyVal.CanInterface() {
		// Use fmt.Sprintf for types that might have custom String() method
		return fmt.Sprintf("%v", keyVal.Interface())
	}
	// Fallback for uninterfaceable keys
	return fmt.Sprintf("<uninterfaceable_key_%s>", keyVal.Type())
}
