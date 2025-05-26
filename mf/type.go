package mf

import (
	"fmt"
	"reflect"
	"strings"
)

// NameType returns the simplified type name of the input value.
// For pointers, it returns the name of the element type.
// For generic or complex types, it extracts the innermost type name.
// Returns "nil" for nil values.
func NameType(v any) string {
	t := reflect.TypeOf(v)
	if t == nil {
		return "nil"
	}

	// Use element type for pointers
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Return simple type name if available and not an array/slice
	if name := t.Name(); name != "" && !strings.Contains(name, "[") {
		return name
	}

	// Handle complex types
	typeStr := t.String()

	// Extract inner type for generic types
	if bracketStart := strings.Index(typeStr, "["); bracketStart > 0 {
		bracketEnd := strings.LastIndex(typeStr, "]")
		if bracketEnd > bracketStart {
			innerType := typeStr[bracketStart+1 : bracketEnd]
			// Use last part after slash for package-qualified types
			if lastSlash := strings.LastIndex(innerType, "/"); lastSlash >= 0 {
				innerType = innerType[lastSlash+1:]
			}
			// Remove nested brackets
			innerType = strings.Split(innerType, "]")[0]
			return innerType
		}
	}

	// Fallback: use last part of type string, excluding array/slice indicators
	parts := strings.Split(typeStr, ".")
	lastPart := parts[len(parts)-1]
	if idx := strings.IndexAny(lastPart, "[]"); idx != -1 {
		lastPart = lastPart[:idx]
	}
	return lastPart
}

// NameField returns the string representation of a struct field's value.
// For fields implementing fmt.Stringer, it uses the String() method.
// Returns an error for nil values, nil pointers, non-struct types, or invalid fields.
func NameField(v any, field string) (string, error) {
	if v == nil {
		return "", fmt.Errorf("nil value")
	}

	rv := reflect.ValueOf(v)

	// Dereference pointers
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return "", fmt.Errorf("nil pointer")
		}
		rv = rv.Elem()
	}

	// Verify input is a struct
	if rv.Kind() != reflect.Struct {
		return "", fmt.Errorf("not a struct")
	}

	// Look up field by name
	f := rv.FieldByName(field)
	if !f.IsValid() {
		return "", fmt.Errorf("field %q not found", field)
	}

	// Use String() for fmt.Stringer types
	if f.CanInterface() {
		if s, ok := f.Interface().(fmt.Stringer); ok {
			return s.String(), nil
		}
	}

	// Fallback to default string formatting
	return fmt.Sprintf("%v", f.Interface()), nil
}
