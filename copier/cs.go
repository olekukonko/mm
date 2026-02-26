package copier

import (
	"fmt"
	"reflect"
	"runtime"
	"unsafe"

	"github.com/olekukonko/mm/cycle"
)

type fastFieldAccess struct {
	index  []int
	offset uintptr
	typ    reflect.Type
	setter func(unsafe.Pointer, reflect.Value)
	getter func(unsafe.Pointer) reflect.Value
}

func (c *Copier) copyStructDriven(dest, src reflect.Value, ctx *SysContext) (err error) {
	srcForInfo := src
	if srcForInfo.Kind() == reflect.Ptr {
		if srcForInfo.IsNil() {
			c.logger.Debugf("   copyStructDriven: Source pointer is nil.")
			if c.overwrite {
				destInfo := c.getTypeInfo(dest.Type())
				for destFieldEffectiveName, dfi := range destInfo.fields {
					if destInfo.unmappedField != nil && destFieldEffectiveName == destInfo.unmappedField.name {
						if unmappedMap, ok, _ := c.getSettableFieldValue(dest, &dfi); ok && unmappedMap.CanSet() {
							if unmappedMap.Kind() == reflect.Map {
								unmappedMap.Set(reflect.MakeMap(unmappedMap.Type()))
							} else {
								unmappedMap.Set(reflect.Zero(unmappedMap.Type()))
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
	destFieldsProcessedOrSkipped := make(map[string]struct{})

	var unmappedDestMap reflect.Value
	var unmappedDestFieldName string
	var unmappedDestValueType reflect.Type

	c.logger.Indent(ctx.depth)
	c.logger.Debugf("Copying Struct -> Struct (%s -> %s) | Overwrite: %v", srcForInfo.Type(), dest.Type(), c.overwrite)

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
				c.logger.Warnf("   Warning: dest unmapped field '%s' (type %s) is not map[string]interface{}.", unmappedDestFieldName, fieldType)
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

	c.logger.Debugf(" Iterating through SOURCE fields of type %s (%d fields).", srcForInfo.Type(), len(srcInfo.fields))

	for srcFieldEffectiveName, srcFieldInfo := range srcInfo.fields {
		c.logger.Debugf(" Processing source field (srcEffectiveName: '%s', Go name: '%s')", srcFieldEffectiveName, srcFieldInfo.structField.Name)

		var destFieldInfoFound FieldInfo
		var destFieldFound = false
		var matchedDestFieldEffectiveName string

		if explicitDestName, mapOk := c.fieldMappings[srcFieldInfo.structField.Name]; mapOk {
			if dfi, found := destInfo.fields[explicitDestName]; found {
				if _, alreadyClaimed := destFieldsProcessedOrSkipped[explicitDestName]; !alreadyClaimed {
					destFieldInfoFound = dfi
					destFieldFound = true
					matchedDestFieldEffectiveName = explicitDestName
					c.logger.Debugf("   Matched dest field '%s' via explicit mapping from src field '%s'.", matchedDestFieldEffectiveName, srcFieldInfo.structField.Name)
				} else {
					c.logger.Debugf("   Dest field '%s' (from explicit map for src '%s') was already handled. Skipping.", explicitDestName, srcFieldInfo.structField.Name)
					continue
				}
			}
		}

		if !destFieldFound {
			if dfi, found := destInfo.fields[srcFieldEffectiveName]; found {
				if unmappedDestFieldName == "" || dfi.name != unmappedDestFieldName {
					if _, alreadyClaimed := destFieldsProcessedOrSkipped[srcFieldEffectiveName]; !alreadyClaimed {
						destFieldInfoFound = dfi
						destFieldFound = true
						matchedDestFieldEffectiveName = srcFieldEffectiveName
						c.logger.Debugf("   Matched dest field '%s' by direct name match with src field '%s'.", matchedDestFieldEffectiveName, srcFieldEffectiveName)
					} else {
						c.logger.Debugf("   Dest field '%s' (matching src name '%s') was already handled. Skipping.", srcFieldEffectiveName, srcFieldEffectiveName)
						continue
					}
				}
			}
		}

		if !destFieldFound && c.tagResolution {
			potentialTags := append([]string{c.tagName}, c.fallbackTagKeys...)
		outerTagLoopResolve:
			for _, destFiCandidate := range destInfo.fields {
				if unmappedDestFieldName != "" && destFiCandidate.name == unmappedDestFieldName {
					continue
				}
				if _, alreadyClaimed := destFieldsProcessedOrSkipped[destFiCandidate.name]; alreadyClaimed {
					continue
				}
				for _, tagKey := range potentialTags {
					srcTagValStr := srcFieldInfo.structField.Tag.Get(tagKey)
					destTagValStr := destFiCandidate.structField.Tag.Get(tagKey)
					if srcTagValStr != "" && srcTagValStr != "-" && destTagValStr != "" && destTagValStr != "-" {
						srcTagOpts := ParseTagOptions(srcTagValStr)
						destTagOpts := ParseTagOptions(destTagValStr)
						srcNameFromThisTag, srcNameOK := srcTagOpts["name"]
						destNameFromThisTag, destNameOK := destTagOpts["name"]
						if srcNameOK && destNameOK && srcNameFromThisTag != "" && srcNameFromThisTag == destNameFromThisTag {
							if _, alreadyClaimedByHigherPriority := destFieldsProcessedOrSkipped[destFiCandidate.name]; !alreadyClaimedByHigherPriority {
								destFieldInfoFound = destFiCandidate
								destFieldFound = true
								matchedDestFieldEffectiveName = destFiCandidate.name
								c.logger.Debugf("   Matched dest field '%s' via tag resolution (tag '%s', common name '%s') from src field '%s'.", matchedDestFieldEffectiveName, tagKey, srcNameFromThisTag, srcFieldEffectiveName)
								break outerTagLoopResolve
							}
						}
					}
				}
			}
		}

		if destFieldFound {
			destFieldsProcessedOrSkipped[matchedDestFieldEffectiveName] = struct{}{}
		}

		if _, ignore := srcFieldInfo.tagOpts["-"]; ignore {
			c.logger.Debugf("   -> SKIPPING Source Field '%s': source field tag '-'. Dest field '%s' preserved.", srcFieldEffectiveName, matchedDestFieldEffectiveName)
			continue
		}

		if destFieldFound {
			if _, skipGlobal := c.skipFields[matchedDestFieldEffectiveName]; skipGlobal {
				c.logger.Debugf("   -> SKIPPING Dest Field '%s' (globally). Src field '%s' not copied. Dest field preserved.", matchedDestFieldEffectiveName, srcFieldEffectiveName)
				continue
			}
			if _, ignoreDest := destFieldInfoFound.tagOpts["-"]; ignoreDest {
				c.logger.Debugf("   -> SKIPPING Dest Field '%s' (tag '-'). Src field '%s' not copied. Dest field preserved.", matchedDestFieldEffectiveName, srcFieldEffectiveName)
				continue
			}
		} else if !unmappedDestMap.IsValid() {
			c.logger.Debugf("   -> SKIPPING Source Field '%s': no specific destination field and no unmapped handler.", srcFieldEffectiveName)
			continue
		}

		srcVal, readable := c.readSourceFieldValue(srcForInfo, &srcFieldInfo)
		if !readable {
			c.logger.Debugf("   -> SKIPPING Source Field '%s': Unreadable. Dest field '%s' preserved.", srcFieldEffectiveName, matchedDestFieldEffectiveName)
			continue
		}

		if _, omit := srcFieldInfo.tagOpts["omitempty"]; omit && IsEmptyValueForOmit(srcVal) {
			c.logger.Debugf("   -> SKIPPING Source Field '%s' due to 'omitempty'. Dest field '%s' preserved.", srcFieldEffectiveName, matchedDestFieldEffectiveName)
			continue
		}

		var itemNameForProcessItem string
		var baseDestTypeForProcessItem reflect.Type
		var destValForOverwriteCheck reflect.Value

		if destFieldFound {
			itemNameForProcessItem = matchedDestFieldEffectiveName
			destValCurrent, dvOk, dvErr := c.getSettableFieldValue(dest, &destFieldInfoFound)
			if dvErr != nil {
				if c.handlePotentialError(dvErr, ctx) {
					continue
				}
				return dvErr
			}
			if !dvOk {
				c.logger.Debugf("   -> Dest field '%s' not settable. Field value preserved.", itemNameForProcessItem)
				continue
			}
			baseDestTypeForProcessItem = destValCurrent.Type()
			destValForOverwriteCheck = destValCurrent
		} else if unmappedDestMap.IsValid() {
			itemNameForProcessItem = srcFieldEffectiveName
			baseDestTypeForProcessItem = unmappedDestValueType
			mapKey := reflect.ValueOf(itemNameForProcessItem)
			if exVal := unmappedDestMap.MapIndex(mapKey); exVal.IsValid() {
				destValForOverwriteCheck = exVal
			}
		} else {
			c.logger.Debugf("   -> INTERNAL SKIP: Source Field '%s': No valid target (should have been caught).", srcFieldEffectiveName)
			continue
		}

		if !c.overwrite && destValForOverwriteCheck.IsValid() && destValForOverwriteCheck.CanInterface() {
			c.logger.Debugf("   Merge Check: Dest field/key '%s' current value for check: '%v' (type %s)", itemNameForProcessItem, destValForOverwriteCheck.Interface(), destValForOverwriteCheck.Type())
		}

		if !c.overwrite && destValForOverwriteCheck.IsValid() {
			isDestNonEmptyNonZero := !(IsBasicTypeZero(destValForOverwriteCheck) || IsEmptyValue(destValForOverwriteCheck))
			if isDestNonEmptyNonZero {
				var valStr string
				if destValForOverwriteCheck.CanInterface() {
					valStr = fmt.Sprintf("'%v'", destValForOverwriteCheck.Interface())
				} else {
					valStr = "<uninterfaceable>"
				}
				c.logger.Debugf("   -> MERGE SKIP for target '%s'. Dest value (%s, type %s) is non-empty/non-zero.",
					itemNameForProcessItem, valStr, destValForOverwriteCheck.Type())
				continue
			}
		}

		formattedSrcVal := srcVal
		if destFieldFound {
			if formatter, ok := c.formatters[matchedDestFieldEffectiveName]; ok {
				if srcVal.CanInterface() {
					formattedResult := formatter(srcVal.Interface())
					if formattedResult != nil {
						newVal := reflect.ValueOf(formattedResult)
						formattedSrcVal = newVal
					} else {
						if c.skipNilSrc {
							c.logger.Debugf("     Formatter for '%s' returned nil, skipping (skipNilSrc=true). Dest field preserved.", matchedDestFieldEffectiveName)
							continue
						}
						formattedSrcVal = GetNilLikeValue(srcVal)
					}
				} else {
					c.logger.Warnf("   -> Cannot apply formatter for field '%s': source value type %s cannot be interfaced.", matchedDestFieldEffectiveName, srcVal.Type())
				}
			}
		}

		fieldTagSensitivity := srcFieldInfo.sensitive
		if destFieldFound {
			fieldTagSensitivity = destFieldInfoFound.sensitive
		}
		dynamicSrcSensitivity := c.checkIsSensitive(formattedSrcVal)
		combinedSensitivity := fieldTagSensitivity || dynamicSrcSensitivity

		if destFieldFound && c.tryFastFieldCopy(dest, &destFieldInfoFound, formattedSrcVal, itemNameForProcessItem, combinedSensitivity, ctx) {
			continue
		}

		res := c.processItem(formattedSrcVal, itemNameForProcessItem, baseDestTypeForProcessItem, combinedSensitivity, ctx)
		if res.err != nil {
			errField := fmt.Errorf("processing src field '%s' for dest target '%s': %w", srcFieldEffectiveName, itemNameForProcessItem, res.err)
			if c.handlePotentialError(errField, ctx) {
				continue
			}
			return errField
		}

		if destFieldFound {
			destValToSet, dvOk, _ := c.getSettableFieldValue(dest, &destFieldInfoFound)
			if dvOk && destValToSet.CanSet() {
				if res.shouldSet {
					if res.valueToSet.IsValid() {
						destValToSet.Set(res.valueToSet)
					} else {
						destValToSet.Set(reflect.Zero(destValToSet.Type()))
					}
					c.logger.Debugf("   -> Field '%s' SET by processItem.", itemNameForProcessItem)
				} else {
					c.logger.Debugf("   -> Field '%s' NOT SET by processItem (shouldSet=false). Dest field value preserved.", itemNameForProcessItem)
				}
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
				c.logger.Debugf("   -> Unmapped key '%s' SET by processItem.", itemNameForProcessItem)
			} else {
				if c.overwrite {
					unmappedDestMap.SetMapIndex(mapKey, reflect.Value{})
					c.logger.Debugf("   -> Unmapped key '%s' REMOVED from unmapped map (overwrite=true, processItem.shouldSet=false).", itemNameForProcessItem)
				} else {
					c.logger.Debugf("   -> Unmapped key '%s' NOT SET by processItem (shouldSet=false). Unmapped map key value preserved if merging.", itemNameForProcessItem)
				}
			}
		}
	}

	if c.overwrite {
		for destFieldToZero, dfi := range destInfo.fields {
			if destFieldToZero == unmappedDestFieldName {
				continue
			}
			if _, wasHandled := destFieldsProcessedOrSkipped[destFieldToZero]; !wasHandled {
				c.logger.Debugf("   Post-src iteration: Zeroing untargeted dest field '%s' (overwrite=true).", destFieldToZero)
				destVal, dvOk, _ := c.getSettableFieldValue(dest, &dfi)
				if dvOk && destVal.CanSet() {
					destVal.Set(reflect.Zero(destVal.Type()))
				}
			}
		}
	}

	c.logger.Debugf("Finished copyStructDriven (%s -> %s)", srcForInfo.Type(), dest.Type())
	return nil
}

func (c *Copier) tryFastFieldCopy(dest reflect.Value, destFieldInfo *FieldInfo, srcVal reflect.Value, itemName string, isSensitive bool, ctx *SysContext) bool {
	if isSensitive || !c.useUnsafe || !srcVal.IsValid() || !srcVal.CanInterface() {
		return false
	}

	destVal, destOk, destErr := c.getSettableFieldValue(dest, destFieldInfo)
	if destErr != nil || !destOk || !destVal.CanSet() {
		return false
	}

	srcType := srcVal.Type()
	destType := destVal.Type()

	if srcType != destType && !srcType.AssignableTo(destType) {
		return false
	}

	if !IsMemCopySuitable(srcType) {
		return false
	}

	if !destVal.CanAddr() || !srcVal.CanAddr() {
		return false
	}

	if destFieldInfo.structField.PkgPath != "" && !c.copyUnexported {
		return false
	}

	elemSize := srcType.Size()
	if elemSize == 0 {
		return false
	}

	destPtr := unsafe.Pointer(destVal.UnsafeAddr())
	srcPtr := unsafe.Pointer(srcVal.UnsafeAddr())

	c.logger.Debugf("   -> Fast unsafe copy for field '%s' (%s)", itemName, srcType)
	memmove(destPtr, srcPtr, elemSize)
	runtime.KeepAlive(destVal)
	runtime.KeepAlive(srcVal)

	return true
}

// copyStructToMap copies data from a struct to a map.
func (c *Copier) copyStructToMap(dest, src reflect.Value, ctx *SysContext) error {
	srcForInfo := src
	if srcForInfo.Kind() == reflect.Ptr {
		if srcForInfo.IsNil() {
			c.logger.Debugf("   copyStructToMap: Source pointer is nil.")
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
	c.logger.Debugf("Copying Struct -> Map (%s -> %s)", srcForInfo.Type(), dest.Type())

	srcInfo := c.getTypeInfo(srcForInfo.Type())

	originalDestMapSnapshot := make(map[interface{}]reflect.Value)
	if dest.IsValid() && !dest.IsNil() {
		iter := dest.MapRange()
		for iter.Next() {
			keyCopy := reflect.New(iter.Key().Type()).Elem()
			keyCopy.Set(iter.Key())
			originalDestMapSnapshot[keyCopy.Interface()] = iter.Value()
		}
	}

	if dest.IsNil() {
		if !dest.CanSet() {
			return fmt.Errorf("cannot set nil map dest %s", destType)
		}
		c.logger.Debugf("   Initializing nil destination map %s.", destType)
		dest.Set(reflect.MakeMap(destType))
	} else if c.overwrite {
		c.logger.Debugf("   Destination map overwrite: will clear keys not present in source struct (after processing).")
	}

	if dest.IsNil() {
		return fmt.Errorf("map %s init failed or dest not settable/became nil", destType)
	}

	destKeysSetOrPreservedFromSource := make(map[interface{}]struct{})

	for srcFieldNameFromInfo, srcFieldInfo := range srcInfo.fields {
		destKeyName := srcFieldNameFromInfo
		if mappedName, ok := c.fieldMappings[srcFieldInfo.structField.Name]; ok {
			destKeyName = mappedName
		} else if mappedName, ok := c.fieldMappings[srcFieldNameFromInfo]; ok {
			destKeyName = mappedName
		}

		var destKeyVal reflect.Value
		if destMapKeyType.Kind() == reflect.String {
			destKeyVal = reflect.ValueOf(destKeyName)
		} else {
			tempKeyHolder := reflect.New(destMapKeyType).Elem()
			keyCopyCtx := &SysContext{depth: ctx.depth + 1, Detector: cycle.New(cycle.WithLogger(c.logger)), Errors: ctx.Errors}
			if errKey := c.CopyRecursive(tempKeyHolder, reflect.ValueOf(destKeyName), keyCopyCtx); errKey != nil {
				c.logger.Debugf("   -> Skipping map key conversion for '%s' (from field '%s') to type %s: %v", destKeyName, srcFieldNameFromInfo, destMapKeyType, errKey)
				if c.handlePotentialError(errKey, ctx) {
				}
				continue
			}
			destKeyVal = tempKeyHolder
		}

		if destKeyVal.IsValid() {
			destKeysSetOrPreservedFromSource[destKeyVal.Interface()] = struct{}{}
		} else {
			c.logger.Debugf("   -> Skipping due to invalid destination key for source field '%s'.", srcFieldNameFromInfo)
			continue
		}

		if _, ignore := srcFieldInfo.tagOpts["-"]; ignore {
			c.logger.Debugf("   -> Skipping struct field '%s' (tag '-'). Dest map key '%v' value preserved if it existed.", srcFieldNameFromInfo, destKeyVal.Interface())
			continue
		}
		if _, skipGlobal := c.skipFields[destKeyName]; skipGlobal {
			c.logger.Debugf("   -> Skipping struct field '%s' to map key '%s' (globally). Dest map key value preserved if it existed.", srcFieldNameFromInfo, destKeyName)
			continue
		}

		srcVal, readable := c.readSourceFieldValue(srcForInfo, &srcFieldInfo)
		if !readable {
			c.logger.Debugf("   -> Skipping struct field '%s': unreadable. Dest map key '%v' value preserved if it existed.", srcFieldNameFromInfo, destKeyVal.Interface())
			continue
		}
		if _, omit := srcFieldInfo.tagOpts["omitempty"]; omit && IsEmptyValueForOmit(srcVal) {
			c.logger.Debugf("   -> Skipping struct field '%s' to map key '%s' (omitempty). Dest map key value preserved if it existed.", srcFieldNameFromInfo, destKeyName)
			continue
		}

		if !c.overwrite {
			if existingMapVal := dest.MapIndex(destKeyVal); existingMapVal.IsValid() && !IsEmptyValue(existingMapVal) {
				c.logger.Debugf("   -> Skipping map key '%s': Merge skip, key exists with non-empty value.", destKeyName)
				continue
			}
		}

		srcValIsDynamicallySensitive := c.checkIsSensitive(srcVal)
		combinedSensitivity := srcFieldInfo.sensitive || srcValIsDynamicallySensitive
		itemNameForProcessItem := destKeyName
		res := c.processItem(srcVal, itemNameForProcessItem, destValueType, combinedSensitivity, ctx)

		if res.err != nil {
			errMapVal := fmt.Errorf("processing value for map key '%s' from field '%s': %w", destKeyName, srcFieldNameFromInfo, res.err)
			if c.handlePotentialError(errMapVal, ctx) {
				continue
			}
			return errMapVal
		}

		if dest.IsNil() {
			return fmt.Errorf("dest map %s became nil during processing", destType)
		}

		if res.shouldSet {
			if res.valueToSet.IsValid() {
				dest.SetMapIndex(destKeyVal, res.valueToSet)
			} else {
				dest.SetMapIndex(destKeyVal, reflect.Zero(destValueType))
			}
			c.logger.Debugf("      Set map key '%s' from field '%s' via processItem.", destKeyName, srcFieldNameFromInfo)
		} else {
			if c.overwrite {
				if originalVal, ok := originalDestMapSnapshot[destKeyVal.Interface()]; ok {
					dest.SetMapIndex(destKeyVal, originalVal)
					c.logger.Debugf("      Map key '%s' from field '%s' NOT SET by processItem (overwrite=true, skipped src value), original dest value for key restored.", destKeyName, srcFieldNameFromInfo)
				} else {
					dest.SetMapIndex(destKeyVal, reflect.Value{})
					c.logger.Debugf("      Map key '%s' from field '%s' NOT SET by processItem (overwrite=true, skipped src value), key removed as it wasn't in original dest.", destKeyName, srcFieldNameFromInfo)
				}
			} else {
				c.logger.Debugf("      Map key '%s' from field '%s' NOT SET by processItem (shouldSet=false). Dest map value for this key preserved.", destKeyName, srcFieldNameFromInfo)
			}
		}
	}

	if c.overwrite {
		currentMapKeys := dest.MapKeys()
		for _, k := range currentMapKeys {
			if _, isSourcedOrPreserved := destKeysSetOrPreservedFromSource[k.Interface()]; !isSourcedOrPreserved {
				c.logger.Debugf("   Overwrite Cleanup: Removing key '%v' from dest map as it was not sourced/preserved from input struct.", k.Interface())
				dest.SetMapIndex(k, reflect.Value{})
			}
		}
	}
	return nil
}
