// cm.go
package mm

import (
	"errors"
	"fmt"
	"log"
	"reflect"
)

// copyMapToStruct copies data from a map to a struct, matching map keys to field names.
func (c *Copier) copyMapToStruct(dest reflect.Value, src reflect.Value, ctx *SysContext) error {
	destInfo := c.getTypeInfo(dest.Type())
	srcMapKeys := src.MapKeys()
	usedMapKeys := make(map[string]struct{})
	processedDestFields := make(map[string]struct{})
	var unmappedDestMap reflect.Value
	var unmappedDestFieldName string
	var unmappedDestValueType reflect.Type

	c.logger.Indent(ctx.depth)
	c.logger.Debugf("Copying Map -> Struct (%s -> %s) | Overwrite: %v", src.Type(), dest.Type(), c.overwrite)

	if destInfo.unmappedField != nil {
		unmappedDestFieldName = destInfo.unmappedField.name
		mapFieldVal, mapFieldValOk, mapFieldValErr := c.getSettableFieldValue(dest, destInfo.unmappedField)
		if mapFieldValErr == nil && mapFieldValOk {
			fieldType := mapFieldVal.Type()
			isValidUnmappedType := fieldType.Kind() == reflect.Map &&
				fieldType.Key().Kind() == reflect.String &&
				(fieldType.Elem().Kind() == reflect.Interface || fieldType.Elem() == reflect.TypeOf((*interface{})(nil)).Elem())
			if isValidUnmappedType {
				if mapFieldVal.IsNil() || c.overwrite {
					mapFieldVal.Set(reflect.MakeMap(fieldType))
				} else if mapFieldVal.IsNil() && !c.overwrite {
					mapFieldVal.Set(reflect.MakeMap(fieldType))
				}
				unmappedDestMap = mapFieldVal
				unmappedDestValueType = fieldType.Elem()
			} else {
				warnMsg := fmt.Sprintf("[mm warning] %s: dest unmapped field '%s' has type %s.", ErrUnmappedFieldType, unmappedDestFieldName, fieldType)
				log.Print(warnMsg)
				c.logger.Warnf("   Warning: %s", warnMsg)
			}
		} else if mapFieldValErr != nil {
			if !errors.Is(mapFieldValErr, ErrUnexportedField) || c.collectErrors {
				errUnmapped := fmt.Errorf("getting unmapped dest field '%s': %w", unmappedDestFieldName, mapFieldValErr)
				if !c.handlePotentialError(errUnmapped, ctx) {
					c.logger.Warnf("   Error initializing unmapped dest field '%s': %v.", unmappedDestFieldName, mapFieldValErr)
				}
			} else {
				c.logger.Debugf("   Unmapped dest field '%s' is unexported or not settable.", unmappedDestFieldName)
			}
		}
		processedDestFields[unmappedDestFieldName] = struct{}{}
	}

	for destFieldEffectiveName, destFieldInfo := range destInfo.fields {
		if destFieldEffectiveName == unmappedDestFieldName {
			continue
		}
		c.logger.Debugf(" Processing destination field '%s' (Go name: '%s')", destFieldEffectiveName, destFieldInfo.structField.Name)

		if _, skip := c.skipFields[destFieldEffectiveName]; skip {
			c.logger.Debugf("   -> Skipping dest field '%s' (explicit global skip).", destFieldEffectiveName)
			processedDestFields[destFieldEffectiveName] = struct{}{}
			continue
		}
		if _, ignore := destFieldInfo.tagOpts["-"]; ignore {
			c.logger.Debugf("   -> Skipping dest field '%s' (tag '-' on destination field).", destFieldEffectiveName)
			processedDestFields[destFieldEffectiveName] = struct{}{}
			continue
		}

		srcKeyNameForThisDestField := destFieldEffectiveName
		if explicitSrcKey, ok := c.fieldMappings[destFieldInfo.structField.Name]; ok {
			srcKeyNameForThisDestField = explicitSrcKey
		} else if mappedSrcKey, ok := c.fieldMappings[destFieldEffectiveName]; ok {
			srcKeyNameForThisDestField = mappedSrcKey
		}

		var srcMapVal reflect.Value
		var actualSrcKeyReflectVal reflect.Value
		foundSrcKeyInMap := false
		if src.Type().Key().Kind() == reflect.String {
			keyReflect := reflect.ValueOf(srcKeyNameForThisDestField)
			val := src.MapIndex(keyReflect)
			if val.IsValid() {
				srcMapVal = val
				actualSrcKeyReflectVal = keyReflect
				foundSrcKeyInMap = true
			}
		}
		if !foundSrcKeyInMap {
			for _, kVal := range srcMapKeys {
				if srcKeyValToString(kVal) == srcKeyNameForThisDestField {
					srcMapVal = src.MapIndex(kVal)
					actualSrcKeyReflectVal = kVal
					foundSrcKeyInMap = true
					break
				}
			}
		}
		processedDestFields[destFieldEffectiveName] = struct{}{} // Mark as processed (attempted to source)

		if !foundSrcKeyInMap {
			c.logger.Debugf("   -> Key '%s' not found in source map for dest field '%s'.", srcKeyNameForThisDestField, destFieldEffectiveName)
			// If overwrite is true, the final loop will zero this dest field as it wasn't processed by a source.
			continue
		}
		if actualSrcKeyReflectVal.IsValid() {
			usedMapKeys[srcKeyValToString(actualSrcKeyReflectVal)] = struct{}{}
		}

		destVal, destValOk, destValErr := c.getSettableFieldValue(dest, &destFieldInfo)
		if destValErr != nil {
			if c.handlePotentialError(destValErr, ctx) {
				continue
			}
			return destValErr
		}
		if !destValOk {
			continue
		}

		formattedSrcMapVal := srcMapVal
		if formatter, ok := c.formatters[destFieldEffectiveName]; ok {
			if srcMapVal.IsValid() && srcMapVal.CanInterface() {
				formattedResult := formatter(srcMapVal.Interface())
				if formattedResult != nil {
					formattedSrcMapVal = reflect.ValueOf(formattedResult)
					if !formattedSrcMapVal.IsValid() {
						formattedSrcMapVal = srcMapVal
					}
				} else {
					if c.skipNilSrc {
						c.logger.Debugf("     Formatter returned nil for '%s', skipping due to skipNilSrc=true.", destFieldEffectiveName)
						// If overwriting, zero the field as formatter + skipNilSrc effectively "removed" the source value.
						if c.overwrite && destVal.CanSet() {
							destVal.Set(reflect.Zero(destVal.Type()))
						}
						continue
					}
					formattedSrcMapVal = getNilLikeValue(srcMapVal)
				}
			}
		}

		if !c.overwrite && destVal.IsValid() && !(isBasicTypeZero(destVal) || isEmptyValue(destVal)) {
			c.logger.Debugf("   -> MERGE SKIP for field '%s'. Dest field non-empty.", destFieldEffectiveName)
			continue
		}

		srcMapValueIsDynamicallySensitive := c.checkIsSensitive(formattedSrcMapVal)
		combinedSensitivity := destFieldInfo.sensitive || srcMapValueIsDynamicallySensitive
		itemNameForProcessItem := fmt.Sprintf("field '%s' from mapKey '%s'", destFieldEffectiveName, srcKeyNameForThisDestField)
		res := c.processItem(formattedSrcMapVal, itemNameForProcessItem, destVal.Type(), combinedSensitivity, ctx)

		if res.err != nil {
			if c.handlePotentialError(res.err, ctx) {
				continue
			}
			return res.err
		}

		if destVal.CanSet() {
			if res.shouldSet {
				if res.valueToSet.IsValid() {
					destVal.Set(res.valueToSet)
				} else {
					destVal.Set(reflect.Zero(destVal.Type()))
				}
				c.logger.Debugf("   -> Field '%s' set by processItem from map key '%s'.", destFieldEffectiveName, srcKeyNameForThisDestField)
			} else { // processItem decided not to set (e.g. src value was skipped by skipNil, skipZero etc.)
				if c.overwrite { // If overwriting and processItem skipped the value, zero the field.
					destVal.Set(reflect.Zero(destVal.Type()))
					c.logger.Debugf("   -> Field '%s' ZEROED: processItem returned shouldSet=false for map key '%s', and overwrite=true.", destFieldEffectiveName, srcKeyNameForThisDestField)
				} else { // Merge mode: field remains untouched.
					c.logger.Debugf("   -> Field '%s' NOT SET: processItem returned shouldSet=false for map key '%s', and overwrite=false (merge).", destFieldEffectiveName, srcKeyNameForThisDestField)
				}
			}
		} else {
			c.logger.Infof("   Internal error: destVal for field '%s' non-settable.", destFieldEffectiveName)
		}
	}

	if unmappedDestMap.IsValid() { /* ... unmapped logic ... */
		for _, keyVal := range srcMapKeys {
			keyStr := srcKeyValToString(keyVal)
			if _, used := usedMapKeys[keyStr]; used {
				continue
			}
			srcValForUnmapped := src.MapIndex(keyVal)
			if !c.overwrite {
				if existingInUnmapped := unmappedDestMap.MapIndex(keyVal); existingInUnmapped.IsValid() && !isEmptyValue(existingInUnmapped) {
					continue
				}
			}
			itemIsDynamicallySensitive := c.checkIsSensitive(srcValForUnmapped)
			itemName := fmt.Sprintf("unmappedKey '%s'", keyStr)
			res := c.processItem(srcValForUnmapped, itemName, unmappedDestValueType, itemIsDynamicallySensitive, ctx)
			if res.err != nil {
				if c.handlePotentialError(res.err, ctx) {
					continue
				}
				return res.err
			}
			if unmappedDestMap.IsNil() {
				return fmt.Errorf("unmapped map '%s' became nil", unmappedDestFieldName)
			}
			if res.shouldSet {
				if res.valueToSet.IsValid() {
					unmappedDestMap.SetMapIndex(keyVal, res.valueToSet)
				} else {
					unmappedDestMap.SetMapIndex(keyVal, reflect.Zero(unmappedDestValueType))
				}
			} else {
				if c.overwrite { // If overwriting and processItem skipped, ensure key is not in the (newly created) unmapped map.
					// No action needed as map was cleared or is new.
				} // If merging, no action needed.
			}
		}
	}

	if c.overwrite {
		for destFieldEffectiveNameToZero, destFieldInfoToZero := range destInfo.fields {
			if _, ok := processedDestFields[destFieldEffectiveNameToZero]; !ok {
				destValToZero, dvOk, _ := c.getSettableFieldValue(dest, &destFieldInfoToZero)
				if dvOk && destValToZero.CanSet() {
					destValToZero.Set(reflect.Zero(destValToZero.Type()))
					c.logger.Debugf("   Post-iteration: Zeroing untargeted dest field '%s'.", destFieldEffectiveNameToZero)
				}
			}
		}
	}
	c.logger.Debugf("Finished copyMapToStruct (%s -> %s)", src.Type(), dest.Type())
	return nil
}

func (c *Copier) copyMapToSlice(dest, src reflect.Value, ctx *SysContext) error {
	// (Logic unchanged from previous correct version)
	destType := dest.Type()
	destElemType := destType.Elem()
	srcType := src.Type()
	c.logger.Indent(ctx.depth)
	c.logger.Debugf("Copying Map -> Slice (%s -> %s)", srcType, destType)
	if !src.IsValid() || src.IsNil() {
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite && dest.CanSet() && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.MakeSlice(destType, 0, 0))
		}
		return nil
	}
	if !dest.CanSet() {
		if c.overwrite || !dest.IsValid() || dest.IsNil() {
			return fmt.Errorf("dest slice %s not settable", destType)
		}
	}
	srcLen := src.Len()
	var targetSlice reflect.Value
	if c.overwrite || !dest.IsValid() || dest.IsNil() {
		targetSlice = reflect.MakeSlice(destType, 0, srcLen)
		if dest.CanSet() {
			dest.Set(targetSlice)
		}
	} else {
		currentLen := dest.Len()
		currentCap := dest.Cap()
		neededCap := currentLen + srcLen
		if currentCap < neededCap {
			grownSlice := reflect.MakeSlice(destType, currentLen, neededCap)
			if currentLen > 0 {
				reflect.Copy(grownSlice, dest.Slice(0, currentLen))
			}
			targetSlice = grownSlice
			if dest.CanSet() {
				dest.Set(targetSlice)
			}
		} else {
			targetSlice = dest
			targetSlice.SetLen(currentLen)
		}
	}
	if srcLen == 0 {
		return nil
	}
	iter := src.MapRange()
	elementsAdded := 0
	for iter.Next() {
		srcMapKey := iter.Key()
		srcVal := iter.Value()
		itemName := fmt.Sprintf("sliceElemFromMapKey[%v]", srcKeyInterface(srcMapKey))
		itemIsSensitive := c.checkIsSensitive(srcVal)
		res := c.processItem(srcVal, itemName, destElemType, itemIsSensitive, ctx)
		if res.err != nil {
			if c.handlePotentialError(res.err, ctx) {
				continue
			}
			return res.err
		}
		if res.shouldSet {
			if res.valueToSet.IsValid() {
				targetSlice = reflect.Append(targetSlice, res.valueToSet)
			} else {
				targetSlice = reflect.Append(targetSlice, reflect.Zero(destElemType))
			}
			elementsAdded++
		}
	}
	if dest.CanSet() {
		needsSet := false
		if !dest.IsValid() || dest.IsNil() {
			needsSet = true
		} else if dest.Pointer() != targetSlice.Pointer() {
			needsSet = true
		} else if dest.Len() != targetSlice.Len() {
			needsSet = true
		} else if dest.Cap() != targetSlice.Cap() {
			needsSet = true
		}
		if needsSet {
			dest.Set(targetSlice)
		}
	}
	return nil
}
