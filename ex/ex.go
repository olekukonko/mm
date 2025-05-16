package ex

import (
	"database/sql"
	"database/sql/driver"
	"encoding"
	"errors"
	"fmt"
	"github.com/olekukonko/mm/mf"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	fieldCache sync.Map

	TruePtr  = new(bool)
	FalsePtr = new(bool)
)

// Options defines configuration for field extraction.
type Options struct {
	Tag             string
	TagBackup       []string
	IncludeOnly     []string
	ExcludeFields   []string
	SkipNil         bool
	UseJSONFallback bool
	StrictMode      bool
	UseFieldName    *bool
	AllowZeroValues bool
	AllowSQLNull    bool
	FieldSorter     func([]Field) []Field // Custom sorter for Field
}

func init() {
	*TruePtr = true
	*FalsePtr = false
}

// Field represents metadata about a field.
type Field struct {
	Name     string
	Value    any
	Tag      string
	Omitted  bool
	Exported bool
	Type     reflect.Type
}

// Meta holds metadata about a struct field.
type Meta struct {
	name        string
	tag         string
	tagNameUsed string
	index       []int
	fieldType   reflect.Type
	isPtr       bool
	embedded    bool
	exported    bool
	omitempty   bool
	omitzero    bool
}

// Extractor handles field extraction from structs or maps.
type Extractor[T any] struct {
	opts       Options
	includeSet map[string]struct{}
	excludeSet map[string]struct{}
}

// New creates a new Extractor with the given options.
func New[T any](opts Options) *Extractor[T] {
	if opts.Tag == "" {
		opts.Tag = "db"
	}
	if opts.UseFieldName == nil {
		useFieldNameDefault := true
		opts.UseFieldName = &useFieldNameDefault
	}
	e := &Extractor[T]{opts: opts}
	e.initFieldSets()
	return e
}

// initFieldSets initializes sets for included and excluded fields.
func (e *Extractor[T]) initFieldSets() {
	e.includeSet = make(map[string]struct{}, len(e.opts.IncludeOnly))
	e.excludeSet = make(map[string]struct{}, len(e.opts.ExcludeFields))
	for _, f := range e.opts.IncludeOnly {
		e.includeSet[f] = struct{}{}
	}
	for _, f := range e.opts.ExcludeFields {
		e.excludeSet[f] = struct{}{}
	}
}

// shouldProcessField checks if a field should be processed.
func (e *Extractor[T]) shouldProcessField(name string) bool {
	if _, excluded := e.excludeSet[name]; excluded {
		return false
	}
	if len(e.includeSet) > 0 {
		_, included := e.includeSet[name]
		return included
	}
	return true
}

// isIncluded checks if a field is explicitly included.
func (e *Extractor[T]) isIncluded(name string) bool {
	if len(e.includeSet) == 0 {
		return false
	}
	_, included := e.includeSet[name]
	return included
}

// Extract extracts fields from the input struct or map.
func (e *Extractor[T]) Extract(input any) (map[string]T, []Field, error) {
	if input == nil {
		return nil, nil, errors.New("cannot extract from nil input")
	}
	rv := reflect.ValueOf(input)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return make(map[string]T), []Field{}, nil
		}
		rv = rv.Elem()
	}
	var resultMap map[string]T
	var infos []Field
	var err error
	var missingRequired []string
	inputType := rv.Kind()
	switch inputType {
	case reflect.Struct:
		if _, ok := rv.Interface().(time.Time); ok {
			err = fmt.Errorf("cannot extract fields from time.Time directly")
		} else {
			resultMap, infos, missingRequired, err = e.extractStruct(rv, 0)
		}
	case reflect.Map:
		keyKind := rv.Type().Key().Kind()
		if keyKind != reflect.String {
			err = fmt.Errorf("map keys must be strings, found %s", keyKind)
		} else {
			resultMap, infos, missingRequired, err = e.extractMap(rv)
		}
	default:
		err = fmt.Errorf("unsupported input type: %T (kind: %s)", input, inputType)
		resultMap = make(map[string]T)
		infos = []Field{}
	}
	if err != nil {
		if resultMap == nil {
			resultMap = make(map[string]T)
		}
		if infos == nil {
			infos = []Field{}
		}
		return resultMap, infos, err
	}
	if e.opts.StrictMode && len(e.opts.IncludeOnly) > 0 {
		finalMissing := map[string]struct{}{}
		for _, name := range missingRequired {
			finalMissing[name] = struct{}{}
		}
		for reqField := range e.includeSet {
			if _, presentInResult := resultMap[reqField]; !presentInResult {
				if _, alreadyMarkedMissing := finalMissing[reqField]; !alreadyMarkedMissing {
					foundButOmittedOrExcluded := false
					for _, info := range infos {
						if info.Name == reqField {
							foundButOmittedOrExcluded = true
							break
						}
					}
					if !foundButOmittedOrExcluded {
						finalMissing[reqField] = struct{}{}
					} else {
						finalMissing[reqField] = struct{}{}
					}
				}
			}
		}
		if len(finalMissing) > 0 {
			missingList := make([]string, 0, len(finalMissing))
			for name := range finalMissing {
				missingList = append(missingList, name)
			}
			sort.Strings(missingList)
			errMsgFormat := "strict mode: required fields missing, excluded, or omitted: %s"
			if inputType == reflect.Map {
				errMsgFormat = "strict mode: required map keys missing/excluded: %s"
			} else {
				allNotFoundInStruct := true
				foundInInfos := make(map[string]bool)
				for _, info := range infos {
					foundInInfos[info.Name] = true
				}
				for name := range finalMissing {
					if _, wasProcessed := foundInInfos[name]; wasProcessed {
						allNotFoundInStruct = false
						break
					}
				}
				if allNotFoundInStruct {
					errMsgFormat = "strict mode: fields specified in IncludeOnly not found in struct definition: %s"
				}
			}
			err = fmt.Errorf(errMsgFormat, strings.Join(missingList, ", "))
			return resultMap, infos, err
		}
	}
	return resultMap, infos, err
}

// extractStruct extracts fields from a struct recursively.
func (e *Extractor[T]) extractStruct(rv reflect.Value, depth int) (map[string]T, []Field, []string, error) {
	if !rv.IsValid() || rv.Kind() != reflect.Struct {
		return nil, nil, nil, fmt.Errorf("internal error: invalid value: %v", rv)
	}
	rt := rv.Type()
	currentLevelFields, err := e.parseStruct(rt)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parsing fields for %s: %w", rt.String(), err)
	}
	resultMap := make(map[string]T)
	infoMap := make(map[string]Field)
	var missingRequired []string
	// Pass 1: Embedded
	for _, f := range currentLevelFields {
		if !f.embedded {
			continue
		}
		structFieldName := rt.FieldByIndex(f.index).Name
		fieldValue := rv.FieldByIndex(f.index)
		if !fieldValue.IsValid() {
			continue
		}
		if fieldValue.Kind() == reflect.Ptr {
			if fieldValue.IsNil() {
				continue
			}
			fieldValue = fieldValue.Elem()
		}
		if fieldValue.Kind() != reflect.Struct || fieldValue.Type() == rt {
			continue
		}
		embeddedResult, embeddedInfos, embeddedMissing, errEmbed := e.extractStruct(fieldValue, depth+1)
		if errEmbed != nil {
			return nil, nil, missingRequired, fmt.Errorf("processing embedded %q (%s): %w", structFieldName, fieldValue.Type().String(), errEmbed)
		}
		missingRequired = append(missingRequired, embeddedMissing...)
		for k, v := range embeddedResult {
			resultMap[k] = v
		}
		for _, info := range embeddedInfos {
			infoMap[info.Name] = info
		}
	}
	// Pass 2: Regular
	for _, f := range currentLevelFields {
		if f.embedded {
			continue
		}
		structFieldName := rt.FieldByIndex(f.index).Name
		finalFieldName := f.name
		isIncludedRequired := e.opts.StrictMode && e.isIncluded(finalFieldName)
		shouldProcess := e.shouldProcessField(finalFieldName)
		if !shouldProcess {
			if isIncludedRequired {
				missingRequired = append(missingRequired, finalFieldName)
			}
			infoMap[finalFieldName] = Field{Name: finalFieldName, Tag: f.tag, Exported: f.exported, Omitted: true, Type: f.fieldType}
			delete(resultMap, finalFieldName)
			continue
		}
		fieldValue := rv.FieldByIndex(f.index)
		if !fieldValue.IsValid() {
			if isIncludedRequired {
				missingRequired = append(missingRequired, finalFieldName)
			}
			infoMap[finalFieldName] = Field{Name: finalFieldName, Tag: f.tag, Exported: f.exported, Omitted: true, Type: f.fieldType}
			delete(resultMap, finalFieldName)
			continue
		}
		processedValue, errProcess := e.processValue(fieldValue)
		if errProcess != nil {
			return nil, nil, missingRequired, fmt.Errorf("processing value for %s (%s): %w", finalFieldName, structFieldName, errProcess)
		}
		shouldOmit, errOmit := e.shouldOmit(processedValue, fieldValue, f)
		if errOmit != nil {
			return nil, nil, missingRequired, fmt.Errorf("omit check for %s (%s): %w", finalFieldName, structFieldName, errOmit)
		}
		info := Field{Name: finalFieldName, Value: processedValue, Tag: f.tag, Omitted: shouldOmit, Exported: f.exported, Type: f.fieldType}
		infoMap[finalFieldName] = info
		if shouldOmit {
			if isIncludedRequired {
				missingRequired = append(missingRequired, finalFieldName)
			}
			delete(resultMap, finalFieldName)
		} else {
			var zeroT T
			tVal, errConvert := e.convertToT(processedValue)
			if errConvert != nil {
				return nil, nil, missingRequired, fmt.Errorf("converting field %s (%s) (PVT %T) to %T: %w", finalFieldName, structFieldName, processedValue, zeroT, errConvert)
			}
			resultMap[finalFieldName] = tVal
		}
	}
	// Collect Field for all fields (embedded and non-embedded)
	finalInfos := make([]Field, 0, len(infoMap))
	// First, include embedded fields from infoMap (populated in Pass 1)
	for _, info := range infoMap {
		// Only include embedded fields not overridden by non-embedded fields
		isOverridden := false
		for _, f := range currentLevelFields {
			if !f.embedded && f.name == info.Name {
				isOverridden = true
				break
			}
		}
		if !isOverridden {
			finalInfos = append(finalInfos, info)
		}
	}
	// Then, append non-embedded fields in struct order
	for _, f := range currentLevelFields {
		if !f.embedded {
			if info, exists := infoMap[f.name]; exists {
				finalInfos = append(finalInfos, info)
			}
		}
	}
	// Apply custom sorter if provided
	if e.opts.FieldSorter != nil {
		finalInfos = e.opts.FieldSorter(finalInfos)
	}
	return resultMap, finalInfos, mf.UniqueSorted(missingRequired), nil
}

// extractMap extracts fields from a map.
func (e *Extractor[T]) extractMap(rv reflect.Value) (map[string]T, []Field, []string, error) {
	if rv.IsNil() {
		return make(map[string]T), []Field{}, nil, nil
	}
	result := make(map[string]T, rv.Len())
	var infos []Field
	var missingRequired []string
	foundKeys := make(map[string]struct{})
	mapIter := rv.MapRange()
	for mapIter.Next() {
		key, value := mapIter.Key(), mapIter.Value()
		fieldName := key.String()
		foundKeys[fieldName] = struct{}{}
		originalValueType := value.Type()
		isIncludedRequired := e.opts.StrictMode && e.isIncluded(fieldName)
		if !e.shouldProcessField(fieldName) {
			if isIncludedRequired {
				missingRequired = append(missingRequired, fieldName)
			}
			info := Field{Name: fieldName, Value: nil, Omitted: true, Exported: true, Type: originalValueType}
			infos = append(infos, info)
			continue
		}
		processedValue, errProcess := e.processValue(value)
		if errProcess != nil {
			return nil, infos, nil, fmt.Errorf("processing value for map key %q: %w", fieldName, errProcess)
		}
		shouldOmit, errOmit := e.shouldOmit(processedValue, value, Meta{})
		if errOmit != nil {
			return nil, infos, nil, fmt.Errorf("omit check for map key %q: %w", fieldName, errOmit)
		}
		info := Field{Name: fieldName, Value: processedValue, Omitted: shouldOmit, Exported: true, Type: originalValueType}
		infos = append(infos, info)
		if shouldOmit {
			if isIncludedRequired {
				missingRequired = append(missingRequired, fieldName)
			}
			continue
		}
		var zeroT T
		tVal, errConvert := e.convertToT(processedValue)
		if errConvert != nil {
			return nil, infos, nil, fmt.Errorf("converting value for map key %q (PVT %T) to %T: %w", fieldName, processedValue, zeroT, errConvert)
		}
		result[fieldName] = tVal
	}
	if e.opts.StrictMode && len(e.includeSet) > 0 {
		for req := range e.includeSet {
			if _, found := foundKeys[req]; !found {
				alreadyMarked := false
				for _, missing := range missingRequired {
					if missing == req {
						alreadyMarked = true
						break
					}
				}
				if !alreadyMarked {
					missingRequired = append(missingRequired, req)
				}
				info := Field{Name: req, Value: nil, Omitted: true, Exported: true, Type: nil}
				infos = append(infos, info)
			}
		}
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return result, infos, mf.UniqueSorted(missingRequired), nil
}

// parseStruct parses fields at the current struct level.
func (e *Extractor[T]) parseStruct(rt reflect.Type) ([]Meta, error) {
	cacheKey := buildCacheKey(rt, e.opts)
	if cached, ok := fieldCache.Load(cacheKey); ok {
		if fields, okCast := cached.([]Meta); okCast {
			return fields, nil
		}
		fieldCache.Delete(cacheKey)
	}
	var fields []Meta
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if mf.IsSkippedField(field, e.opts.Tag) {
			continue
		}
		if !field.IsExported() && !field.Anonymous {
			continue
		}
		meta, err := e.parseFieldMeta(field, []int{i})
		if err != nil {
			return nil, fmt.Errorf("parsing field %s.%s: %w", rt.Name(), field.Name, err)
		}
		if meta != nil {
			fields = append(fields, *meta)
		}
	}
	fieldCache.Store(cacheKey, fields)
	return fields, nil
}

// parseFieldMeta parses metadata for a struct field.
func (e *Extractor[T]) parseFieldMeta(field reflect.StructField, index []int) (*Meta, error) {
	if mf.IsSkippedField(field, e.opts.Tag) {
		return nil, nil
	}
	tagValue := ""
	tagNameUsed := ""
	primaryTagValue := field.Tag.Get(e.opts.Tag)
	if primaryTagValue != "" && primaryTagValue != "-" {
		tagValue = primaryTagValue
		tagNameUsed = e.opts.Tag
	}
	if tagNameUsed == "" {
		for _, backupName := range e.opts.TagBackup {
			if backupName == "" || backupName == e.opts.Tag {
				continue
			}
			backupTagValue := field.Tag.Get(backupName)
			if backupTagValue != "" && backupTagValue != "-" {
				tagValue = backupTagValue
				tagNameUsed = backupName
				break
			}
		}
	}
	if tagNameUsed == "" && e.opts.UseJSONFallback && e.opts.Tag != "json" {
		jsonTagValue := field.Tag.Get("json")
		if jsonTagValue != "" && jsonTagValue != "-" {
			tagValue = jsonTagValue
			tagNameUsed = "json"
		}
	}
	if tagValue == "-" || (tagNameUsed == "" && primaryTagValue == "-") {
		return nil, nil
	}
	parts := strings.Split(tagValue, ",")
	tagNameFromTag := strings.TrimSpace(parts[0])
	isEmbedded := false
	finalFieldName := ""
	useFieldNameSetting := e.opts.UseFieldName == nil || *e.opts.UseFieldName
	if field.Anonymous {
		if tagNameFromTag != "" {
			finalFieldName = tagNameFromTag
			isEmbedded = false
		} else {
			finalFieldName = field.Name
			isEmbedded = true
		}
	} else {
		if tagNameUsed != "" {
			if tagNameFromTag != "" {
				finalFieldName = tagNameFromTag
			} else {
				if useFieldNameSetting {
					finalFieldName = field.Name
				} else {
					return nil, nil
				}
			}
		} else {
			if useFieldNameSetting {
				if field.Tag == "" {
					finalFieldName = field.Name
				} else {
					return nil, nil
				}
			} else {
				return nil, nil
			}
		}
	}
	if finalFieldName == "" && !isEmbedded {
		return nil, nil
	}
	meta := &Meta{name: finalFieldName, tag: tagValue, tagNameUsed: tagNameUsed, index: index, fieldType: field.Type, isPtr: field.Type.Kind() == reflect.Ptr, embedded: isEmbedded, exported: field.IsExported()}
	if len(parts) > 1 {
		for _, opt := range parts[1:] {
			switch strings.TrimSpace(opt) {
			case "omitempty":
				meta.omitempty = true
			case "omitzero":
				meta.omitzero = true
			}
		}
	}
	return meta, nil
}

// shouldOmit determines if a field should be omitted based on its value and options.
func (e *Extractor[T]) shouldOmit(processedValue any, originalValue reflect.Value, f Meta) (bool, error) {
	// Include all fields if AllowZeroValues is true
	if e.opts.AllowZeroValues {
		return false, nil
	}

	// Include sql.Null* fields as nil if AllowSQLNull is true and SkipNil is false
	if e.opts.AllowSQLNull && !e.opts.SkipNil {
		switch originalValue.Interface().(type) {
		case sql.NullString, sql.NullFloat64, sql.NullInt64, sql.NullInt32, sql.NullInt16, sql.NullByte, sql.NullBool, sql.NullTime:
			return false, nil
		}
	}

	// Check for omit tags
	hasOmitTag := f.omitempty || f.omitzero
	if hasOmitTag {
		if f.omitempty && mf.IsEmptyInterface(processedValue, originalValue) {
			return true, nil
		}
		if f.omitzero && mf.IsZeroInterface(processedValue, originalValue) {
			return true, nil
		}
	}

	// Handle nil values when SkipNil is true
	if e.opts.SkipNil && (mf.IsNilInterface(processedValue) || mf.IsNilOrInvalid(originalValue)) {
		return true, nil
	}

	// Default to omitting empty fields if no omit tags
	return mf.IsEmptyInterface(processedValue, originalValue), nil
}

// processValue processes a reflect.Value into a suitable type.
func (e *Extractor[T]) processValue(v reflect.Value) (any, error) {
	if !v.IsValid() {
		return nil, nil
	}
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, nil
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return nil, nil
	}
	if v.CanInterface() {
		iValue := v.Interface()
		if t, ok := iValue.(time.Time); ok {
			return t, nil
		}
		if valuer, ok := iValue.(driver.Valuer); ok {
			val, err := valuer.Value()
			if err != nil {
				return nil, fmt.Errorf("driver.Valuer error on %T: %w", iValue, err)
			}
			return val, nil
		}
		if marshaler, ok := iValue.(encoding.TextMarshaler); ok {
			text, err := marshaler.MarshalText()
			if err != nil {
				return nil, fmt.Errorf("encoding.TextMarshaler error on %T: %w", iValue, err)
			}
			if text == nil {
				return nil, nil
			}
			return string(text), nil
		}
	}
	if v.CanAddr() {
		addr := v.Addr()
		if addr.IsValid() && addr.CanInterface() {
			addrIntf := addr.Interface()
			if valuer, ok := addrIntf.(driver.Valuer); ok {
				val, err := valuer.Value()
				if err != nil {
					return nil, fmt.Errorf("driver.Valuer error on pointer to %s: %w", v.Type().String(), err)
				}
				return val, nil
			}
			if marshaler, ok := addrIntf.(encoding.TextMarshaler); ok {
				text, err := marshaler.MarshalText()
				if err != nil {
					return nil, fmt.Errorf("encoding.TextMarshaler error on pointer to %s: %w", v.Type().String(), err)
				}
				if text == nil {
					return nil, nil
				}
				return string(text), nil
			}
		}
	}
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			if v.CanInterface() {
				return v.Interface(), nil
			}
			return nil, errors.New("internal error: cannot interface time.Time value")
		}
		tempOpts := e.opts
		tempExtractor := New[any](tempOpts)
		m, _, _, err := tempExtractor.extractStruct(v, 0)
		if err != nil {
			return nil, fmt.Errorf("error processing nested struct %s: %w", v.Type().String(), err)
		}
		return m, nil
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			if v.CanInterface() {
				return v.Interface(), nil
			}
			return nil, fmt.Errorf("cannot process map with non-string key type: %s", v.Type().Key())
		}
		return e.processMap(v)
	case reflect.Slice, reflect.Array:
		return e.processSlice(v)
	case reflect.String:
		return v.String(), nil
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Int:
		return int(v.Int()), nil
	case reflect.Int8:
		return int8(v.Int()), nil
	case reflect.Int16:
		return int16(v.Int()), nil
	case reflect.Int32:
		return int32(v.Int()), nil
	case reflect.Int64:
		return v.Int(), nil
	case reflect.Uint:
		return uint(v.Uint()), nil
	case reflect.Uint8:
		return uint8(v.Uint()), nil
	case reflect.Uint16:
		return uint16(v.Uint()), nil
	case reflect.Uint32:
		return uint32(v.Uint()), nil
	case reflect.Uint64:
		return v.Uint(), nil
	case reflect.Uintptr:
		return uintptr(v.Uint()), nil
	case reflect.Float32:
		return float32(v.Float()), nil
	case reflect.Float64:
		return v.Float(), nil
	case reflect.Complex64:
		return complex64(v.Complex()), nil
	case reflect.Complex128:
		return v.Complex(), nil
	default:
		if v.CanInterface() {
			return v.Interface(), nil
		}
		return nil, fmt.Errorf("cannot process value of unsupported kind %s (type %s)", v.Kind(), v.Type().String())
	}
}

// processMap processes a map value.
func (e *Extractor[T]) processMap(v reflect.Value) (map[string]any, error) {
	if v.Kind() != reflect.Map || v.Type().Key().Kind() != reflect.String {
		return nil, fmt.Errorf("internal error: processMap called with non-map[string] type: %s", v.Type())
	}
	if v.IsNil() {
		return map[string]any(nil), nil
	}
	result := make(map[string]any, v.Len())
	mapIter := v.MapRange()
	for mapIter.Next() {
		mapKey := mapIter.Key().String()
		mapValue := mapIter.Value()
		processedVal, err := e.processValue(mapValue)
		if err != nil {
			return nil, fmt.Errorf("error processing value for map key %q (type %s): %w", mapKey, mapValue.Type().String(), err)
		}
		result[mapKey] = processedVal
	}
	return result, nil
}

// processSlice processes a slice or array value.
func (e *Extractor[T]) processSlice(v reflect.Value) (any, error) {
	kind := v.Kind()
	if kind != reflect.Slice && kind != reflect.Array {
		return nil, fmt.Errorf("internal error: processSlice called with non-slice/array type: %s", v.Type())
	}
	if v.Type().Elem().Kind() == reflect.Uint8 {
		if kind == reflect.Slice {
			if v.IsNil() {
				return []byte(nil), nil
			}
			if v.Type() == reflect.TypeOf([]byte{}) && v.CanInterface() {
				return v.Bytes(), nil
			}
			byteSlice := make([]byte, v.Len())
			reflect.Copy(reflect.ValueOf(byteSlice), v)
			return byteSlice, nil
		} else {
			byteSlice := make([]byte, v.Len())
			if v.CanAddr() {
				reflect.Copy(reflect.ValueOf(byteSlice), v.Slice(0, v.Len()))
			} else {
				for i := 0; i < v.Len(); i++ {
					elem := v.Index(i)
					if elem.CanUint() {
						byteSlice[i] = byte(elem.Uint())
					} else {
						return nil, fmt.Errorf("internal error: cannot get uint8 value from array element at index %d", i)
					}
				}
			}
			return byteSlice, nil
		}
	}
	if kind == reflect.Slice && v.IsNil() {
		return []any(nil), nil
	}
	count := v.Len()
	result := make([]any, count)
	for i := 0; i < count; i++ {
		elemValue := v.Index(i)
		processedElem, err := e.processValue(elemValue)
		if err != nil {
			return nil, fmt.Errorf("error processing slice/array element at index %d (type %s): %w", i, elemValue.Type().String(), err)
		}
		result[i] = processedElem
	}
	return result, nil
}

// convertToT converts a value to type T.
func (e *Extractor[T]) convertToT(val any) (T, error) {
	var zeroT T
	targetType := reflect.TypeOf(zeroT)
	if val == nil {
		if targetType != nil {
			switch targetType.Kind() {
			case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
				return zeroT, nil
			default:
				return zeroT, fmt.Errorf("cannot assign nil to non-nillable type %T", zeroT)
			}
		} else {
			return zeroT, nil
		}
	}
	if targetType == nil {
		if tVal, ok := val.(T); ok {
			return tVal, nil
		}
		return zeroT, fmt.Errorf("internal error: failed direct assertion of non-nil %T to target type interface{}", val)
	}
	rv := reflect.ValueOf(val)
	if !rv.IsValid() {
		return zeroT, fmt.Errorf("internal error: invalid reflect.Value: %v (%T)", val, val)
	}
	isReflectNil := false
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		isReflectNil = rv.IsNil()
	}
	if isReflectNil {
		switch targetType.Kind() {
		case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
			return zeroT, nil
		default:
			return zeroT, fmt.Errorf("cannot assign typed nil (%T) to non-nillable type %T", val, zeroT)
		}
	}
	sourceType := rv.Type()
	targetKind := targetType.Kind()
	sourceKind := sourceType.Kind()
	if mf.IsNumericKind(targetKind) && mf.IsNumericKind(sourceKind) {
		if sourceType.AssignableTo(targetType) {
			if rv.CanConvert(targetType) {
				return rv.Convert(targetType).Interface().(T), nil
			}
		}
		switch {
		case mf.IsIntegerKind(sourceKind) && mf.IsIntegerKind(targetKind):
			if mf.IsSignedKind(sourceKind) {
				int64Val := rv.Int()
				if mf.IsSignedKind(targetKind) {
					if mf.FitsInInt(int64Val, targetType) {
						return reflect.ValueOf(int64Val).Convert(targetType).Interface().(T), nil
					}
					return zeroT, fmt.Errorf("value %d overflows %s", int64Val, targetType)
				} else {
					if int64Val < 0 {
						return zeroT, fmt.Errorf("negative %d to unsigned %s", int64Val, targetType)
					}
					uint64Val := uint64(int64Val)
					if mf.FitsInUint(uint64Val, targetType) {
						return reflect.ValueOf(uint64Val).Convert(targetType).Interface().(T), nil
					}
					return zeroT, fmt.Errorf("value %d overflows %s", uint64Val, targetType)
				}
			} else {
				uint64Val := rv.Uint()
				if mf.IsUnsignedKind(targetKind) {
					if mf.FitsInUint(uint64Val, targetType) {
						return reflect.ValueOf(uint64Val).Convert(targetType).Interface().(T), nil
					}
					return zeroT, fmt.Errorf("value %d overflows %s", uint64Val, targetType)
				} else {
					if mf.FitsInUintToSigned(uint64Val, targetType) {
						int64Val := int64(uint64Val)
						return reflect.ValueOf(int64Val).Convert(targetType).Interface().(T), nil
					}
					return zeroT, fmt.Errorf("value %d overflows %s", uint64Val, targetType)
				}
			}
		case mf.IsFloatKind(sourceKind) && mf.IsFloatKind(targetKind):
			float64Val := rv.Float()
			if targetKind == reflect.Float32 && (float64Val > math.MaxFloat32 || float64Val < -math.MaxFloat32) {
				return zeroT, fmt.Errorf("value %f overflows float32", float64Val)
			}
			return reflect.ValueOf(float64Val).Convert(targetType).Interface().(T), nil
		case mf.IsComplexKind(sourceKind) && mf.IsComplexKind(targetKind):
			complex128Val := rv.Complex()
			return reflect.ValueOf(complex128Val).Convert(targetType).Interface().(T), nil
		default:
		}
	}
	if (mf.IsNumericKind(sourceKind) && (targetKind == reflect.String || targetKind == reflect.Bool)) ||
		(sourceKind == reflect.String && mf.IsNumericKind(targetKind)) ||
		(sourceKind == reflect.Bool && targetKind != reflect.Bool) ||
		(targetKind == reflect.Bool && sourceKind != reflect.Bool) {
		return zeroT, fmt.Errorf("cannot convert %s to %s (invalid cross-category conversion)", sourceKind, targetKind)
	}
	if targetKind == reflect.String && sourceType.Kind() == reflect.Slice && sourceType.Elem().Kind() == reflect.Uint8 {
		if bs, ok := val.([]byte); ok {
			strVal := string(bs)
			strRv := reflect.ValueOf(strVal)
			if strRv.Type().AssignableTo(targetType) || strRv.Type().ConvertibleTo(targetType) {
				return strRv.Convert(targetType).Interface().(T), nil
			}
		}
	}
	if sourceType.AssignableTo(targetType) {
		if tVal, ok := rv.Interface().(T); ok {
			return tVal, nil
		}
	}
	if sourceType.ConvertibleTo(targetType) {
		convertedValue := rv.Convert(targetType)
		if tVal, ok := convertedValue.Interface().(T); ok {
			return tVal, nil
		}
	}
	return zeroT, fmt.Errorf("cannot convert %T (type %s) to %T (type %s)", val, sourceType.String(), zeroT, targetType.String())
}

// buildCacheKey generates a cache key for struct fields.
func buildCacheKey(rt reflect.Type, opts Options) string {
	useFieldNameStr := "nil"
	if opts.UseFieldName != nil {
		useFieldNameStr = fmt.Sprintf("%t", *opts.UseFieldName)
	}
	inc := append([]string{}, opts.IncludeOnly...)
	exc := append([]string{}, opts.ExcludeFields...)
	backupTags := append([]string{}, opts.TagBackup...)
	sort.Strings(inc)
	sort.Strings(exc)
	sort.Strings(backupTags)
	return fmt.Sprintf("%s:%s|tag=%s|backup=%s|fallback=%t|usefname=%s|inc=%s|exc=%s|allowzero=%t|allowsqlnull=%t", rt.String(), rt.PkgPath(), opts.Tag, strings.Join(backupTags, ","), opts.UseJSONFallback, useFieldNameStr, strings.Join(inc, ","), strings.Join(exc, ","), opts.AllowZeroValues, opts.AllowSQLNull)
}
