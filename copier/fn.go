package copier

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
)

// ParseTagOptions parses struct tag options into a map of option names to values.
// The first part is treated as the field name unless it's a known flag (e.g., "omitempty", "sensitive").
// Subsequent parts are treated as flags. The "-" flag removes any parsed name.
// Returns a map of options where flags have empty string values.
func ParseTagOptions(tag string) map[string]string {
	opts := make(map[string]string)
	parts := strings.Split(tag, ",")
	knownFlags := map[string]bool{
		"omitempty": true,
		"sensitive": true,
		"unmapped":  true,
		"-":         true,
	}
	if len(parts) > 0 {
		part0 := strings.TrimSpace(parts[0])
		if part0 != "" {
			if _, isFlag := knownFlags[part0]; isFlag {
				opts[part0] = ""
			} else {
				opts["name"] = part0
			}
		}
		for _, opt := range parts[1:] {
			trimmedOpt := strings.TrimSpace(opt)
			if trimmedOpt != "" {
				opts[trimmedOpt] = ""
			}
		}
	}
	if _, ignorePresent := opts["-"]; ignorePresent {
		delete(opts, "name")
	}
	return opts
}

// IsMemCopySuitable checks if a type is suitable for memory copying.
// Returns true for basic types (bool, numbers), arrays of suitable types, and structs without pointers.
func IsMemCopySuitable(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128:
		return true
	case reflect.Array:
		return IsMemCopySuitable(typ.Elem())
	case reflect.Struct:
		return !HasPointers(typ)
	default:
		return false
	}
}

// HasPointers checks if a type contains pointers or reference types.
// Returns true for pointers, maps, slices, channels, functions, interfaces, strings, or arrays/structs containing them.
func HasPointers(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Ptr, reflect.UnsafePointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface, reflect.String:
		return true
	case reflect.Array:
		return HasPointers(typ.Elem())
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			if HasPointers(typ.Field(i).Type) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// IsEmptyValue checks if a value is considered empty, differing from IsZero for some types.
// Returns true for invalid values, nil pointers/interfaces, empty collections, or structs like bytes.Buffer with zero length.
// Basic types (bool, numbers) are never empty, only zero. Structs use sql.NullXXX pattern or IsZero as fallback.
func IsEmptyValue(v reflect.Value) bool {
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
		return false
	case reflect.Struct:
		if v.CanAddr() {
			addrVal := v.Addr()
			if addrVal.CanInterface() {
				switch ptr := addrVal.Interface().(type) {
				case *bytes.Buffer:
					return ptr.Len() == 0
				case *strings.Builder:
					return ptr.Len() == 0
				}
			}
		}
		validField := v.FieldByName("Valid")
		if validField.IsValid() && validField.Kind() == reflect.Bool {
			return !validField.Bool()
		}
		return v.IsZero()
	default:
		return v.IsZero()
	}
}

// IsNilable checks if a value can be nil.
// Returns true for invalid values, channels, functions, interfaces, maps, pointers, or slices.
func IsNilable(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return true
	default:
		return false
	}
}

// IsEmptyValueForOmit checks if a value is empty according to Go's `omitempty` logic.
// Returns true for invalid values, nil pointers/interfaces, empty collections, zero basic types, or structs with sql.NullXXX pattern or IsZero.
func IsEmptyValueForOmit(v reflect.Value) bool {
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
	case reflect.Interface, reflect.Ptr, reflect.Chan, reflect.Func:
		return v.IsNil()
	case reflect.Struct:
		validField := v.FieldByName("Valid")
		if validField.IsValid() && validField.Kind() == reflect.Bool && validField.CanInterface() {
			return !validField.Bool()
		}
		return v.IsZero()
	default:
		return v.IsZero()
	}
}

// GetNilLikeValue returns a nil-like value for the given type.
// For nilable types (pointers, interfaces, slices, maps, channels, functions), returns a typed nil.
// For other types, returns an invalid reflect.Value to indicate absence.
func GetNilLikeValue(originalVal reflect.Value) reflect.Value {
	if !originalVal.IsValid() {
		return reflect.Value{}
	}
	t := originalVal.Type()
	if t.Kind() == reflect.Ptr || t.Kind() == reflect.Interface || t.Kind() == reflect.Slice ||
		t.Kind() == reflect.Map || t.Kind() == reflect.Chan || t.Kind() == reflect.Func {
		return reflect.Zero(t)
	}
	return reflect.Value{}
}

// IsBasicTypeZero checks if a basic scalar type (bool, numbers, string) is zero.
// Returns true for invalid values or zero values of basic types.
func IsBasicTypeZero(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.String:
		return v.IsZero()
	default:
		return false
	}
}

// InterfaceKeySrc retrieves a map key's interface value for logging.
// Returns a string representation for unexported or invalid keys.
func InterfaceKeySrc(key reflect.Value) interface{} {
	if !key.IsValid() {
		return "<invalid_key>"
	}
	if key.CanInterface() {
		return key.Interface()
	}
	return fmt.Sprintf("<uninterfaceable_key_type_%s>", key.Type())
}

// InterfaceToString converts a map key's reflect.Value to a string for logging or lookup.
// Uses a direct conversion for string keys (the common case) and fmt.Sprintf otherwise.
func InterfaceToString(keyVal reflect.Value) string {
	if !keyVal.IsValid() {
		return "<invalid_key_value>"
	}
	if keyVal.Kind() == reflect.String {
		return keyVal.String()
	}
	if keyVal.CanInterface() {
		return fmt.Sprintf("%v", keyVal.Interface())
	}
	return fmt.Sprintf("<uninterfaceable_key_%s>", keyVal.Type())
}
