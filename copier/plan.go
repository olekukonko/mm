package copier

import "reflect"

// Interface types used by the copy hooks. Keeping them in package-level
// variables avoids rebuilding the same reflect.Type on every field visit.
var (
	copiableType        = reflect.TypeOf((*Copiable)(nil)).Elem()
	sensitivityType     = reflect.TypeOf((*Sensitivity)(nil)).Elem()
	marshalCopierType   = reflect.TypeOf((*MarshalCopier)(nil)).Elem()
	unmarshalCopierType = reflect.TypeOf((*UnmarshalCopier)(nil)).Elem()
)

// directCopyMode reports whether the copier is configured in a way where plain
// data can be copied with a single reflect.Value.Set (or a direct conversion)
// with exactly the same result as the field-by-field path.
func (c *Copier) directCopyMode() bool {
	return c.overwrite &&
		!c.skipZeroValue && !c.skipEmptyValue && !c.skipNilSrc && !c.skipSensitive &&
		!c.merging &&
		len(c.fieldMappings) == 0 && len(c.formatters) == 0 &&
		len(c.skipFields) == 0 && len(c.typeConverters) == 0
}

// canDirectAssign reports whether values of typ can be copied with a single
// reflect.Value.Set. The answer is cached per type.
func (c *Copier) canDirectAssign(typ reflect.Type) bool {
	if c.plainCache == nil {
		return false
	}
	if cached, ok := c.plainCache.Load(typ); ok {
		return cached.(bool)
	}
	ok := c.isPlainType(typ)
	c.plainCache.Store(typ, ok)
	return ok
}

// isPlainType reports whether typ is a value type with no reference semantics
// (no pointers, maps, slices, interfaces, channels or functions) and no copy
// hooks or behaviour-changing tags anywhere in its tree. Such values can be
// copied by assignment without aliasing anything.
func (c *Copier) isPlainType(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.String:
		return !implementsCopyHooks(typ)
	case reflect.Array:
		return c.isPlainType(typ.Elem())
	case reflect.Struct:
		if implementsCopyHooks(typ) {
			return false
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.PkgPath != "" || f.Anonymous {
				// Unexported and embedded fields need the full field walk so that
				// skip/zero semantics stay identical to the slow path.
				return false
			}
			if c.hasBehaviourTag(f.Tag) {
				return false
			}
			if !c.isPlainType(f.Type) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// hasBehaviourTag reports whether a struct tag changes copy semantics (skip,
// omitempty, sensitive, unmapped) rather than just renaming the field.
func (c *Copier) hasBehaviourTag(tag reflect.StructTag) bool {
	if c.tagHasBehaviour(tag, c.tagName) {
		return true
	}
	for _, key := range c.fallbackTagKeys {
		if c.tagHasBehaviour(tag, key) {
			return true
		}
	}
	return false
}

func (c *Copier) tagHasBehaviour(tag reflect.StructTag, key string) bool {
	value, ok := tag.Lookup(key)
	if !ok || value == "" {
		return false
	}
	opts := ParseTagOptions(value)
	if _, ignore := opts["-"]; ignore {
		return true
	}
	for _, flag := range []string{"omitempty", "sensitive", "unmapped"} {
		if _, set := opts[flag]; set {
			return true
		}
	}
	return false
}

// implementsCopyHooks reports whether typ (or *typ) implements one of the
// custom copy interfaces and therefore must be handled by the slow path.
func implementsCopyHooks(typ reflect.Type) bool {
	return implementsAny(typ, copiableType) ||
		implementsAny(typ, sensitivityType) ||
		implementsAny(typ, marshalCopierType) ||
		implementsAny(typ, unmarshalCopierType)
}

func implementsAny(typ, iface reflect.Type) bool {
	if typ.Implements(iface) {
		return true
	}
	if typ.Kind() != reflect.Ptr {
		return reflect.PtrTo(typ).Implements(iface)
	}
	return false
}

// tryFastStructCopy copies src into dest using a cached field plan. It reports
// whether the plan could be applied; false means the caller must fall back to
// the generic field-by-field path.
func (c *Copier) tryFastStructCopy(dest, src reflect.Value) bool {
	plan := c.fastStructPlan(src.Type(), dest.Type())
	if plan == nil {
		return false
	}
	for _, pair := range plan.pairs {
		srcVal := src.FieldByIndex(pair.srcIndex)
		destVal := dest.FieldByIndex(pair.destIndex)
		if !destVal.CanSet() {
			return false
		}
		if pair.sameType {
			destVal.Set(srcVal)
		} else {
			destVal.Set(srcVal.Convert(pair.destType))
		}
	}
	return true
}

type structFieldPair struct {
	srcIndex  []int
	destIndex []int
	destType  reflect.Type
	sameType  bool
}

// structPlan is a precomputed field mapping for a struct-to-struct copy.
type structPlan struct {
	pairs []structFieldPair
}

// fastStructPlan returns a cached plan for copying srcType into destType, or
// nil when the generic (field-by-field) path has to be used.
//
// A plan is only produced for the common "same field names, plain values, no
// customisation" shape. Everything else keeps using copyStructDriven, so plan
// building can never change behaviour.
func (c *Copier) fastStructPlan(srcType, destType reflect.Type) *structPlan {
	if c.planCache == nil || !c.directCopyMode() {
		return nil
	}
	key := struct{ src, dst reflect.Type }{srcType, destType}
	if cached, ok := c.planCache.Load(key); ok {
		if cached == nil {
			return nil
		}
		return cached.(*structPlan)
	}

	plan := c.buildStructPlan(srcType, destType)
	if plan == nil {
		c.planCache.Store(key, (*structPlan)(nil))
		return nil
	}
	c.planCache.Store(key, plan)
	return plan
}

func (c *Copier) buildStructPlan(srcType, destType reflect.Type) *structPlan {
	if srcType.Kind() != reflect.Struct || destType.Kind() != reflect.Struct {
		return nil
	}

	destInfo := c.getTypeInfo(destType)
	srcInfo := c.getTypeInfo(srcType)
	if destInfo.unmappedField != nil || len(destInfo.fields) == 0 || len(srcInfo.fields) == 0 {
		return nil
	}

	plan := &structPlan{pairs: make([]structFieldPair, 0, len(destInfo.fields))}
	for name, destFieldInfo := range destInfo.fields {
		srcFieldInfo, ok := srcInfo.fields[name]
		if !ok {
			// An unmatched destination field would have to be zeroed, which the
			// slow path already does; do not try to replicate it here.
			return nil
		}
		if c.hasBehaviourTag(srcFieldInfo.structField.Tag) || c.hasBehaviourTag(destFieldInfo.structField.Tag) {
			return nil
		}
		if !c.isPlainType(srcFieldInfo.typ) || !c.isPlainType(destFieldInfo.typ) {
			return nil
		}
		if srcFieldInfo.typ != destFieldInfo.typ && !srcFieldInfo.typ.ConvertibleTo(destFieldInfo.typ) {
			return nil
		}
		plan.pairs = append(plan.pairs, structFieldPair{
			srcIndex:  srcFieldInfo.index,
			destIndex: destFieldInfo.index,
			destType:  destFieldInfo.typ,
			sameType:  srcFieldInfo.typ == destFieldInfo.typ,
		})
	}

	return plan
}

// mapStructField is one destination field in a map<->struct plan.
type mapStructField struct {
	name  string
	key   reflect.Value // pre-built reflect.Value of name, avoids per-call ValueOf
	index []int
	typ   reflect.Type
}

// mapStructPlan is a cached field list for map<->struct copies.
type mapStructPlan struct {
	fields   []mapStructField
	byName   map[string]int  // key -> index into fields
	unmapped *mapStructField // nil when the struct has no usable ,unmapped field
}

type mapPlanKey struct {
	typ reflect.Type
}

// fastMapStructPlan returns a cached list of destination fields for a plain
// struct, or nil when the generic map/struct code has to handle the copy.
func (c *Copier) fastMapStructPlan(destType reflect.Type) *mapStructPlan {
	if c.planCache == nil || !c.directCopyMode() || destType.Kind() != reflect.Struct {
		return nil
	}
	key := mapPlanKey{typ: destType}
	if cached, ok := c.planCache.Load(key); ok {
		if cached == nil {
			return nil
		}
		return cached.(*mapStructPlan)
	}

	info := c.getTypeInfo(destType)
	plan := &mapStructPlan{
		fields: make([]mapStructField, 0, len(info.fields)),
		byName: make(map[string]int, len(info.fields)),
	}

	// The ,unmapped sink must be map[string]interface{} (map[string]T would need
	// per-value conversion, which stays in the generic path).
	if info.unmappedField != nil {
		ft := info.unmappedField.typ
		if ft.Kind() != reflect.Map || ft.Key().Kind() != reflect.String ||
			ft.Elem().Kind() != reflect.Interface || ft.Elem().NumMethod() != 0 {
			c.planCache.Store(key, (*mapStructPlan)(nil))
			return nil
		}
		plan.unmapped = &mapStructField{
			name:  info.unmappedField.name,
			index: info.unmappedField.index,
			typ:   ft,
		}
	}

	for name, fi := range info.fields {
		if c.hasBehaviourTag(fi.structField.Tag) || !c.isPlainType(fi.typ) {
			c.planCache.Store(key, (*mapStructPlan)(nil))
			return nil
		}
		plan.byName[name] = len(plan.fields)
		plan.fields = append(plan.fields, mapStructField{
			name:  name,
			key:   reflect.ValueOf(name),
			index: fi.index,
			typ:   fi.typ,
		})
	}

	if len(plan.fields) == 0 && plan.unmapped == nil {
		c.planCache.Store(key, (*mapStructPlan)(nil))
		return nil
	}
	c.planCache.Store(key, plan)
	return plan
}

// tryFastMapToStruct handles map[string]T -> plain struct when every present
// value can be assigned directly to its destination field. Values that do not
// match a field land in the ,unmapped map when the struct declares one.
// Anything else (interfaces, numeric conversions, hooks, tags) falls back to
// the generic path.
func (c *Copier) tryFastMapToStruct(dest, src reflect.Value) bool {
	if dest.Kind() != reflect.Struct || !dest.CanSet() || src.Kind() != reflect.Map ||
		src.Type().Key().Kind() != reflect.String {
		return false
	}
	if implementsCopyHooks(dest.Type()) || implementsCopyHooks(src.Type()) {
		return false
	}
	plan := c.fastMapStructPlan(dest.Type())
	if plan == nil {
		return false
	}
	elemIsInterface := src.Type().Elem().Kind() == reflect.Interface

	// Validate before writing so a fallback never leaves a half-copied value.
	for _, f := range plan.fields {
		if !dest.FieldByIndex(f.index).CanSet() {
			return false
		}
		v := src.MapIndex(f.key)
		if !v.IsValid() {
			continue
		}
		if elemIsInterface {
			if v.IsNil() {
				// A nil value would be zeroed/skipped by the generic path.
				return false
			}
			v = v.Elem()
		}
		if !v.Type().AssignableTo(f.typ) {
			return false
		}
	}

	var unmappedSink reflect.Value
	if plan.unmapped != nil {
		unmappedSink = dest.FieldByIndex(plan.unmapped.index)
		if !unmappedSink.CanSet() || unmappedSink.Type() != plan.unmapped.typ {
			return false
		}
		// The generic path deep-copies unmatched values into the sink; only plain
		// values can be shared safely (they have value semantics).
		iter := src.MapRange()
		for iter.Next() {
			if _, matched := plan.byName[iter.Key().String()]; matched {
				continue
			}
			v := iter.Value()
			if elemIsInterface {
				if v.IsNil() {
					return false
				}
				v = v.Elem()
			}
			if !c.canDirectAssign(v.Type()) {
				return false
			}
		}
	}

	for _, f := range plan.fields {
		fieldVal := dest.FieldByIndex(f.index)
		v := src.MapIndex(f.key)
		if !v.IsValid() {
			fieldVal.Set(reflect.Zero(f.typ))
			continue
		}
		if elemIsInterface {
			v = v.Elem()
		}
		fieldVal.Set(v)
	}

	if plan.unmapped != nil {
		sink := reflect.MakeMap(plan.unmapped.typ)
		iter := src.MapRange()
		for iter.Next() {
			if _, matched := plan.byName[iter.Key().String()]; matched {
				continue
			}
			v := iter.Value()
			if elemIsInterface {
				v = v.Elem()
			}
			sink.SetMapIndex(iter.Key(), v)
		}
		unmappedSink.Set(sink)
	}
	return true
}

// tryFastStructToMap handles plain struct -> map with string keys when every
// field is directly assignable to the map's element type.
func (c *Copier) tryFastStructToMap(dest, src reflect.Value) bool {
	if dest.Kind() != reflect.Map || !dest.CanSet() || dest.Type().Key().Kind() != reflect.String {
		return false
	}
	if src.Kind() != reflect.Struct {
		return false
	}
	plan := c.fastMapStructPlan(src.Type())
	if plan == nil || plan.unmapped != nil {
		return false
	}
	elemType := dest.Type().Elem()
	for _, f := range plan.fields {
		if !f.typ.AssignableTo(elemType) {
			return false
		}
	}

	if c.overwrite {
		dest.Set(reflect.MakeMap(dest.Type()))
	} else if dest.IsNil() {
		return false
	}
	for _, f := range plan.fields {
		dest.SetMapIndex(f.key, src.FieldByIndex(f.index))
	}
	return true
}
