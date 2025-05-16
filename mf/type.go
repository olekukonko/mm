package mf

import (
	"fmt"
	"reflect"
	"strings"
)

func NameType(v any) string {
	t := reflect.TypeOf(v)
	if t == nil {
		return "nil"
	}

	// Handle pointers
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// First try to get the simple name
	if name := t.Name(); name != "" && !strings.Contains(name, "[") {
		return name
	}

	// For complex types, parse the string representation
	typeStr := t.String()

	// Special handling for generic/adapter types
	if bracketStart := strings.Index(typeStr, "["); bracketStart > 0 {
		// Extract content inside brackets
		bracketEnd := strings.LastIndex(typeStr, "]")
		if bracketEnd > bracketStart {
			innerType := typeStr[bracketStart+1 : bracketEnd]

			// Extract the last part after slash
			if lastSlash := strings.LastIndex(innerType, "/"); lastSlash >= 0 {
				innerType = innerType[lastSlash+1:]
			}

			// Remove any remaining brackets (for nested cases)
			innerType = strings.Split(innerType, "]")[0]
			return innerType
		}
	}

	// Fallback for non-generic types
	parts := strings.Split(typeStr, ".")
	lastPart := parts[len(parts)-1]
	if idx := strings.IndexAny(lastPart, "[]"); idx != -1 {
		lastPart = lastPart[:idx]
	}
	return lastPart
}

func NameField(v any, field string) (string, error) {
	// Handle nil input
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

	// Check if struct
	if rv.Kind() != reflect.Struct {
		return "", fmt.Errorf("not a struct")
	}

	// Find field
	f := rv.FieldByName(field)
	if !f.IsValid() {
		return "", fmt.Errorf("field %q not found", field)
	}

	// Handle string conversion
	if f.CanInterface() {
		if s, ok := f.Interface().(fmt.Stringer); ok {
			return s.String(), nil
		}
	}

	return fmt.Sprintf("%v", f.Interface()), nil
}
