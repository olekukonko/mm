// ca.go
package mm

import (
	"fmt"
	"reflect"
	"runtime"
	"unsafe"
)

// copyArray copies data from a slice or array to a fixed-size array.
func (c *Copier) copyArray(dest, src reflect.Value, ctx *SysContext) error {
	srcType := src.Type()
	destType := dest.Type()
	srcLen := src.Len()
	destLen := dest.Len()

	c.logger.Indent(ctx.depth)
	c.logger.Debugf("Copying Slice/Array -> Array (%s -> %s)", srcType, destType)

	if destLen != srcLen {
		return fmt.Errorf("array length mismatch: dest %s[%d] != src %s[%d]", destType.Elem(), destLen, srcType.Elem(), srcLen)
	}
	if srcLen == 0 {
		c.logger.Debugf("   Arrays empty.")
		return nil
	}

	destElemType := destType.Elem()
	srcElemType := src.Type().Elem()
	canBulkCopy := false

	if c.useUnsafe && destElemType == srcElemType && isMemCopySuitable(destElemType) {
		if dest.CanAddr() && src.CanAddr() {
			elemSize := destElemType.Size()
			totalSize := elemSize * uintptr(destLen)
			if totalSize > 0 && elemSize > 0 {
				destPtr := unsafe.Pointer(dest.UnsafeAddr())
				srcPtr := unsafe.Pointer(src.UnsafeAddr())
				c.logger.Debugf("   -> Performing unsafe memmove (size %d)", totalSize)
				memmove(destPtr, srcPtr, totalSize)
				runtime.KeepAlive(dest)
				runtime.KeepAlive(src)
				canBulkCopy = true
			} else if totalSize == 0 {
				canBulkCopy = true
			}
		} else {
			c.logger.Debugf("   -> Cannot use memmove: array not addressable (or src not addressable).")
		}
	}

	if !canBulkCopy {
		c.logger.Debugf("   Performing element-wise copy (using processItem)")
		for i := 0; i < srcLen; i++ {
			srcElem := src.Index(i)
			destElem := dest.Index(i)
			itemName := fmt.Sprintf("arrayElem[%d]", i)

			currentItemIsSensitive := c.checkIsSensitive(srcElem)

			res := c.processItem(srcElem, itemName, destElemType, currentItemIsSensitive, ctx)

			if res.err != nil {
				return res.err // Error was not collected by processItem
			}

			if res.shouldSet {
				if !destElem.CanSet() {
					elemErr := fmt.Errorf("internal error: cannot set array element %d of type %s", i, destElem.Type())
					if c.handlePotentialError(elemErr, ctx) {
						continue
					}
					return elemErr
				}
				if res.valueToSet.IsValid() {
					destElem.Set(res.valueToSet)
				} else {
					destElem.Set(reflect.Zero(destElem.Type()))
				}
				c.logger.Debugf("      Array element %d set via processItem.", i)
			} else {
				c.logger.Debugf("      Array element %d not set by processItem (skipped or Copiable handled non-overwrite).", i)
			}
		}
	} else {
		c.logger.Debugf("   -> Bulk copy performed.")
	}
	return nil
}

// copySlice copies data from a slice or array to a slice.
func (c *Copier) copySlice(dest, src reflect.Value, ctx *SysContext) error {
	destType := dest.Type()
	srcType := src.Type()
	srcLen := src.Len()

	c.logger.Indent(ctx.depth)
	c.logger.Debugf("Copying Slice/Array -> Slice (%s -> %s)", srcType, destType)

	if src.Kind() == reflect.Slice && src.IsNil() {
		c.logger.Debugf("   Source slice is nil.")
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite && dest.CanSet() && dest.IsValid() && !dest.IsNil() {
			c.logger.Debugf("   Zeroing destination slice (setting to nil).")
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	currentDestLen := 0
	if dest.IsValid() && !dest.IsNil() {
		currentDestLen = dest.Len()
	}

	var newDest, targetSliceForCopyElements reflect.Value
	targetLenAfterCopy, copyStartIndex := 0, 0
	sliceReallocated := false

	if c.overwrite {
		targetLenAfterCopy, copyStartIndex = srcLen, 0
		if !dest.IsValid() || dest.IsNil() || dest.Cap() < srcLen {
			if !dest.CanSet() {
				return fmt.Errorf("cannot set/resize slice dest %s for overwrite (cap %d < srcLen %d or nil)", destType, dest.Cap(), srcLen)
			}
			newDest = reflect.MakeSlice(destType, srcLen, srcLen)
			sliceReallocated = true
			c.logger.Debugf("   -> Created/Recreated slice dest %s len/cap %d", destType, srcLen)
			targetSliceForCopyElements = newDest
		} else {
			targetSliceForCopyElements = dest
			if dest.Len() != srcLen {
				if !dest.CanSet() {
					return fmt.Errorf("cannot set length of non-settable slice %s for overwrite", destType)
				}
				dest.SetLen(srcLen)
				c.logger.Debugf("   -> Adjusted dest slice %s length from %d to %d", destType, currentDestLen, srcLen)
			} else {
				c.logger.Debugf("   -> Dest slice %s already correct size/cap for overwrite", destType)
			}
		}
	} else {
		targetLenAfterCopy = currentDestLen + srcLen
		copyStartIndex = currentDestLen
		if !dest.IsValid() || dest.IsNil() {
			if !dest.CanSet() {
				return fmt.Errorf("cannot set nil slice dest %s for merge", destType)
			}
			newDest = reflect.MakeSlice(destType, targetLenAfterCopy, targetLenAfterCopy)
			sliceReallocated = true
			c.logger.Debugf("   -> Initialized nil slice dest %s len/cap %d", destType, targetLenAfterCopy)
			targetSliceForCopyElements = newDest
		} else {
			if dest.Cap() < targetLenAfterCopy {
				if !dest.CanSet() {
					return fmt.Errorf("cannot grow non-settable slice %s (cap %d < target %d)", destType, dest.Cap(), targetLenAfterCopy)
				}
				newDest = reflect.MakeSlice(destType, targetLenAfterCopy, targetLenAfterCopy)
				reflect.Copy(newDest, dest)
				sliceReallocated = true
				c.logger.Debugf("   -> Grew slice dest %s cap from %d to %d", destType, dest.Cap(), targetLenAfterCopy) // Corrected: dest.Cap() refers to old cap
				targetSliceForCopyElements = newDest
			} else {
				targetSliceForCopyElements = dest
				if dest.Len() != targetLenAfterCopy {
					if !dest.CanSet() {
						return fmt.Errorf("cannot set length of non-settable slice %s for merge extend", destType)
					}
					dest.SetLen(targetLenAfterCopy)
					c.logger.Debugf("   -> Extended slice dest %s length from %d to %d", destType, currentDestLen, targetLenAfterCopy)
				} else {
					c.logger.Debugf("   -> Dest slice %s already correct length/cap for merge", destType)
				}
			}
		}
	}

	if sliceReallocated {
		if dest.CanSet() {
			dest.Set(newDest)
			c.logger.Debugf("   -> Assigned newly allocated slice back to original dest.")
			targetSliceForCopyElements = dest
		} else {
			c.logger.Debugf("   WARNING: Slice reallocated (%s) but original dest not settable. Element copy will target new slice.", newDest.Type())
		}
	}

	if srcLen == 0 {
		if c.overwrite && targetSliceForCopyElements.IsValid() && !targetSliceForCopyElements.IsNil() && targetSliceForCopyElements.Len() > 0 {
			if targetSliceForCopyElements.CanSet() {
				targetSliceForCopyElements.SetLen(0)
				c.logger.Debugf("   -> Src empty, dest cleared (len set to 0).")
			}
		} else {
			c.logger.Debugf("   -> Src empty, nothing to copy.")
		}
		return nil
	}

	destElemType := destType.Elem()
	srcElemType := src.Type().Elem()
	canBulkCopy := false

	if c.overwrite && targetSliceForCopyElements.IsValid() && targetSliceForCopyElements.Len() == srcLen {
		if srcType.Kind() == destType.Kind() && srcElemType.AssignableTo(destElemType) { // reflect.Copy works best for same kinds and assignable elements
			c.logger.Debugf("   -> Performing reflect.Copy")
			reflect.Copy(targetSliceForCopyElements, src)
			canBulkCopy = true
		} else if c.useUnsafe && destElemType == srcElemType && isMemCopySuitable(destElemType) {
			if targetSliceForCopyElements.CanAddr() && src.CanAddr() && srcLen > 0 {
				elemSize := destElemType.Size()
				totalSize := elemSize * uintptr(srcLen)
				if totalSize > 0 && elemSize > 0 {
					destPtr := unsafe.Pointer(targetSliceForCopyElements.Pointer())
					srcPtr := unsafe.Pointer(src.Pointer())
					c.logger.Debugf("   -> Performing unsafe memmove (size %d)", totalSize)
					memmove(destPtr, srcPtr, totalSize)
					runtime.KeepAlive(targetSliceForCopyElements)
					runtime.KeepAlive(src)
					canBulkCopy = true
				} else if totalSize == 0 {
					canBulkCopy = true
				}
			} else {
				c.logger.Debugf("   -> Cannot use memmove: slice data not addressable or srcLen is 0.")
			}
		}
	}

	if !canBulkCopy {
		c.logger.Debugf("   -> Performing element-wise copy (using processItem. StartIndex: %d, SrcLen: %d, TargetSliceLen: %d, TargetSliceCap: %d)",
			copyStartIndex, srcLen, targetSliceForCopyElements.Len(), targetSliceForCopyElements.Cap())

		if !targetSliceForCopyElements.IsValid() {
			return fmt.Errorf("internal error: target slice for element copy is invalid")
		}
		if copyStartIndex+srcLen > targetSliceForCopyElements.Len() {
			return fmt.Errorf("dest slice %s insufficient length (%d) for element-wise copy (need %d from index %d)",
				destType, targetSliceForCopyElements.Len(), srcLen, copyStartIndex)
		}

		for i := 0; i < srcLen; i++ {
			destIndex := copyStartIndex + i
			var destElemTarget reflect.Value // The Value representing the element at destIndex, to be set.
			originalDestElemAtIndex := targetSliceForCopyElements.Index(destIndex)

			if originalDestElemAtIndex.CanSet() {
				destElemTarget = originalDestElemAtIndex
			} else if c.useUnsafe && targetSliceForCopyElements.CanAddr() && originalDestElemAtIndex.CanAddr() {
				elemPtr := unsafe.Pointer(originalDestElemAtIndex.UnsafeAddr())
				destElemTarget = reflect.NewAt(originalDestElemAtIndex.Type(), elemPtr).Elem()
				if !destElemTarget.CanSet() {
					elemErr := fmt.Errorf("cannot get settable slice element %d for %s even with unsafe", destIndex, destType)
					if c.handlePotentialError(elemErr, ctx) {
						continue
					}
					return elemErr
				}
			} else {
				elemErr := fmt.Errorf("cannot get settable slice element %d for %s", destIndex, destType)
				if c.handlePotentialError(elemErr, ctx) {
					continue
				}
				return elemErr
			}

			srcElem := src.Index(i)
			itemName := fmt.Sprintf("sliceElem[%d]", i)
			currentItemIsSensitive := c.checkIsSensitive(srcElem)

			res := c.processItem(srcElem, itemName, destElemType, currentItemIsSensitive, ctx)

			if res.err != nil {
				return res.err
			}

			if res.shouldSet {
				if !destElemTarget.CanSet() { // Should be ensured above
					elemErr := fmt.Errorf("internal error: cannot set slice element %d of type %s", destIndex, destElemTarget.Type())
					if c.handlePotentialError(elemErr, ctx) {
						continue
					}
					return elemErr
				}
				if res.valueToSet.IsValid() {
					destElemTarget.Set(res.valueToSet)
				} else {
					destElemTarget.Set(reflect.Zero(destElemTarget.Type()))
				}
				c.logger.Debugf("      Slice element at destIndex %d set via processItem.", destIndex)
			} else {
				c.logger.Debugf("      Slice element at destIndex %d not set by processItem.", destIndex)
			}
		}
	} else {
		c.logger.Debugf("   -> Bulk copy performed.")
	}
	return nil
}

func (c *Copier) copySliceToMap(dest, src reflect.Value, ctx *SysContext) error {
	destType := dest.Type()
	destKeyType := destType.Key()
	destValueType := destType.Elem()
	srcType := src.Type()
	srcElemType := srcType.Elem()
	srcLen := src.Len()

	c.logger.Indent(ctx.depth)
	c.logger.Debugf("Copying Slice -> Map (%s -> %s)", srcType, destType)

	if src.Kind() == reflect.Slice && src.IsNil() {
		c.logger.Debugf("   Source slice is nil.")
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite && dest.CanSet() && dest.IsValid() && !dest.IsNil() {
			c.logger.Debugf("   Zeroing destination map (setting to nil).")
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	if dest.IsNil() {
		if !dest.CanSet() {
			return fmt.Errorf("cannot set nil map dest %s", destType)
		}
		dest.Set(reflect.MakeMapWithSize(destType, srcLen))
		c.logger.Debugf("   Initialized nil map dest.")
	} else if c.overwrite {
		iter := dest.MapRange()
		var keys []reflect.Value
		for iter.Next() {
			keys = append(keys, iter.Key())
		}
		for _, k := range keys {
			dest.SetMapIndex(k, reflect.Value{})
		}
		c.logger.Debugf("   Cleared destination map for overwrite.")
	}

	if srcLen == 0 {
		c.logger.Debugf("   Source slice is empty, nothing to copy into map.")
		return nil
	}

	if destKeyType.Kind() == reflect.Int {
		c.logger.Debugf("   -> Slice to map[int]T logic using processItem for values")
		for i := 0; i < srcLen; i++ {
			srcElem := src.Index(i)
			destMapKey := reflect.ValueOf(i)

			if !c.overwrite {
				if existingVal := dest.MapIndex(destMapKey); existingVal.IsValid() && !isEmptyValue(existingVal) {
					c.logger.Debugf("      -> Skipping map key '%d': Overwrite=false and key exists non-empty.", i)
					continue
				}
			}

			itemName := fmt.Sprintf("mapValueFromSlice[%d]", i)
			currentItemIsSensitive := c.checkIsSensitive(srcElem)
			res := c.processItem(srcElem, itemName, destValueType, currentItemIsSensitive, ctx)

			if res.err != nil {
				return fmt.Errorf("slice elem %d to map key %d: %w", i, i, res.err)
			}
			if res.shouldSet {
				if res.valueToSet.IsValid() {
					dest.SetMapIndex(destMapKey, res.valueToSet)
				} else {
					dest.SetMapIndex(destMapKey, reflect.Zero(destValueType))
				}
				c.logger.Debugf("      Set map key '%d' via processItem.", i)
			}
		}
		return nil
	}

	if destValueType.Kind() == reflect.String {
		c.logger.Debugf("   -> Slice to map[T]string logic (key Copiable handling updated)")
		canUseElemAsKey := srcElemType.AssignableTo(destKeyType) || srcElemType.ConvertibleTo(destKeyType)
		if !canUseElemAsKey {
			return fmt.Errorf("%w: cannot convert slice elem %s to map key %s", ErrTypeMismatch, srcElemType, destKeyType)
		}

		for i := 0; i < srcLen; i++ {
			srcElemForKey := src.Index(i)

			if c.skipZeroValue && srcElemForKey.IsValid() && srcElemForKey.IsZero() { /* log, continue */
				continue
			}
			if c.skipEmptyValue && isEmptyValue(srcElemForKey) { /* log, continue */
				continue
			}
			if c.skipNilSrc && srcElemForKey.IsValid() && isNilable(srcElemForKey) && srcElemForKey.IsNil() { /* log, continue */
				continue
			}

			isKeySensitive := c.checkIsSensitive(srcElemForKey)
			if isKeySensitive && c.skipSensitive { /* log, continue */
				continue
			}

			var destMapKey reflect.Value
			copiableUsedForKey := false

			if isKeySensitive && srcElemForKey.IsValid() && !srcElemForKey.Type().Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) {
				c.logger.Debugf("      -> SKIPPING Sensitive src elem (idx %d, type %s) for key: No Copiable.", i, srcElemForKey.Type())
				if c.debugMode {
					c.logger.Infof("[mm debug] Sensitive type %s at slice idx %d (key for map[T]string) skipped due to missing Copiable", srcElemForKey.Type(), i)
				}
				continue
			}

			if srcElemForKey.IsValid() && srcElemForKey.Type().Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) && srcElemForKey.CanInterface() {
				c.logger.Debugf("      Src elem (idx %d, type %s) for key is Copiable.", i, srcElemForKey.Type())
				var copiableInst Copiable
				receiverForKey := reflect.New(srcElemForKey.Type())
				var okAssertKey bool
				if receiverForKey.Type().Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) {
					copiableInst, okAssertKey = receiverForKey.Interface().(Copiable)
				}
				if !okAssertKey && srcElemForKey.Type().Kind() != reflect.Ptr && srcElemForKey.Type().Kind() != reflect.Interface {
					valReceiverKey := receiverForKey.Elem()
					if valReceiverKey.Type().Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) && valReceiverKey.CanInterface() {
						copiableInst, okAssertKey = valReceiverKey.Interface().(Copiable)
					}
				}
				if !okAssertKey {
					keyErr := fmt.Errorf("copiable (sliceToMap key): failed to assert instance of %s for key (idx %d)", srcElemForKey.Type(), i)
					if c.handlePotentialError(keyErr, ctx) {
						continue
					}
					return keyErr
				}

				var keySetByCallback bool
				ctxCopyForKey := &CopyContext{
					FieldName: fmt.Sprintf("sliceElemToMapKey[%d]", i),
					Settings:  struct{ Overwrite, SkipEmpty, SkipZero, SkipNil bool }{c.overwrite, c.skipEmptyValue, c.skipZeroValue, c.skipNilSrc},
					Overwrite: false,
					SetValue: func(value interface{}) error {
						keySetByCallback = true
						if value == nil {
							destMapKey = reflect.Value{}
							return fmt.Errorf("copiable for map key (idx %d) provided nil via SetValue", i)
						}
						v := reflect.ValueOf(value)
						if !v.IsValid() {
							destMapKey = reflect.Value{}
							return fmt.Errorf("copiable for map key (idx %d) SetValue with reflect.ValueOf(non-nil)=invalid", i)
						}
						if !v.Type().AssignableTo(destKeyType) && !v.Type().ConvertibleTo(destKeyType) {
							destMapKey = reflect.Value{}
							return fmt.Errorf("copiable for map key (idx %d) SetValue with incompatible type %s for dest key type %s", i, v.Type(), destKeyType)
						}
						destMapKey = v
						return nil
					},
					Copier: c,
				}

				returnedKeyFromCopy, errKeyCopy := copiableInst.Copy(ctxCopyForKey, srcElemForKey.Interface())
				if errKeyCopy != nil {
					mapErr := fmt.Errorf("Copiable.Copy for map key (idx %d) failed: %w", i, errKeyCopy)
					if c.handlePotentialError(mapErr, ctx) {
						continue
					}
					return mapErr
				}

				if ctxCopyForKey.Overwrite {
					if !keySetByCallback && returnedKeyFromCopy != nil {
						if err := ctxCopyForKey.SetValue(returnedKeyFromCopy); err != nil {
							mapErr := fmt.Errorf("setting Copiable value from return for map key (idx %d): %w", i, err)
							if c.handlePotentialError(mapErr, ctx) {
								continue
							}
							return mapErr
						}
					}
					if !destMapKey.IsValid() {
						mapErr := fmt.Errorf("copiable for map key (idx %d) set Overwrite but resulted in invalid/nil key", i)
						if c.handlePotentialError(mapErr, ctx) {
							continue
						}
						return mapErr
					}
					if destMapKey.Type().AssignableTo(destKeyType) { /* fine */
					} else if destMapKey.Type().ConvertibleTo(destKeyType) {
						destMapKey = destMapKey.Convert(destKeyType)
					} else {
						mapErr := fmt.Errorf("copiable for map key (idx %d) type %s not assignable/convertible to dest key type %s", i, destMapKey.Type(), destKeyType)
						if c.handlePotentialError(mapErr, ctx) {
							continue
						}
						return mapErr
					}
					copiableUsedForKey = true
				} else if isKeySensitive {
					mapErr := fmt.Errorf("sensitive map key (idx %d, type %s) not transformed by Copiable: %w", i, srcElemForKey.Type(), ErrSensitiveTypeCopied)
					if c.handlePotentialError(mapErr, ctx) {
						continue
					}
					return mapErr
				}
			} // end if srcElemForKey implements Copiable

			if !copiableUsedForKey {
				if srcElemType.AssignableTo(destKeyType) {
					destMapKey = srcElemForKey
				} else {
					destMapKey = srcElemForKey.Convert(destKeyType)
				}
			}
			if !destMapKey.IsValid() {
				c.logger.Warnf("      Resulting destMapKey for slice idx %d is invalid. Skipping.", i)
				continue
			}

			if !c.overwrite {
				if existingVal := dest.MapIndex(destMapKey); existingVal.IsValid() && !isEmptyValue(existingVal) {
					c.logger.Debugf("      -> Skipping map key '%v': Overwrite=false and key exists non-empty.", destMapKey.Interface())
					continue
				}
			}

			var destValueStr string
			stringerSrc := srcElemForKey // Use original element for stringification
			if stringerSrc.CanInterface() {
				if stringer, ok := stringerSrc.Interface().(fmt.Stringer); ok {
					destValueStr = stringer.String()
				}
			}
			if destValueStr == "" && stringerSrc.CanInterface() {
				destValueStr = fmt.Sprint(stringerSrc.Interface())
			}
			destMapValue := reflect.ValueOf(destValueStr)

			dest.SetMapIndex(destMapKey, destMapValue)
			c.logger.Debugf("      -> Set map key '%v' to string value '%s'.", destMapKey.Interface(), destValueStr)
		}
		return nil
	}

	return fmt.Errorf("%w: cannot convert slice %s to map %s (unsupported map key/value types combination)", ErrTypeMismatch, srcType, destType)
}
