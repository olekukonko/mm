package copier

import (
	"fmt"
	"reflect"
)

func (c *Copier) copyMapToStruct(dest reflect.Value, src reflect.Value, ctx *SysContext) error {
	destInfo := c.getTypeInfo(dest.Type())
	srcMapKeys := src.MapKeys()
	usedMapKeys := make(map[string]struct{})
	destFieldsProcessedOrSkipped := make(map[string]struct{})

	var unmappedDestMap reflect.Value
	var unmappedDestFieldName string
	var unmappedDestValueType reflect.Type

	c.logger.Indent(ctx.depth)
	c.logger.Debugf("Copying Map -> Struct (%s -> %s) | Overwrite: %v", src.Type(), dest.Type(), c.overwrite)

	if destInfo.unmappedField != nil {
		unmappedDestFieldName = destInfo.unmappedField.name
		destFieldsProcessedOrSkipped[unmappedDestFieldName] = struct{}{}
		mapFieldVal, mapFieldValOk, mapFieldValErr := c.getSettableFieldValue(dest, destInfo.unmappedField)

		if mapFieldValErr == nil && mapFieldValOk {
			fieldType := mapFieldVal.Type()
			isValidUnmappedType := fieldType.Kind() == reflect.Map &&
				fieldType.Key().Kind() == reflect.String &&
				(fieldType.Elem().Kind() == reflect.Interface || fieldType.Elem() == reflect.TypeOf((*interface{})(nil)).Elem())
			if isValidUnmappedType {
				if mapFieldVal.IsNil() {
					mapFieldVal.Set(reflect.MakeMap(fieldType))
					c.logger.Debugf("   Initialized nil unmapped field '%s'.", unmappedDestFieldName)
				} else if c.overwrite {
					mapFieldVal.Set(reflect.MakeMap(fieldType))
					c.logger.Debugf("   Cleared existing unmapped field '%s' for overwrite.", unmappedDestFieldName)
				}
				unmappedDestMap = mapFieldVal
				unmappedDestValueType = fieldType.Elem()
			} else {
				warnMsg := fmt.Sprintf("[mm warning] %s: dest unmapped field '%s' has type %s.", ErrUnmappedFieldType, unmappedDestFieldName, fieldType)
				c.logger.Warnf("   Warning: %s", warnMsg)
			}
		} else if mapFieldValErr != nil {
			errUnmapped := fmt.Errorf("getting unmapped dest field '%s': %w", unmappedDestFieldName, mapFieldValErr)
			if c.handlePotentialError(errUnmapped, ctx) {
			} else if mapFieldValErr != ErrUnexportedField {
				c.logger.Warnf("   Error initializing unmapped dest field '%s': %v.", unmappedDestFieldName, mapFieldValErr)
			} else {
				c.logger.Debugf("   Unmapped dest field '%s' is unexported or not settable.", unmappedDestFieldName)
			}
		}
	}

	for destFieldEffectiveName, destFieldInfo := range destInfo.fields {
		if destFieldEffectiveName == unmappedDestFieldName {
			continue
		}
		c.logger.Debugf(" Processing destination field '%s' (Go name: '%s')", destFieldEffectiveName, destFieldInfo.structField.Name)

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
				if InterfaceToString(kVal) == srcKeyNameForThisDestField {
					srcMapVal = src.MapIndex(kVal)
					actualSrcKeyReflectVal = kVal
					foundSrcKeyInMap = true
					break
				}
			}
		}

		if !foundSrcKeyInMap {
			c.logger.Debugf("   -> Key '%s' not found in source map for dest field '%s'. Field value preserved (or zeroed if overwrite and not otherwise set).", srcKeyNameForThisDestField, destFieldEffectiveName)
			continue
		}

		destFieldsProcessedOrSkipped[destFieldEffectiveName] = struct{}{}
		if actualSrcKeyReflectVal.IsValid() {
			usedMapKeys[InterfaceToString(actualSrcKeyReflectVal)] = struct{}{}
		}

		if _, skip := c.skipFields[destFieldEffectiveName]; skip {
			c.logger.Debugf("   -> Skipping dest field '%s' (explicit global skip). Field value preserved.", destFieldEffectiveName)
			continue
		}
		if _, ignore := destFieldInfo.tagOpts["-"]; ignore {
			c.logger.Debugf("   -> Skipping dest field '%s' (tag '-' on destination field). Field value preserved.", destFieldEffectiveName)
			continue
		}

		destVal, destValOk, destValErr := c.getSettableFieldValue(dest, &destFieldInfo)
		if destValErr != nil {
			if c.handlePotentialError(destValErr, ctx) {
				continue
			}
			return destValErr
		}
		if !destValOk {
			c.logger.Debugf("   -> Dest field '%s' not settable. Field value preserved.", destFieldEffectiveName)
			continue
		}

		formattedSrcMapVal := srcMapVal
		if formatter, ok := c.formatters[destFieldEffectiveName]; ok {
			if srcMapVal.IsValid() && srcMapVal.CanInterface() {
				formattedResult := formatter(srcMapVal.Interface())
				if formattedResult != nil {
					newVal := reflect.ValueOf(formattedResult)
					formattedSrcMapVal = newVal
				} else {
					if c.skipNilSrc {
						c.logger.Debugf("     Formatter returned nil for '%s', skipping due to skipNilSrc=true. Field value preserved.", destFieldEffectiveName)
						continue
					}
					formattedSrcMapVal = GetNilLikeValue(srcMapVal)
				}
			}
		}

		if !c.overwrite && destVal.IsValid() && destVal.CanInterface() {
			c.logger.Debugf("   Merge Check: Dest field '%s' current value for check: '%v' (type %s)", destFieldEffectiveName, destVal.Interface(), destVal.Type())
		}

		if !c.overwrite && destVal.IsValid() {
			isDestNonEmptyNonZero := !(IsBasicTypeZero(destVal) || IsEmptyValue(destVal))
			if isDestNonEmptyNonZero {
				var valStr string
				if destVal.CanInterface() {
					valStr = fmt.Sprintf("'%v'", destVal.Interface())
				} else {
					valStr = "<uninterfaceable>"
				}
				c.logger.Debugf("   -> MERGE SKIP for field '%s'. Dest field (%s, type %s) non-empty/non-zero.",
					destFieldEffectiveName, valStr, destVal.Type())
				continue
			}
		}

		srcMapValueIsDynamicallySensitive := c.checkIsSensitive(formattedSrcMapVal)
		combinedSensitivity := destFieldInfo.sensitive || srcMapValueIsDynamicallySensitive

		res := c.processItem(formattedSrcMapVal, destFieldEffectiveName, destVal.Type(), combinedSensitivity, ctx)

		if res.err != nil {
			if c.handlePotentialError(res.err, ctx) {
				continue
			}
			return res.err
		}

		if res.shouldSet {
			if res.valueToSet.IsValid() {
				destVal.Set(res.valueToSet)
			} else {
				destVal.Set(reflect.Zero(destVal.Type()))
			}
			c.logger.Debugf("   -> Field '%s' SET by processItem from map key '%s'.", destFieldEffectiveName, srcKeyNameForThisDestField)
		} else {
			c.logger.Debugf("   -> Field '%s' NOT SET: processItem returned shouldSet=false for map key '%s'. Dest field value preserved.", destFieldEffectiveName, srcKeyNameForThisDestField)
		}
	}

	if unmappedDestMap.IsValid() {
		for _, keyVal := range srcMapKeys {
			keyStr := InterfaceToString(keyVal)
			if _, used := usedMapKeys[keyStr]; used {
				continue
			}

			srcValForUnmapped := src.MapIndex(keyVal)

			if !c.overwrite {
				if existingInUnmapped := unmappedDestMap.MapIndex(keyVal); existingInUnmapped.IsValid() && !IsEmptyValue(existingInUnmapped) {
					c.logger.Debugf("   -> UNMAPPED MERGE SKIP for key '%s'. Dest key exists and non-empty.", keyStr)
					continue
				}
			}

			itemIsDynamicallySensitive := c.checkIsSensitive(srcValForUnmapped)
			itemName := keyStr
			res := c.processItem(srcValForUnmapped, itemName, unmappedDestValueType, itemIsDynamicallySensitive, ctx)

			if res.err != nil {
				unmappedErr := fmt.Errorf("processing unmapped key '%s': %w", keyStr, res.err)
				if c.handlePotentialError(unmappedErr, ctx) {
					continue
				}
				return unmappedErr
			}

			if unmappedDestMap.IsNil() {
				return fmt.Errorf("internal error: unmapped map '%s' became nil", unmappedDestFieldName)
			}

			if res.shouldSet {
				if res.valueToSet.IsValid() {
					unmappedDestMap.SetMapIndex(keyVal, res.valueToSet)
				} else {
					unmappedDestMap.SetMapIndex(keyVal, reflect.Zero(unmappedDestValueType))
				}
				c.logger.Debugf("   -> Unmapped key '%s' SET by processItem.", keyStr)
			} else {
				if c.overwrite {
					unmappedDestMap.SetMapIndex(keyVal, reflect.Value{})
					c.logger.Debugf("   -> Unmapped key '%s' REMOVED from unmapped map (overwrite=true, processItem.shouldSet=false).", keyStr)
				} else {
					c.logger.Debugf("   -> Unmapped key '%s' NOT SET by processItem (shouldSet=false). Key value in unmapped map preserved if merging.", keyStr)
				}
			}
		}
	}

	if c.overwrite {
		for destFieldToConsiderZeroing, destFieldInfoToZero := range destInfo.fields {
			if destFieldToConsiderZeroing == unmappedDestFieldName {
				continue
			}
			if _, wasHandled := destFieldsProcessedOrSkipped[destFieldToConsiderZeroing]; !wasHandled {
				c.logger.Debugf("   Post-iteration: Zeroing untargeted dest field '%s' (overwrite=true, no src key mapped or processed).", destFieldToConsiderZeroing)
				destValToZero, dvOk, _ := c.getSettableFieldValue(dest, &destFieldInfoToZero)
				if dvOk && destValToZero.CanSet() {
					destValToZero.Set(reflect.Zero(destValToZero.Type()))
				}
			}
		}
	}
	c.logger.Debugf("Finished copyMapToStruct (%s -> %s)", src.Type(), dest.Type())
	return nil
}

func (c *Copier) copyMapToSlice(dest, src reflect.Value, ctx *SysContext) error {
	destType := dest.Type()
	destElemType := destType.Elem()
	srcType := src.Type()
	c.logger.Indent(ctx.depth)
	c.logger.Debugf("Copying Map -> Slice (%s -> %s)", srcType, destType)

	if !src.IsValid() || src.IsNil() {
		if c.skipNilSrc {
			c.logger.Debugf("   Source map is nil and skipNilSrc=true, destination slice preserved.")
			return nil
		}
		if c.overwrite && dest.CanSet() && dest.IsValid() && !dest.IsNil() {
			c.logger.Debugf("   Source map is nil, overwriting destination slice with empty slice.")
			dest.Set(reflect.MakeSlice(destType, 0, 0))
		} else {
			c.logger.Debugf("   Source map is nil, not overwriting or dest not settable. Destination slice preserved.")
		}
		return nil
	}

	if !dest.CanSet() && (c.overwrite || !dest.IsValid() || dest.IsNil()) {
		return fmt.Errorf("dest slice %s not settable for required creation/clear", destType)
	}

	srcLen := src.Len()
	var targetSlice reflect.Value

	if c.overwrite || !dest.IsValid() || dest.IsNil() {
		targetSlice = reflect.MakeSlice(destType, 0, srcLen)
		if dest.CanSet() {
			dest.Set(targetSlice)
		}
		c.logger.Debugf("   Overwrite/NilDest: Created new target slice for dest %s (len 0, cap %d).", destType, srcLen)
	} else {
		targetSlice = dest
		if targetSlice.Cap() < targetSlice.Len()+srcLen {
			grownSlice := reflect.MakeSlice(destType, targetSlice.Len(), targetSlice.Len()+srcLen)
			if targetSlice.Len() > 0 {
				reflect.Copy(grownSlice, targetSlice.Slice(0, targetSlice.Len()))
			}
			targetSlice = grownSlice
			if dest.CanSet() {
				dest.Set(targetSlice)
			}
			c.logger.Debugf("   Merge: Grew target slice for dest %s to len %d, cap %d.", destType, targetSlice.Len(), targetSlice.Cap())
		} else {
			c.logger.Debugf("   Merge: Using existing dest slice %s (len %d, cap %d).", destType, targetSlice.Len(), targetSlice.Cap())
		}
	}

	if srcLen == 0 {
		if c.overwrite && dest.IsValid() && !dest.IsNil() && dest.Len() > 0 {
			if dest.CanSet() {
				dest.SetLen(0)
				c.logger.Debugf("   Source map empty, overwrite=true. Cleared dest slice %s to len 0.", destType)
			}
		} else {
			c.logger.Debugf("   Source map empty, nothing to append to slice.")
		}
		return nil
	}

	iter := src.MapRange()
	elementsAdded := 0
	for iter.Next() {
		srcVal := iter.Value()

		itemIsSensitive := c.checkIsSensitive(srcVal)
		res := c.processItem(srcVal, elementsAdded, destElemType, itemIsSensitive, ctx)

		if res.err != nil {
			mapToSliceErr := fmt.Errorf("processing map value for slice element %d: %w", elementsAdded, res.err)
			if c.handlePotentialError(mapToSliceErr, ctx) {
				continue
			}
			return mapToSliceErr
		}

		if res.shouldSet {
			if targetSlice.IsNil() && dest.CanSet() {
				c.logger.Warnf("   Target slice became nil unexpectedly, re-initializing for append.")
				targetSlice = reflect.MakeSlice(destType, 0, srcLen-elementsAdded)
				dest.Set(targetSlice)
			}

			if res.valueToSet.IsValid() {
				targetSlice = reflect.Append(targetSlice, res.valueToSet)
			} else {
				targetSlice = reflect.Append(targetSlice, reflect.Zero(destElemType))
			}
			elementsAdded++
		}
	}

	if dest.CanSet() {
		needsSet := !dest.IsValid() || dest.IsNil()
		if !needsSet && dest.IsValid() && !dest.IsNil() {
			if dest.Pointer() != targetSlice.Pointer() || dest.Len() != targetSlice.Len() || dest.Cap() != targetSlice.Cap() {
				needsSet = true
			}
		}
		if needsSet {
			dest.Set(targetSlice)
			c.logger.Debugf("   Finalized dest slice %s (len %d, cap %d).", destType, targetSlice.Len(), targetSlice.Cap())
		}
	} else if targetSlice.Pointer() != dest.Pointer() {
		c.logger.Warnf("Map -> Slice: dest slice %s was not settable and copy was performed into a new slice. Original dest variable is unchanged.", destType)
	}
	return nil
}

func (c *Copier) copyMap(dest, src reflect.Value, ctx *SysContext) error {
	if src.Kind() != reflect.Map {
		return fmt.Errorf("copyMap: source must be map, got %s", src.Type())
	}
	if dest.Kind() != reflect.Map {
		return fmt.Errorf("copyMap: destination must be map, got %s", dest.Type())
	}
	destType := dest.Type()
	srcType := src.Type()
	srcLen := 0
	if src.IsValid() && !src.IsNil() {
		srcLen = src.Len()
	}
	c.logger.Indent(ctx.depth)
	c.logger.Debugf("Copying Map -> Map (%s -> %s)", srcType, destType)

	originalDestMapSnapshot := make(map[interface{}]reflect.Value)
	if dest.IsValid() && !dest.IsNil() {
		iter := dest.MapRange()
		for iter.Next() {
			keyCopy := reflect.New(iter.Key().Type()).Elem()
			keyCopy.Set(iter.Key())
			originalDestMapSnapshot[keyCopy.Interface()] = iter.Value()
		}
	}

	if !src.IsValid() || src.IsNil() {
		if c.skipNilSrc {
			c.logger.Debugf("   Source map is nil, skipping (skipNilSrc=true). Dest map preserved.")
			return nil
		}
		if c.overwrite && dest.CanSet() {
			c.logger.Debugf("   Source map is nil, zeroing destination map (setting to new empty map).")
			dest.Set(reflect.MakeMap(destType))
		} else {
			c.logger.Debugf("   Source map is nil, not skipping, not overwriting or dest not settable/valid. Dest unchanged.")
		}
		return nil
	}

	if dest.IsNil() {
		if !dest.CanSet() {
			return fmt.Errorf("cannot set nil map dest %s", destType)
		}
		c.logger.Debugf("   Initializing nil destination map %s.", destType)
		dest.Set(reflect.MakeMapWithSize(destType, srcLen))
		if dest.IsNil() {
			return fmt.Errorf("map %s init failed or dest not settable", destType)
		}
	}

	if dest.IsNil() {
		return fmt.Errorf("map %s is nil after setup, cannot copy elements", destType)
	}

	if srcLen == 0 {
		if c.overwrite {
			c.logger.Debugf("   Source map empty and overwriting. Clearing destination map %s.", destType)
			if dest.IsValid() && !dest.IsNil() {
				iter := dest.MapRange()
				var keysToClear []reflect.Value
				for iter.Next() {
					keysToClear = append(keysToClear, iter.Key())
				}
				for _, k := range keysToClear {
					dest.SetMapIndex(k, reflect.Value{})
				}
			}
		} else {
			c.logger.Debugf("   Source map is empty, nothing to copy.")
		}
		return nil
	}

	destKeyType := destType.Key()
	destValType := destType.Elem()
	iter := src.MapRange()

	processedSourceKeys := make(map[interface{}]struct{})

	for iter.Next() {
		srcKey, srcVal := iter.Key(), iter.Value()

		destKeyHolder := reflect.New(destKeyType).Elem()
		keyCopyCtx := c.acquireSysContext()
		keyCopyCtx.depth = ctx.depth + 1

		if errKey := c.CopyRecursive(destKeyHolder, srcKey, keyCopyCtx); errKey != nil {
			c.releaseSysContext(keyCopyCtx)
			keyErr := fmt.Errorf("failed to copy/convert map key '%v' (type %s) to type %s: %w", InterfaceKeySrc(srcKey), srcKey.Type(), destKeyType, errKey)
			if c.handlePotentialError(keyErr, ctx) {
				continue
			}
			return keyErr
		}
		c.releaseSysContext(keyCopyCtx)

		destKey := destKeyHolder
		if destKey.IsValid() {
			processedSourceKeys[destKey.Interface()] = struct{}{}
		} else {
			c.logger.Debugf("      Skipping due to invalid destination key from source key '%v'.", InterfaceKeySrc(srcKey))
			continue
		}

		if !c.overwrite {
			if existingVal := dest.MapIndex(destKey); existingVal.IsValid() && !IsEmptyValue(existingVal) {
				c.logger.Debugf("      Skipping map key '%v': Overwrite=false and key exists with non-empty value.", InterfaceKeySrc(destKey))
				continue
			}
		}

		srcValIsDynamicallySensitive := c.checkIsSensitive(srcVal)

		res := c.processItem(srcVal, destKey.Interface(), destValType, srcValIsDynamicallySensitive, ctx)

		if res.err != nil {
			errMapItem := fmt.Errorf("processing value for map key '%v': %w", InterfaceKeySrc(destKey), res.err)
			if c.handlePotentialError(errMapItem, ctx) {
				continue
			}
			return errMapItem
		}

		if dest.IsNil() {
			return fmt.Errorf("dest map %s became nil during copyMap iteration", destType)
		}

		if res.shouldSet {
			if res.valueToSet.IsValid() {
				dest.SetMapIndex(destKey, res.valueToSet)
			} else {
				dest.SetMapIndex(destKey, reflect.Zero(destValType))
			}
			c.logger.Debugf("      Set map key '%v' via processItem.", InterfaceKeySrc(destKey))
		} else {
			if c.overwrite {
				if originalVal, ok := originalDestMapSnapshot[destKey.Interface()]; ok {
					dest.SetMapIndex(destKey, originalVal)
					c.logger.Debugf("      Map value for key '%v' NOT SET by processItem (overwrite=true, skipped src), restored original dest value.", InterfaceKeySrc(destKey))
				} else {
					dest.SetMapIndex(destKey, reflect.Value{})
					c.logger.Debugf("      Map value for key '%v' NOT SET by processItem (overwrite=true, skipped src), key removed or remains absent from dest.", InterfaceKeySrc(destKey))
				}
			} else {
				c.logger.Debugf("      Map value for key '%v' NOT SET by processItem (merge, skipped src). Dest value preserved.", InterfaceKeySrc(destKey))
			}
		}
	}

	if c.overwrite {
		currentKeysInDest := dest.MapKeys()
		for _, k := range currentKeysInDest {
			if _, srcProvidedThisKey := processedSourceKeys[k.Interface()]; !srcProvidedThisKey {
				c.logger.Debugf("   Overwrite Cleanup: Removing key '%v' from dest map as it was not processed from source map.", k.Interface())
				dest.SetMapIndex(k, reflect.Value{})
			}
		}
	}
	return nil
}
