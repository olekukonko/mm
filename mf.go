// mf.go
package mm

import (
	"errors"
	"fmt"
	"reflect"
	"unsafe"
)

// Handling Helpers
func (c *Copier) getTypeInfo(typ reflect.Type) *TypeInfo {
	if typ == nil {
		return &TypeInfo{fields: make(map[string]FieldInfo)}
	}

	concreteType := typ
	if concreteType.Kind() == reflect.Ptr {
		if concreteType.Elem() == nil {
			c.logger.Warn("getTypeInfo: encountered pointer type %s with nil element type", typ)
			return &TypeInfo{fields: make(map[string]FieldInfo)}
		}
		concreteType = concreteType.Elem()
	}

	if concreteType.Kind() != reflect.Struct {
		return &TypeInfo{
			fields:       make(map[string]FieldInfo),
			size:         typ.Size(),
			alignment:    uintptr(typ.Align()),
			containsPtrs: checkContainsPointers(typ),
			isStruct:     false,
			isSlice:      typ.Kind() == reflect.Slice,
			isMap:        typ.Kind() == reflect.Map,
			isPtr:        typ.Kind() == reflect.Ptr,
			isInterface:  typ.Kind() == reflect.Interface,
			canMemCopy:   isMemCopySuitable(concreteType),
		}
	}

	if cached, ok := c.typeInfoCache.Load(concreteType); ok {
		return cached.(*TypeInfo)
	}

	c.logger.Debug("getTypeInfo: Analyzing struct type %s (concrete: %s)", typ, concreteType)
	info := &TypeInfo{
		fields:       make(map[string]FieldInfo),
		size:         typ.Size(),
		alignment:    uintptr(typ.Align()),
		containsPtrs: checkContainsPointers(typ),
		isStruct:     true,
		isSlice:      typ.Kind() == reflect.Slice, // Use original type for these flags
		isMap:        typ.Kind() == reflect.Map,
		isPtr:        typ.Kind() == reflect.Ptr,
		isInterface:  typ.Kind() == reflect.Interface,
	}
	processedEffectiveNames := make(map[string]string)

	for i := 0; i < concreteType.NumField(); i++ {
		f := concreteType.Field(i)
		c.logger.Debug("getTypeInfo: Processing field %s.%s", concreteType.Name(), f.Name)

		if f.PkgPath != "" && !c.copyUnexported {
			c.logger.Debug("getTypeInfo:     Skipping unexported field '%s'.", f.Name)
			continue
		}

		effectiveName := f.Name
		var chosenTagOptions map[string]string
		var isIgnoredByTag bool

		// --- Tag Parsing Logic (using corrected parseTagOptions) ---
		primaryTagValue := f.Tag.Get(c.tagName)
		if primaryTagValue != "" {
			parsedOpts := parseTagOptions(primaryTagValue) // Corrected parsing
			if name, ok := parsedOpts["name"]; ok {
				effectiveName = name
				chosenTagOptions = parsedOpts
				c.logger.Debug("getTypeInfo:     Primary tag ('%s') provided name: '%s', options: %v", c.tagName, effectiveName, chosenTagOptions)
			} else if _, ignorePresent := parsedOpts["-"]; ignorePresent {
				isIgnoredByTag = true
				chosenTagOptions = parsedOpts
				c.logger.Debug("getTypeInfo:     Primary tag ('%s') IGNORED field '%s'.", c.tagName, f.Name)
			} else { // Primary tag exists but only contains flags (like "omitempty", "sensitive")
				chosenTagOptions = parsedOpts
				c.logger.Debug("getTypeInfo:     Primary tag ('%s') provided only flags: %v for field '%s'. Effective name remains '%s'.", c.tagName, chosenTagOptions, f.Name, effectiveName)
			}
		} else {
			c.logger.Debug("getTypeInfo:     Primary tag ('%s') not found or empty for field %s.", c.tagName, f.Name)
		}

		shouldTryFallbacksForName := !isIgnoredByTag && c.tagResolution &&
			(chosenTagOptions == nil || (chosenTagOptions != nil && effectiveName == f.Name && chosenTagOptions["name"] == ""))

		if shouldTryFallbacksForName {
			c.logger.Debug("getTypeInfo:     Tag resolution ON for field %s. Trying fallbacks for name/ignore: %v", f.Name, c.fallbackTagKeys)
			for _, fallbackKey := range c.fallbackTagKeys {
				fallbackTagValue := f.Tag.Get(fallbackKey)
				if fallbackTagValue != "" {
					parsedFallbackOpts := parseTagOptions(fallbackTagValue)
					if name, ok := parsedFallbackOpts["name"]; ok {
						effectiveName = name
						chosenTagOptions = parsedFallbackOpts
						isIgnoredByTag = false
						c.logger.Debug("getTypeInfo:       Fallback tag '%s' provided name: '%s'. Options from this tag: %v.", fallbackKey, effectiveName, chosenTagOptions)
						break
					} else if _, ignorePresent := parsedFallbackOpts["-"]; ignorePresent {
						isIgnoredByTag = true
						chosenTagOptions = parsedFallbackOpts
						c.logger.Debug("getTypeInfo:       Fallback tag '%s' IGNORED field '%s'.", fallbackKey, f.Name)
						break
					} else if len(parsedFallbackOpts) > 0 && chosenTagOptions == nil {
						chosenTagOptions = parsedFallbackOpts
						c.logger.Debug("getTypeInfo:       Fallback tag '%s' provided only flags: %v for field '%s'. Effective name remains '%s'.", fallbackKey, chosenTagOptions, f.Name, effectiveName)
						// Note: We don't break here, primary tag options might still be preferred if they existed without a name.
					}
				}
			}
		}
		// --- End Tag Parsing Logic ---

		if chosenTagOptions == nil {
			chosenTagOptions = make(map[string]string)
		}

		// *** Check for sensitive flag using correct parsing ***
		_, isSensitive := chosenTagOptions["sensitive"] // Check for presence of the key

		fInfo := FieldInfo{
			name:        effectiveName,
			structField: f,
			index:       f.Index,
			offset:      f.Offset,
			typ:         f.Type,
			tagOpts:     chosenTagOptions,
			sensitive:   isSensitive, // Use the correctly parsed flag
		}

		_, isUnmapped := chosenTagOptions["unmapped"]
		if isUnmapped {
			if info.unmappedField != nil && info.unmappedField.structField.Name != f.Name {
				c.logger.Warn("[mm warning] Multiple unmapped fields ('%s', '%s') in %s, ignoring current field '%s'",
					info.unmappedField.structField.Name, f.Name, concreteType.Name(), f.Name)
			} else {
				info.unmappedField = &fInfo
				c.logger.Debug("getTypeInfo:     Field '%s' (Go name) is designated as UNMAPPED field. Effective name: '%s'.", f.Name, effectiveName)
			}
		} else if isIgnoredByTag {
			c.logger.Debug("getTypeInfo:     Field '%s' (Go name) is IGNORED due to tag '-' (effective name was to be '%s').", f.Name, effectiveName)
		} else {
			if existingGoName, duplicate := processedEffectiveNames[effectiveName]; duplicate {
				c.logger.Warn("[mm warning] Duplicate effective field name '%s' in type %s. Field '%s' conflicts with already processed field '%s'. Skipping field '%s'.",
					effectiveName, concreteType.Name(), f.Name, existingGoName, f.Name)
			} else {
				info.fields[effectiveName] = fInfo
				processedEffectiveNames[effectiveName] = f.Name
				c.logger.Debug("getTypeInfo:     Field '%s' (Go name) mapped to effective name '%s'. Added to TypeInfo.", f.Name, effectiveName)
			}
		}
	}
	info.canMemCopy = isMemCopySuitable(concreteType)
	c.typeInfoCache.Store(concreteType, info)
	c.logger.Debug("getTypeInfo: Finished analyzing type %s (concrete: %s). Fields: %d, Unmapped: %v",
		typ, concreteType, len(info.fields), info.unmappedField != nil)
	return info
}

func (c *Copier) handlePotentialError(err error, ctx *SysContext) bool {
	if err == nil {
		return false
	}
	if c.panicOnError {
		panic(fmt.Sprintf("mm copy panic: %v", err))
	}
	if c.collectErrors {
		c.logger.Debug("Collecting error: %v", err)
		ctx.errors = append(ctx.errors, err)
		return true
	}
	return false
}

func (c *Copier) finalizeResult(returnedErr error, ctx *SysContext) error {
	if c.collectErrors && len(ctx.errors) > 0 {
		finalErr := errors.Join(ctx.errors...)
		if c.panicOnError {
			panic(fmt.Sprintf("mm copy failed with %d error(s): %v", len(ctx.errors), finalErr))
		}
		return finalErr
	}
	if returnedErr != nil {
		if c.panicOnError {
			panic(fmt.Sprintf("mm copy failed: %v", returnedErr))
		}
		return returnedErr
	}
	return nil
}

func (c *Copier) tryConvert(src reflect.Value, destType reflect.Type) (reflect.Value, bool, error) {
	tryConvertDepthPrefix := "    "
	if !src.IsValid() {
		c.logger.Debug("%stryConvert: Source is invalid, cannot convert.", tryConvertDepthPrefix)
		return reflect.Value{}, false, ErrSourceIsNil
	}
	srcType := src.Type()
	var srcInterfaceVal interface{}
	if src.CanInterface() {
		srcInterfaceVal = src.Interface()
	} else {
		srcInterfaceVal = "<uninterfaceable_src>"
	}
	c.logger.Debug("%sTRYCONVERT_ENTRY: Src: %s (%v), DestType: %s", tryConvertDepthPrefix, srcType, srcInterfaceVal, destType)

	if srcType.AssignableTo(destType) {
		c.logger.Debug("%s  tryConvert: Src type %s is directly assignable to dest type %s.", tryConvertDepthPrefix, srcType, destType)
		return src, true, nil
	}

	if converters, ok := c.typeConverters[srcType]; ok {
		if converter, exists := converters[destType]; exists {
			c.logger.Debug("%s  tryConvert: Using custom converter %s -> %s", tryConvertDepthPrefix, srcType, destType)
			converted, errConvert := converter(src)
			if errConvert != nil {
				c.logger.Debug("%s    tryConvert: Custom converter FAILED: %v", tryConvertDepthPrefix, errConvert)
				return reflect.Value{}, false, fmt.Errorf("type converter %s -> %s failed: %w", srcType, destType, errConvert)
			}
			if !converted.IsValid() {
				c.logger.Debug("%s    tryConvert: Custom converter for %s -> %s returned an invalid reflect.Value.", tryConvertDepthPrefix, srcType, destType)
				return reflect.Value{}, false, fmt.Errorf("type converter %s -> %s returned invalid value", srcType, destType)
			}
			if !converted.Type().AssignableTo(destType) {
				if converted.Type().ConvertibleTo(destType) {
					c.logger.Debug("%s    tryConvert: Custom converter for %s -> %s returned %s, converting to %s.", tryConvertDepthPrefix, srcType, destType, converted.Type(), destType)
					finalConverted := converted.Convert(destType)
					if !finalConverted.Type().AssignableTo(destType) {
						c.logger.Debug("%s      tryConvert: Post-Go-conversion type %s is STILL not assignable to %s.", tryConvertDepthPrefix, finalConverted.Type(), destType)
						return reflect.Value{}, false, fmt.Errorf("type converter %s -> %s returned %s, and after Go conversion to %s, it's still not assignable to %s", srcType, destType, converted.Type(), finalConverted.Type(), destType)
					}
					c.logger.Debug("%s    tryConvert: Custom converter SUCCESS (with Go conversion). Result type: %s", tryConvertDepthPrefix, finalConverted.Type())
					return finalConverted, true, nil
				}
				c.logger.Debug("%s    tryConvert: Custom converter for %s -> %s returned type %s, which is not assignable or convertible to %s.", tryConvertDepthPrefix, srcType, destType, converted.Type(), destType)
				return reflect.Value{}, false, fmt.Errorf("type converter %s -> %s returned %s, not assignable/convertible to %s", srcType, destType, converted.Type(), destType)
			}
			c.logger.Debug("%s    tryConvert: Custom converter SUCCESS. Result type: %s", tryConvertDepthPrefix, converted.Type())
			return converted, true, nil
		}
	}

	if srcType.ConvertibleTo(destType) {
		c.logger.Debug("%s  tryConvert: Using Go's conversion %s -> %s", tryConvertDepthPrefix, srcType, destType)
		return src.Convert(destType), true, nil
	}

	// T -> *U
	if destType.Kind() == reflect.Ptr {
		destElemType := destType.Elem()
		if convMap, ok := c.typeConverters[srcType]; ok {
			if _, specificConvExists := convMap[destElemType]; specificConvExists {
				c.logger.Debug("%s  tryConvert: Found custom converter for src %s -> destElem %s. Attempting T -> *U.", tryConvertDepthPrefix, srcType, destElemType)
				convertedElem, elemOk, elemErr := c.tryConvert(src, destElemType)
				if elemOk && elemErr == nil {
					newPtr := reflect.New(destElemType)
					newPtr.Elem().Set(convertedElem)
					c.logger.Debug("%s    tryConvert: Successfully created *%s from %s via element conversion.", tryConvertDepthPrefix, destElemType, srcType)
					return newPtr, true, nil
				}
				if elemErr != nil {
					c.logger.Debug("%s    tryConvert: Element conversion for T -> *U (src %s to elem %s) failed: %v", tryConvertDepthPrefix, srcType, destElemType, elemErr)
				}
			}
		}
		if srcType.Kind() != reflect.Ptr {
			if srcType.AssignableTo(destElemType) {
				c.logger.Debug("%s  tryConvert: Assigning %s to *%s (elem assignable)", tryConvertDepthPrefix, srcType, destElemType)
				newPtr := reflect.New(destElemType)
				newPtr.Elem().Set(src)
				return newPtr, true, nil
			}
			if srcType.ConvertibleTo(destElemType) {
				c.logger.Debug("%s  tryConvert: Converting %s to *%s (elem convertible)", tryConvertDepthPrefix, srcType, destElemType)
				newPtr := reflect.New(destElemType)
				newPtr.Elem().Set(src.Convert(destElemType))
				return newPtr, true, nil
			}
		}
	}

	// *T -> U
	if srcType.Kind() == reflect.Ptr && !src.IsNil() {
		srcElem := src.Elem()
		srcElemType := srcElem.Type()
		if destType.Kind() != reflect.Ptr {
			if convMap, ok := c.typeConverters[srcElemType]; ok {
				if _, specificConvExists := convMap[destType]; specificConvExists {
					c.logger.Debug("%s  tryConvert: Found custom converter for srcElem %s -> dest %s. Attempting *T -> U.", tryConvertDepthPrefix, srcElemType, destType)
					return c.tryConvert(srcElem, destType)
				}
			}
		}
		if srcElemType.AssignableTo(destType) {
			c.logger.Debug("%s  tryConvert: Assigning *%s to %s (deref assignable)", tryConvertDepthPrefix, srcElemType, destType)
			return srcElem, true, nil
		}
		if srcElemType.ConvertibleTo(destType) {
			c.logger.Debug("%s  tryConvert: Converting *%s to %s (deref convertible)", tryConvertDepthPrefix, srcElemType, destType)
			return srcElem.Convert(destType), true, nil
		}
	}

	// interface -> T
	if srcType.Kind() == reflect.Interface && !src.IsNil() {
		srcElem := src.Elem()
		if srcElem.IsValid() {
			convertedElem, okElem, errElem := c.tryConvert(srcElem, destType)
			if okElem && errElem == nil {
				c.logger.Debug("%s  tryConvert: Converting interface concrete type %s to %s", tryConvertDepthPrefix, srcElem.Type(), destType)
				return convertedElem, true, nil
			}
			if errElem != nil && !errors.Is(errElem, ErrNoConverter) {
				return reflect.Value{}, false, fmt.Errorf("failed to convert interface element %s to %s: %w", srcElem.Type(), destType, errElem)
			}
		}
	}

	c.logger.Debug("%s  tryConvert: No conversion path found for %s -> %s", tryConvertDepthPrefix, srcType, destType)
	return reflect.Value{}, false, ErrNoConverter
}

func (c *Copier) getSettableFieldValue(structVal reflect.Value, fi *FieldInfo) (reflect.Value, bool, error) {
	if !structVal.IsValid() {
		return reflect.Value{}, false, fmt.Errorf("invalid struct value for field '%s'", fi.name)
	}
	if fi == nil {
		return reflect.Value{}, false, fmt.Errorf("nil field info")
	}
	concreteStructVal := structVal
	if concreteStructVal.Kind() == reflect.Ptr {
		if concreteStructVal.IsNil() {
			return reflect.Value{}, false, fmt.Errorf("cannot get field '%s' from nil pointer to struct %s", fi.name, concreteStructVal.Type())
		}
		concreteStructVal = concreteStructVal.Elem()
	}
	if concreteStructVal.Kind() != reflect.Struct {
		return reflect.Value{}, false, fmt.Errorf("cannot get field '%s' from non-struct type %s", fi.name, concreteStructVal.Type())
	}

	f := concreteStructVal.FieldByIndex(fi.index)
	if !f.IsValid() {
		return reflect.Value{}, false, fmt.Errorf("field '%s' (index %v) is invalid on struct %s", fi.name, fi.index, concreteStructVal.Type())
	}
	if f.CanSet() {
		return f, true, nil
	}

	if fi.structField.PkgPath != "" { // Unexported field
		if c.useUnsafe && c.copyUnexported {
			if !concreteStructVal.CanAddr() {
				return reflect.Value{}, false, fmt.Errorf("struct %s (field '%s') not addressable for unsafe set", concreteStructVal.Type(), fi.name)
			}
			fieldPtr := unsafe.Pointer(concreteStructVal.UnsafeAddr() + fi.offset)
			fUnsafe := reflect.NewAt(fi.typ, fieldPtr).Elem()
			if fUnsafe.IsValid() { // Assume settable if obtained via NewAt
				return fUnsafe, true, nil
			}
			return reflect.Value{}, false, fmt.Errorf("unsafe access to field '%s' of %s yielded invalid value", fi.name, concreteStructVal.Type())
		}
		return reflect.Value{}, false, ErrUnexportedField
	}
	// Exported but not settable
	return reflect.Value{}, false, fmt.Errorf("exported field '%s' not settable (likely from unaddressable struct)", fi.name)
}

func (c *Copier) readSourceFieldValue(srcStruct reflect.Value, fi *FieldInfo) (reflect.Value, bool) {
	if !srcStruct.IsValid() || fi == nil {
		return reflect.Value{}, false
	}
	concreteSrcStruct := srcStruct
	if concreteSrcStruct.Kind() == reflect.Ptr {
		if concreteSrcStruct.IsNil() {
			return reflect.Value{}, false
		}
		concreteSrcStruct = concreteSrcStruct.Elem()
	}
	if concreteSrcStruct.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}

	f := concreteSrcStruct.FieldByIndex(fi.index)
	if !f.IsValid() {
		return reflect.Value{}, false
	}

	if f.CanInterface() {
		return f, true
	} // Standard way for exported fields

	if fi.structField.PkgPath != "" { // Unexported
		if c.useUnsafe && c.copyUnexported {
			if !concreteSrcStruct.CanAddr() {
				c.logger.Debug("   readSourceFieldValue: Struct %s (field '%s') not addressable for unsafe read.", concreteSrcStruct.Type(), fi.name)
				return reflect.Value{}, false
			}
			fieldPtr := unsafe.Pointer(concreteSrcStruct.UnsafeAddr() + fi.offset)
			fUnsafe := reflect.NewAt(fi.typ, fieldPtr).Elem()
			if fUnsafe.IsValid() {
				return fUnsafe, true
			}
			c.logger.Debug("   readSourceFieldValue: Unsafe read for '%s' of %s yielded invalid value.", fi.name, concreteSrcStruct.Type())
			return reflect.Value{}, false
		}
		c.logger.Debug("   readSourceFieldValue: Field '%s' of %s unexported and unsafe/copyUnexported not enabled.", fi.name, concreteSrcStruct.Type())
		return reflect.Value{}, false
	}

	// Exported but cannot interface (rare)
	c.logger.Debug("   readSourceFieldValue: Exported field '%s' of %s cannot be read via CanInterface().", fi.name, concreteSrcStruct.Type())
	return reflect.Value{}, false
}

func (c *Copier) checkIsSensitive(val reflect.Value) bool {
	if !val.IsValid() {
		return false
	}

	t := val.Type()
	checkVal := val

	if t.Kind() == reflect.Ptr || t.Kind() == reflect.Interface {
		if val.IsNil() {
			// Check type definition for sensitivity
			tToCheck := t
			if t.Kind() == reflect.Ptr {
				tToCheck = t.Elem()
			}
			if tToCheck == nil || tToCheck.Kind() == reflect.Invalid {
				return false
			}

			if tToCheck.Implements(reflect.TypeOf((*Sensitivity)(nil)).Elem()) {
				zeroVal := reflect.New(tToCheck).Elem() // Get zero value of element T
				if zeroVal.CanInterface() {
					if s, ok := zeroVal.Interface().(Sensitivity); ok {
						return s.Sensitive()
					}
				} else if zeroVal.CanAddr() && reflect.PtrTo(tToCheck).Implements(reflect.TypeOf((*Sensitivity)(nil)).Elem()) {
					// Check if *T implements it for pointer receiver on T
					zeroPtr := zeroVal.Addr()
					if zeroPtr.CanInterface() {
						if s, ok := zeroPtr.Interface().(Sensitivity); ok {
							return s.Sensitive()
						}
					}
				}
			}
			return false // Cannot determine from nil if type doesn't implement
		}
		// Non-nil Ptr/Interface, check concrete value
		checkVal = val.Elem()
		if !checkVal.IsValid() {
			return false
		} // e.g., nil pointer inside interface
		t = checkVal.Type()
	}

	// Check concrete value instance or its pointer receiver
	if checkVal.CanInterface() {
		if s, ok := checkVal.Interface().(Sensitivity); ok {
			return s.Sensitive()
		}
	}
	if checkVal.CanAddr() {
		addrVal := checkVal.Addr()
		if addrVal.CanInterface() {
			if s, ok := addrVal.Interface().(Sensitivity); ok {
				return s.Sensitive()
			}
		}
	}

	return false
}
