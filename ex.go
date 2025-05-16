package mm

import "github.com/olekukonko/mm/ex"

func ExtractToMap(i any, o ex.Options) (map[string]any, []ex.Field, error) {
	return ex.New[any](o).Extract(i)
}

func ExtractToStringMap(i any, o ex.Options) (map[string]string, []ex.Field, error) {
	return ex.New[string](o).Extract(i)
}

func ExtractWithNames(i any, fN []string, o ex.Options) (map[string]any, []ex.Field, error) {
	o.IncludeOnly = fN
	o.StrictMode = true
	return ex.New[any](o).Extract(i)
}
