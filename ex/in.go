package ex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Inspector configures the inspection output
type Inspector struct {
	ShowMap      bool     // Whether to show the result map
	ShowFields   bool     // Whether to show field info
	ShowJSON     bool     // Whether to show JSON output
	Indent       string   // Indentation string for JSON output
	FieldFilters []string // Fields to include (empty means all)
}

// Inspectorate provides sensible defaults
var Inspectorate = Inspector{
	ShowMap:    true,
	ShowFields: true,
	ShowJSON:   false,
	Indent:     "    ",
}

// Inspect provides a human-readable inspection of the extraction results
func Inspect(resultMap map[string]any, fieldInfos []Field, opts ...Inspector) string {
	var opt Inspector
	if len(opts) > 0 {
		opt = opts[0]
	} else {
		opt = Inspectorate
	}

	var buf bytes.Buffer

	// Filter fieldInfos if needed
	filteredInfos := fieldInfos
	if len(opt.FieldFilters) > 0 {
		filteredInfos = make([]Field, 0, len(fieldInfos))
		fieldSet := make(map[string]struct{})
		for _, f := range opt.FieldFilters {
			fieldSet[f] = struct{}{}
		}

		for _, info := range fieldInfos {
			if _, ok := fieldSet[info.Name]; ok {
				filteredInfos = append(filteredInfos, info)
			}
		}
	}

	// Result Map output
	if opt.ShowMap && resultMap != nil {
		buf.WriteString("Result Map:\n")
		for k, v := range resultMap {
			buf.WriteString(fmt.Sprintf("  %s: %v (%s)\n", k, v, reflect.TypeOf(v)))
		}
		buf.WriteString("\n")
	}

	// Field Info output
	if opt.ShowFields && len(filteredInfos) > 0 {
		buf.WriteString("Field Info:\n")
		for _, info := range filteredInfos {
			buf.WriteString(fmt.Sprintf("  Name: %s\n", info.Name))
			buf.WriteString(fmt.Sprintf("  Value: %v\n", info.Value))
			buf.WriteString(fmt.Sprintf("  Type: %s\n", info.Type.String()))
			buf.WriteString(fmt.Sprintf("  Tag: %s\n", info.Tag))
			buf.WriteString(fmt.Sprintf("  Omitted: %v\n", info.Omitted))
			buf.WriteString(fmt.Sprintf("  Exported: %v\n", info.Exported))
			buf.WriteString("  ----\n")
		}
		buf.WriteString("\n")
	}

	// JSON output
	if opt.ShowJSON && len(filteredInfos) > 0 {
		buf.WriteString("Field Info (JSON):\n")
		xb, err := json.Marshal(filteredInfos)
		if err != nil {
			buf.WriteString(fmt.Sprintf("  JSON Error: %v\n", err))
		} else {
			var prettyJSON bytes.Buffer
			json.Indent(&prettyJSON, xb, "", opt.Indent)
			buf.WriteString(prettyJSON.String())
			buf.WriteString("\n")
		}
	}

	return strings.TrimSpace(buf.String())
}
