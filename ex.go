package mm

import "github.com/olekukonko/mm/ex"

// Extract extracts fields from the input value into a map with string keys and any values.
// Returns the extracted map, a slice of processed fields, and an error if extraction fails.
func Extract(i any) ([]ex.Field, error) {
	_, fields, err := ex.New[any](ex.Options{}).Extract(i)
	return fields, err
}

// ExtractToMap extracts fields from the input value into a map with string keys and any values.
// Returns the extracted map, a slice of processed fields, and an error if extraction fails.
func ExtractToMap(i any, o ex.Options) (map[string]any, []ex.Field, error) {
	return ex.New[any](o).Extract(i)
}

// ExtractToString extracts fields from the input value into a map with string keys and string values.
// Returns the extracted map, a slice of processed fields, and an error if extraction fails.
func ExtractToString(i any, o ex.Options) (map[string]string, []ex.Field, error) {
	return ex.New[string](o).Extract(i)
}

// ExtractWithNames extracts specified fields from the input value into a map with string keys and any values.
// Only fields listed in fieldNames are included, and strict mode is enabled to enforce this restriction.
// Returns the extracted map, a slice of processed fields, and an error if extraction fails.
func ExtractWithNames(i any, fieldNames []string, o ex.Options) (map[string]any, []ex.Field, error) {
	o.IncludeOnly = fieldNames
	o.StrictMode = true
	return ex.New[any](o).Extract(i)
}
