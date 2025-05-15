// cs.go
package mm

import (
	"fmt"
	// "log" // Keep commented unless TestUnmappedFieldsWrongType specifically needs it
	"reflect"
)

// copyStructDriven copies data from src struct to dest struct.
func (c *Copier) copyStructDriven(dest, src reflect.Value, ctx *SysContext) (err error) {
	srcForInfo := src
	if srcForInfo.Kind() == reflect.Ptr {
		if srcForInfo.IsNil() {
			c.logger.Debug("   copyStructDriven: Source pointer is nil.")
			if c.overwrite { // If source is entirely nil and overwriting, zero all dest fields
				destInfo := c.getTypeInfo(dest.Type())
				for _, dfi := range destInfo.fields {
					// Handle unmapped field: clear it if it's a map, or zero it
					if destInfo.unmappedField != nil && dfi.name == destInfo.unmappedField.name {
						if unmappedMap, ok, _ := c.getSettableFieldValue(dest, &dfi); ok && unmappedMap.CanSet() {
							if unmappedMap.Kind() == reflect.Map {
								unmappedMap.Set(reflect.MakeMap(unmappedMap.Type())) // Clear map
							} else {
								unmappedMap.Set(reflect.Zero(unmappedMap.Type())) // Zero other types
							}
						}
						continue
					}
					dVal, dOk, _ := c.getSettableFieldValue(dest, &dfi)
					if dOk && dVal.CanSet() {
						dVal.Set(reflect.Zero(dVal.Type()))
					}
				}
			}
			return nil
		}
		srcForInfo = srcForInfo.Elem()
	}
	if srcForInfo.Kind() != reflect.Struct {
		return fmt.Errorf("copyStructDriven: source must be a struct or pointer to a struct, got %s", src.Type())
	}

	destInfo := c.getTypeInfo(dest.Type())
	srcInfo := c.getTypeInfo(srcForInfo.Type())

	processedDestFields := make(map[string]struct{})
	var unmappedDestMap reflect.Value
	var unmappedDestFieldName string
	var unmappedDestValueType reflect.Type

	c.logger.Indent(ctx.depth)
	c.logger.Debug("Copying Struct -> Struct (%s -> %s) | Overwrite: %v", srcForInfo.Type(), dest.Type(), c.overwrite)

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
				c.logger.Warn("   Warning: dest unmapped field '%s' (type %s) is not map[string]interface{}.", unmappedDestFieldName, fieldType)
			}
		} else if mapFieldValErr != nil {
			c.logger.Warn("   Error initializing unmapped dest field '%s': %v.", unmappedDestFieldName, mapFieldValErr)
		}
		processedDestFields[unmappedDestFieldName] = struct{}{}
	}

	c.logger.Debug(" Iterating through SOURCE fields of type %s (%d fields).", srcForInfo.Type(), len(srcInfo.fields))
	for srcFieldEffectiveName, srcFieldInfo := range srcInfo.fields {
		c.logger.Debug(" Processing source field (srcEffectiveName: '%s', Go name: '%s')", srcFieldEffectiveName, srcFieldInfo.structField.Name)

		var destFieldInfoFound FieldInfo
		var destFieldFound = false
		var matchedDestFieldEffectiveName string

		// --- Find Destination Field ---
		if explicitMappedDestName, ok := c.fieldMappings[srcFieldInfo.structField.Name]; ok {
			if dfi, found := destInfo.fields[explicitMappedDestName]; found {
				destFieldInfoFound = dfi
				destFieldFound = true
				matchedDestFieldEffectiveName = explicitMappedDestName
			}
		}
		if !destFieldFound {
			if dfi, found := destInfo.fields[srcFieldEffectiveName]; found {
				if unmappedDestFieldName == "" || dfi.name != unmappedDestFieldName {
					destFieldInfoFound = dfi
					destFieldFound = true
					matchedDestFieldEffectiveName = srcFieldEffectiveName
				}
			}
			if !destFieldFound && c.tagResolution {
				potentialTags := append([]string{c.tagName}, c.fallbackTagKeys...)
			outerTagLoop:
				for _, destFiCandidate := range destInfo.fields {
					if (unmappedDestFieldName != "" && destFiCandidate.name == unmappedDestFieldName) || processedDestFields[destFiCandidate.name] != struct{}{} {
						continue
					}
					for _, tagKey := range potentialTags {
						srcTagValStr := srcFieldInfo.structField.Tag.Get(tagKey)
						destTagValStr := destFiCandidate.structField.Tag.Get(tagKey)
						if srcTagValStr != "" && srcTagValStr != "-" && destTagValStr != "" && destTagValStr != "-" {
							srcTagOpts := parseTagOptions(srcTagValStr)
							destTagOpts := parseTagOptions(destTagValStr)
							srcNameFromThisTag, srcNameOK := srcTagOpts["name"]
							destNameFromThisTag, destNameOK := destTagOpts["name"]
							if srcNameOK && destNameOK && srcNameFromThisTag != "" && srcNameFromThisTag == destNameFromThisTag {
								destFieldInfoFound = destFiCandidate
								destFieldFound = true
								matchedDestFieldEffectiveName = destFiCandidate.name
								break outerTagLoop
							}
						}
					}
				}
			}
		}
		// --- End Find Destination Field ---

		// --- Initial Skip Checks (before reading srcVal) ---
		if _, ignore := srcFieldInfo.tagOpts["-"]; ignore {
			c.logger.Debug("   -> SKIPPING Source Field '%s': source field tag '-'.", srcFieldEffectiveName)
			if destFieldFound {
				processedDestFields[matchedDestFieldEffectiveName] = struct{}{}
			}
			continue
		}
		if destFieldFound {
			if _, skipGlobal := c.skipFields[matchedDestFieldEffectiveName]; skipGlobal {
				c.logger.Debug("   -> SKIPPING Dest Field '%s' (globally). Src field '%s' will not be copied.", matchedDestFieldEffectiveName, srcFieldEffectiveName)
				processedDestFields[matchedDestFieldEffectiveName] = struct{}{}
				continue
			}
			if _, ignoreDest := destFieldInfoFound.tagOpts["-"]; ignoreDest {
				c.logger.Debug("   -> SKIPPING Dest Field '%s' (tag '-'). Src field '%s' will not be copied.", matchedDestFieldEffectiveName, srcFieldEffectiveName)
				processedDestFields[matchedDestFieldEffectiveName] = struct{}{}
				continue
			}
			if _, processed := processedDestFields[matchedDestFieldEffectiveName]; processed {
				c.logger.Debug("   -> SKIPPING Dest Field '%s': already processed. Src field '%s' will not be copied.", matchedDestFieldEffectiveName, srcFieldEffectiveName)
				continue
			}
		} else if !unmappedDestMap.IsValid() { // No specific dest field and no unmapped handler
			c.logger.Debug("   -> SKIPPING Source Field '%s': no destination field and no unmapped handler.", srcFieldEffectiveName)
			continue
		}
		// --- End Initial Skip Checks ---

		srcVal, readable := c.readSourceFieldValue(srcForInfo, &srcFieldInfo)
		if !readable {
			c.logger.Debug("   -> SKIPPING Source Field '%s': Unreadable.", srcFieldEffectiveName)
			if destFieldFound {
				processedDestFields[matchedDestFieldEffectiveName] = struct{}{}
			}
			continue
		}

		// OmitEmpty skip for source field
		if _, omit := srcFieldInfo.tagOpts["omitempty"]; omit && isEmptyValueForOmit(srcVal) {
			c.logger.Debug("   -> SKIPPING Source Field '%s' due to 'omitempty' on source and value is empty/zero.", srcFieldEffectiveName)
			if destFieldFound {
				processedDestFields[matchedDestFieldEffectiveName] = struct{}{}
				// If c.overwrite was true, the old logic zeroed the field.
				// NEW: If source is omitempty and omitted, the destination field simply isn't updated from this source.
				// It will retain its original value. The `processedDestFields` marking prevents it from being
				// zeroed by the final "untargeted fields" loop if c.overwrite is true.
				// No action needed here to zero out destValToZero.
			}
			continue
		}

		var itemNameForProcessItem string
		var baseDestTypeForProcessItem reflect.Type
		var destValForOverwriteCheck reflect.Value
		targetIsSpecificField := false

		if destFieldFound {
			targetIsSpecificField = true
			itemNameForProcessItem = matchedDestFieldEffectiveName
			destValCurrent, dvOk, dvErr := c.getSettableFieldValue(dest, &destFieldInfoFound)
			if dvErr != nil {
				errField := fmt.Errorf("getting settable dest field '%s': %w", matchedDestFieldEffectiveName, dvErr)
				if c.handlePotentialError(errField, ctx) {
					processedDestFields[matchedDestFieldEffectiveName] = struct{}{}
					continue
				}
				return errField
			}
			if !dvOk {
				c.logger.Debug("   -> SKIPPING (cannot get settable): Cannot get settable field value for dest '%s'.", matchedDestFieldEffectiveName)
				processedDestFields[matchedDestFieldEffectiveName] = struct{}{}
				continue
			}
			baseDestTypeForProcessItem = destValCurrent.Type()
			destValForOverwriteCheck = destValCurrent
			// Mark as processed only if we are actually going to attempt to set it.
			// Will be marked again if set, or if processItem skips.
		} else if unmappedDestMap.IsValid() {
			targetIsSpecificField = false
			itemNameForProcessItem = srcFieldEffectiveName
			baseDestTypeForProcessItem = unmappedDestValueType
			mapKey := reflect.ValueOf(itemNameForProcessItem)
			if exVal := unmappedDestMap.MapIndex(mapKey); exVal.IsValid() {
				destValForOverwriteCheck = exVal
			}
		} else {
			continue
		}

		if !c.overwrite && destValForOverwriteCheck.IsValid() && !(isBasicTypeZero(destValForOverwriteCheck) || isEmptyValue(destValForOverwriteCheck)) {
			c.logger.Debug("   -> MERGE SKIP for target '%s'. Dest value is non-empty/non-zero.", itemNameForProcessItem)
			if destFieldFound {
				processedDestFields[matchedDestFieldEffectiveName] = struct{}{}
			} // Mark as handled (by merge skip)
			continue
		}

		formattedSrcVal := srcVal
		if targetIsSpecificField { /* ... formatter logic ... */
			if formatter, ok := c.formatters[matchedDestFieldEffectiveName]; ok {
				if !srcVal.CanInterface() {
					c.logger.Warn("   -> Cannot apply formatter for field '%s': source value cannot be interfaced.", matchedDestFieldEffectiveName)
				} else {
					formattedResult := formatter(srcVal.Interface())
					if formattedResult != nil {
						formattedSrcVal = reflect.ValueOf(formattedResult)
						if !formattedSrcVal.IsValid() {
							formattedSrcVal = srcVal
						}
					} else {
						if c.skipNilSrc {
							c.logger.Debug("     Formatter returned nil for '%s', skipping due to skipNilSrc=true.", matchedDestFieldEffectiveName)
							if c.overwrite && destFieldFound {
								dVal, dOk, _ := c.getSettableFieldValue(dest, &destFieldInfoFound)
								if dOk && dVal.CanSet() {
									dVal.Set(reflect.Zero(dVal.Type()))
								}
								processedDestFields[matchedDestFieldEffectiveName] = struct{}{}
							}
							continue
						}
						formattedSrcVal = getNilLikeValue(srcVal)
					}
				}
			}
		}

		fieldTagSensitivity := srcFieldInfo.sensitive
		if destFieldFound {
			fieldTagSensitivity = destFieldInfoFound.sensitive
		}
		dynamicSrcSensitivity := c.checkIsSensitive(formattedSrcVal)
		combinedSensitivity := fieldTagSensitivity || dynamicSrcSensitivity

		res := c.processItem(formattedSrcVal, itemNameForProcessItem, baseDestTypeForProcessItem, combinedSensitivity, ctx)

		if res.err != nil {
			errField := fmt.Errorf("processing src field '%s' for dest target '%s': %w", srcFieldEffectiveName, itemNameForProcessItem, res.err)
			if c.handlePotentialError(errField, ctx) {
				if destFieldFound {
					processedDestFields[matchedDestFieldEffectiveName] = struct{}{}
				}
				continue
			}
			return errField
		}

		// Mark destination field as processed because we attempted to process its source.
		if destFieldFound {
			processedDestFields[matchedDestFieldEffectiveName] = struct{}{}
		}

		if targetIsSpecificField {
			destValToSet, dvOk, _ := c.getSettableFieldValue(dest, &destFieldInfoFound)
			if dvOk && destValToSet.CanSet() {
				if res.shouldSet {
					if res.valueToSet.IsValid() {
						destValToSet.Set(res.valueToSet)
					} else {
						destValToSet.Set(reflect.Zero(destValToSet.Type()))
					}
					c.logger.Debug("   -> Field '%s' set by processItem.", itemNameForProcessItem)
				} else {
					// If processItem decided not to set (e.g. skipNilSrc, skipZeroValue for this item),
					// then the destination field should remain untouched.
					// The global c.overwrite affects whether an actual value from processItem overwrites
					// a non-empty destination, not whether a skipped source zeroes the destination.
					c.logger.Debug("   -> Field '%s' NOT SET by processItem (shouldSet=false due to skip flags on item). Dest field untouched.", itemNameForProcessItem)
				}
			} else {
				c.logger.Error("   Internal error: destVal for field '%s' could not be set after processItem.", itemNameForProcessItem)
			}
		} else if unmappedDestMap.IsValid() {
			mapKey := reflect.ValueOf(itemNameForProcessItem)
			if res.shouldSet {
				if unmappedDestMap.IsNil() {
					return fmt.Errorf("internal error: unmapped dest map '%s' became nil", unmappedDestFieldName)
				}
				if res.valueToSet.IsValid() {
					unmappedDestMap.SetMapIndex(mapKey, res.valueToSet)
				} else {
					unmappedDestMap.SetMapIndex(mapKey, reflect.Zero(unmappedDestValueType))
				}
				c.logger.Debug("   -> Unmapped key '%s' set by processItem.", itemNameForProcessItem)
			} else {
				// If processItem decided not to set for this unmapped key, then do nothing.
				// If overwriting struct, unmapped map was already cleared. If merging, existing key is preserved.
				c.logger.Debug("   -> Unmapped key '%s' NOT SET by processItem (shouldSet=false). Map key untouched or remains absent.", itemNameForProcessItem)
			}
		}
	}

	// Zero out destination fields that were NOT processed/targeted by any source field, only if overwriting.
	if c.overwrite {
		for destFieldEffectiveNameToZero, destFieldInfoToZero := range destInfo.fields {
			if _, ok := processedDestFields[destFieldEffectiveNameToZero]; !ok {
				c.logger.Debug("   Post-src iteration: Zeroing untargeted dest field '%s' (overwrite=true).", destFieldEffectiveNameToZero)
				destValToZero, dvOk, _ := c.getSettableFieldValue(dest, &destFieldInfoToZero)
				if dvOk && destValToZero.CanSet() {
					destValToZero.Set(reflect.Zero(destValToZero.Type()))
				}
			}
		}
	}

	c.logger.Debug("Finished copyStructDriven (%s -> %s)", srcForInfo.Type(), dest.Type())
	return nil
}

// copyStructToMap copies data from a struct to a map.
func (c *Copier) copyStructToMap(dest, src reflect.Value, ctx *SysContext) error {
	srcForInfo := src
	if srcForInfo.Kind() == reflect.Ptr {
		if srcForInfo.IsNil() {
			c.logger.Debug("   copyStructToMap: Source pointer is nil.")
			if c.overwrite && dest.IsValid() && !dest.IsNil() && dest.CanSet() {
				dest.Set(reflect.MakeMap(dest.Type()))
			}
			return nil
		}
		srcForInfo = srcForInfo.Elem()
	}
	if srcForInfo.Kind() != reflect.Struct {
		return fmt.Errorf("copyStructToMap: source must be a struct or pointer to a struct, got %s", src.Type())
	}
	if dest.Kind() != reflect.Map {
		return fmt.Errorf("copyStructToMap: destination must be a map, got %s", dest.Type())
	}
	destType := dest.Type()
	destMapKeyType := destType.Key()
	destValueType := destType.Elem()
	c.logger.Indent(ctx.depth)
	c.logger.Debug("Copying Struct -> Map (%s -> %s)", srcForInfo.Type(), dest.Type())
	if dest.IsNil() {
		if !dest.CanSet() {
			return fmt.Errorf("cannot set nil map dest %s", destType)
		}
		dest.Set(reflect.MakeMap(destType))
	} else if c.overwrite {
		// Clear the map by creating a new one if overwriting
		dest.Set(reflect.MakeMapWithSize(destType, srcForInfo.Type().NumField()))
	}

	if dest.IsNil() { // Re-check if MakeMap failed (e.g. unaddressable dest)
		return fmt.Errorf("map %s init failed or dest not settable", destType)
	}

	srcInfo := c.getTypeInfo(srcForInfo.Type())
	for srcFieldNameFromInfo, srcFieldInfo := range srcInfo.fields {
		destKeyName := srcFieldNameFromInfo
		if mappedName, ok := c.fieldMappings[srcFieldInfo.structField.Name]; ok {
			destKeyName = mappedName
		}
		if _, ignore := srcFieldInfo.tagOpts["-"]; ignore {
			c.logger.Debug("   -> Skipping struct field '%s' (tag '-'): not copied to map.", srcFieldNameFromInfo)
			continue
		}
		if _, skipGlobal := c.skipFields[destKeyName]; skipGlobal {
			c.logger.Debug("   -> Skipping struct field '%s' to map key '%s' (globally).", srcFieldNameFromInfo, destKeyName)
			continue
		}
		srcVal, readable := c.readSourceFieldValue(srcForInfo, &srcFieldInfo)
		if !readable {
			c.logger.Debug("   -> Skipping struct field '%s': unreadable.", srcFieldNameFromInfo)
			continue
		}
		if _, omit := srcFieldInfo.tagOpts["omitempty"]; omit && isEmptyValueForOmit(srcVal) {
			c.logger.Debug("   -> Skipping struct field '%s' to map key '%s': omitempty on source.", srcFieldNameFromInfo, destKeyName)
			continue
		}
		var destKeyVal reflect.Value
		if destMapKeyType.Kind() == reflect.String {
			destKeyVal = reflect.ValueOf(destKeyName)
		} else {
			tempKeyHolder := reflect.New(destMapKeyType).Elem()
			keyConvCtx := &SysContext{depth: ctx.depth + 1, detector: newCycleDetector(), errors: ctx.errors}
			if err := c.copyRecursive(tempKeyHolder, reflect.ValueOf(destKeyName), keyConvCtx); err != nil {
				c.logger.Debug("   -> Skipping map key conversion for '%s': %v", destKeyName, err)
				continue
			}
			destKeyVal = tempKeyHolder
		}

		// In struct->map, if overwrite=false, we still add new keys.
		// The check is only relevant if the key *already exists* in the destination map.
		if !c.overwrite {
			if existingMapVal := dest.MapIndex(destKeyVal); existingMapVal.IsValid() && !isEmptyValue(existingMapVal) {
				c.logger.Debug("   -> Skipping map key '%s': Merge skip, key exists with non-empty value.", destKeyName)
				continue
			}
		}

		srcValIsDynamicallySensitive := c.checkIsSensitive(srcVal)
		combinedSensitivity := srcFieldInfo.sensitive || srcValIsDynamicallySensitive
		itemNameForProcessItem := fmt.Sprintf("mapValueForKey '%s' (fromField '%s')", destKeyName, srcFieldNameFromInfo)
		res := c.processItem(srcVal, itemNameForProcessItem, destValueType, combinedSensitivity, ctx)
		if res.err != nil {
			if c.handlePotentialError(res.err, ctx) {
				continue
			}
			return res.err
		}
		if dest.IsNil() { // Should ideally not happen if set above, but good guard
			return fmt.Errorf("dest map %s became nil during processing", destType)
		}
		if res.shouldSet {
			if res.valueToSet.IsValid() {
				dest.SetMapIndex(destKeyVal, res.valueToSet)
			} else {
				dest.SetMapIndex(destKeyVal, reflect.Zero(destValueType))
			}
			c.logger.Debug("      Set map key '%s' from field '%s' via processItem.", destKeyName, srcFieldNameFromInfo)
		} else {
			// If processItem decided not to set (e.g. due to skipNil on srcVal), then this key-value pair is not added.
			// If c.overwrite was true, map was cleared, so key won't exist.
			// If c.overwrite was false, and key didn't exist, it's still not added. If it did exist and was non-empty, skipped above.
			c.logger.Debug("      Map key '%s' from field '%s' NOT SET by processItem (shouldSet=false).", destKeyName, srcFieldNameFromInfo)
		}
	}
	return nil
}

// copyMap copies data from a source map to a destination map.
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
	c.logger.Debug("Copying Map -> Map (%s -> %s)", srcType, destType)
	if !src.IsValid() || src.IsNil() {
		if c.skipNilSrc {
			c.logger.Debug("   Source map is nil, skipping (skipNilSrc=true).")
			return nil
		}
		// If not skipping nil source, and overwriting, clear the destination map.
		if c.overwrite && dest.CanSet() && dest.IsValid() && !dest.IsNil() {
			c.logger.Debug("   Source map is nil, zeroing destination map (setting to new empty map).")
			dest.Set(reflect.MakeMap(destType))
		} else {
			c.logger.Debug("   Source map is nil, not skipping, not overwriting or dest not settable/valid. Dest unchanged.")
		}
		return nil
	}

	if dest.IsNil() {
		if !dest.CanSet() {
			return fmt.Errorf("cannot set nil map dest %s", destType)
		}
		c.logger.Debug("   Initializing nil destination map %s.", destType)
		dest.Set(reflect.MakeMapWithSize(destType, srcLen))
		if dest.IsNil() { // Check if MakeMap succeeded
			return fmt.Errorf("map %s init failed or dest not settable", destType)
		}
	} else if c.overwrite {
		c.logger.Debug("   Overwriting destination map %s (clearing).", destType)
		dest.Set(reflect.MakeMapWithSize(destType, srcLen)) // Clear by creating new
	}

	if dest.IsNil() { // Final check after potential init/overwrite
		return fmt.Errorf("map %s is nil after setup, cannot copy elements", destType)
	}

	if srcLen == 0 {
		c.logger.Debug("   Source map is empty, nothing to copy.")
		return nil
	}
	destKeyType := destType.Key()
	destValType := destType.Elem()
	iter := src.MapRange()
	for iter.Next() {
		srcKey, srcVal := iter.Key(), iter.Value()

		// Copy/convert source key to destination key type
		destKeyHolder := reflect.New(destKeyType).Elem()
		// Use a fresh cycle detector for key copying, as keys are usually simple and less prone to complex cycles
		// with the main data graph, but could have their own internal complexities if they are structs.
		keyCopyCtx := &SysContext{depth: ctx.depth + 1, detector: newCycleDetector(), errors: ctx.errors}
		if err := c.copyRecursive(destKeyHolder, srcKey, keyCopyCtx); err != nil {
			keyErr := fmt.Errorf("failed to copy/convert map key '%v' (type %s) to type %s: %w", srcKeyInterface(srcKey), srcKey.Type(), destKeyType, err)
			if c.handlePotentialError(keyErr, ctx) {
				continue
			}
			return keyErr
		}
		destKey := destKeyHolder

		if !c.overwrite {
			if existingVal := dest.MapIndex(destKey); existingVal.IsValid() && !isEmptyValue(existingVal) {
				c.logger.Debug("      Skipping map key '%v': Overwrite=false and key exists with non-empty value.", srcKeyInterface(destKey))
				continue
			}
		}
		itemName := fmt.Sprintf("mapValue[key:%s]", srcKeyInterface(destKey))
		srcValIsDynamicallySensitive := c.checkIsSensitive(srcVal)
		// Note: Key sensitivity is not explicitly handled here, but copyRecursive for the key might handle it.
		// For map values, sensitivity is checked on srcVal.

		res := c.processItem(srcVal, itemName, destValType, srcValIsDynamicallySensitive, ctx)
		if res.err != nil {
			// Error already handled by processItem if it was collected.
			// If not collected, then this is a direct error to return.
			return fmt.Errorf("processing value for map key '%v': %w", srcKeyInterface(destKey), res.err)
		}
		if dest.IsNil() { // Guard against dest becoming nil during concurrent ops (though not expected here)
			return fmt.Errorf("dest map %s became nil during copyMap iteration", destType)
		}
		if res.shouldSet {
			if res.valueToSet.IsValid() {
				dest.SetMapIndex(destKey, res.valueToSet)
			} else {
				dest.SetMapIndex(destKey, reflect.Zero(destValType))
			}
			c.logger.Debug("      Set map key '%v' via processItem.", srcKeyInterface(destKey))
		} else {
			// If processItem decided not to set (e.g. due to skipNil on srcVal), then this key-value pair is not added/updated.
			// If c.overwrite was true, map was cleared, so key won't exist unless explicitly set.
			// If c.overwrite was false, and key didn't exist, it's still not added. If it did exist and was non-empty, skipped above.
			c.logger.Debug("      Map value for key '%v' NOT SET by processItem (shouldSet=false).", srcKeyInterface(destKey))
		}
	}
	return nil
}
