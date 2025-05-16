// ca.go
package copier

import (
	"fmt"
	"github.com/olekukonko/mm/mf"
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

	elementPotentiallyNeedsProcessItem := false
	if destElemType.Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) ||
		reflect.PtrTo(destElemType).Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) ||
		srcElemType.Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) ||
		srcElemType.Implements(reflect.TypeOf((*Sensitivity)(nil)).Elem()) ||
		(c.skipSensitive && srcElemType.Implements(reflect.TypeOf((*Sensitivity)(nil)).Elem())) {
		elementPotentiallyNeedsProcessItem = true
		c.logger.Debugf("   -> Element type %s or %s may require processItem (Copiable/Sensitive). Disabling bulk copy for array.", srcElemType, destElemType)
	}

	if !elementPotentiallyNeedsProcessItem && c.useUnsafe && destElemType == srcElemType && mf.IsMemCopySuitable(destElemType) {
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
			res := c.processItem(srcElem, itemName, destElem.Type(), currentItemIsSensitive, ctx)

			if res.err != nil {
				return res.err
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
				c.logger.Debugf("      Array element %d not set by processItem (skipped or Copiable handled non-overwrite). Dest element preserved.", i)
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

	// Keep a reference to the original dest's state for potential preservation logic.
	originalDestValue := dest
	originalDestLen := 0
	var originalDestContentCopy reflect.Value // Used if we need a snapshot

	if originalDestValue.IsValid() && !originalDestValue.IsNil() {
		originalDestLen = originalDestValue.Len()
		// Create a snapshot of originalDestValue's content if we might need it for overwrite+element-wise
		// This snapshot helps preserve initial values if targetSliceForCopyElements is entirely new.
		if c.overwrite { // Only relevant for overwrite scenarios needing preservation
			originalDestContentCopy = reflect.MakeSlice(destType, originalDestLen, originalDestLen)
			reflect.Copy(originalDestContentCopy, originalDestValue)
			c.logger.Debugf("   Snapshot of original dest content taken (len %d).", originalDestLen)
		}
	}

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

	currentDestEffectiveLen := 0 // Length of the slice that elements will be copied into
	if dest.IsValid() && !dest.IsNil() {
		currentDestEffectiveLen = dest.Len()
	}

	var newDestAllocated, targetSliceForCopyElements reflect.Value
	targetLenAfterCopy, copyStartIndex := 0, 0
	sliceReallocated := false // True if targetSliceForCopyElements points to a newly allocated backing array

	if c.overwrite {
		targetLenAfterCopy = srcLen
		copyStartIndex = 0 // Copying starts at index 0 of the target
		if !dest.IsValid() || dest.IsNil() || dest.Cap() < srcLen {
			if !dest.CanSet() {
				return fmt.Errorf("cannot set/resize slice dest %s for overwrite (cap %d < srcLen %d or nil/invalid)", destType, dest.Cap(), srcLen)
			}
			newDestAllocated = reflect.MakeSlice(destType, srcLen, srcLen) // New slice, elements are zeroed
			sliceReallocated = true
			c.logger.Debugf("   -> Created new slice for dest %s (len/cap %d) due to nil/cap<srcLen.", destType, srcLen)
			targetSliceForCopyElements = newDestAllocated
		} else { // dest is valid, non-nil, and has enough capacity
			targetSliceForCopyElements = dest
			if dest.Len() != srcLen {
				if !dest.CanSet() {
					return fmt.Errorf("cannot set length of non-settable slice %s for overwrite", destType)
				}
				dest.SetLen(srcLen) // Adjust length; elements preserved/zeroed by SetLen
				c.logger.Debugf("   -> Adjusted dest slice %s length from %d to %d for overwrite.", destType, currentDestEffectiveLen, srcLen)
			} else {
				c.logger.Debugf("   -> Dest slice %s already correct size for overwrite.", destType)
			}
			// sliceReallocated remains false, targetSliceForCopyElements is the original dest (or a view of it)
		}
	} else { // Merge mode
		targetLenAfterCopy = currentDestEffectiveLen + srcLen
		copyStartIndex = currentDestEffectiveLen
		if !dest.IsValid() || dest.IsNil() {
			if !dest.CanSet() {
				return fmt.Errorf("cannot set nil slice dest %s for merge", destType)
			}
			newDestAllocated = reflect.MakeSlice(destType, targetLenAfterCopy, targetLenAfterCopy)
			sliceReallocated = true
			c.logger.Debugf("   -> Initialized nil slice dest %s len/cap %d for merge.", destType, targetLenAfterCopy)
			targetSliceForCopyElements = newDestAllocated
		} else {
			if dest.Cap() < targetLenAfterCopy {
				if !dest.CanSet() {
					return fmt.Errorf("cannot grow non-settable slice %s (cap %d < target %d)", destType, dest.Cap(), targetLenAfterCopy)
				}
				newDestAllocated = reflect.MakeSlice(destType, targetLenAfterCopy, targetLenAfterCopy)
				if currentDestEffectiveLen > 0 {
					reflect.Copy(newDestAllocated, dest.Slice(0, currentDestEffectiveLen))
				}
				sliceReallocated = true
				c.logger.Debugf("   -> Grew slice dest %s cap from %d to %d, len from %d to %d for merge.", destType, dest.Cap(), newDestAllocated.Cap(), currentDestEffectiveLen, newDestAllocated.Len())
				targetSliceForCopyElements = newDestAllocated
			} else {
				targetSliceForCopyElements = dest
				if dest.Len() != targetLenAfterCopy {
					if !dest.CanSet() {
						return fmt.Errorf("cannot set length of non-settable slice %s for merge extend", destType)
					}
					dest.SetLen(targetLenAfterCopy)
					c.logger.Debugf("   -> Extended slice dest %s length from %d to %d for merge.", destType, currentDestEffectiveLen, targetLenAfterCopy)
				} else {
					c.logger.Debugf("   -> Dest slice %s already correct length/cap for merge.", destType)
				}
			}
		}
	}

	if sliceReallocated { // If newDestAllocated was created and used for targetSliceForCopyElements
		if dest.CanSet() {
			dest.Set(newDestAllocated)
			c.logger.Debugf("   -> Assigned newly allocated slice back to original dest variable.")
			targetSliceForCopyElements = dest // Ensure subsequent operations use the slice attached to 'dest'
		} else {
			c.logger.Warnf("   -> Slice reallocated (%s) but original dest variable not settable. Element copy will target the new slice; original variable 'dest' is unchanged.", newDestAllocated.Type())
			// targetSliceForCopyElements remains newDestAllocated
		}
	}

	if srcLen == 0 {
		if c.overwrite && targetSliceForCopyElements.IsValid() && !targetSliceForCopyElements.IsNil() && targetSliceForCopyElements.Len() > 0 {
			if targetSliceForCopyElements.CanSet() && targetSliceForCopyElements.Len() != 0 {
				targetSliceForCopyElements.SetLen(0)
				c.logger.Debugf("   -> Src empty, dest slice (len %d) cleared (set to len 0).", targetSliceForCopyElements.Len())
			}
		} else {
			c.logger.Debugf("   -> Src empty, nothing to copy.")
		}
		return nil
	}

	destElemType := destType.Elem()
	srcElemType := src.Type().Elem()
	canBulkCopy := false

	elementPotentiallyNeedsProcessItem := false
	if destElemType.Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) ||
		reflect.PtrTo(destElemType).Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) ||
		srcElemType.Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) ||
		srcElemType.Implements(reflect.TypeOf((*Sensitivity)(nil)).Elem()) ||
		(c.skipSensitive && srcElemType.Implements(reflect.TypeOf((*Sensitivity)(nil)).Elem())) {
		elementPotentiallyNeedsProcessItem = true
		c.logger.Debugf("   -> Element type %s or %s may require processItem (Copiable/Sensitive). Element-wise copy will be used.", srcElemType, destElemType)
	}

	// Attempt bulk copy only if elements don't need special processing AND it's a full overwrite.
	if !elementPotentiallyNeedsProcessItem && c.overwrite && targetSliceForCopyElements.IsValid() && targetSliceForCopyElements.Len() == srcLen {
		if srcType.Kind() == destType.Kind() && srcElemType.AssignableTo(destElemType) {
			c.logger.Debugf("   -> Performing reflect.Copy (overwrite, full slice, assignable elements).")
			reflect.Copy(targetSliceForCopyElements, src)
			canBulkCopy = true
		} else if c.useUnsafe && destElemType == srcElemType && mf.IsMemCopySuitable(destElemType) {
			if targetSliceForCopyElements.CanAddr() && src.CanAddr() && srcLen > 0 {
				elemSize := destElemType.Size()
				totalSize := elemSize * uintptr(srcLen)
				if totalSize > 0 && elemSize > 0 {
					destPtr := unsafe.Pointer(targetSliceForCopyElements.Pointer())
					srcPtr := unsafe.Pointer(src.Pointer())
					c.logger.Debugf("   -> Performing unsafe memmove (overwrite, full slice, size %d).", totalSize)
					memmove(destPtr, srcPtr, totalSize)
					runtime.KeepAlive(targetSliceForCopyElements)
					runtime.KeepAlive(src)
					canBulkCopy = true
				} else if totalSize == 0 {
					canBulkCopy = true // Copying zero bytes is a successful bulk copy.
				}
			} else {
				c.logger.Debugf("   -> Cannot use memmove: slice data not addressable or srcLen is 0.")
			}
		}
	}

	if !canBulkCopy {
		c.logger.Debugf("   -> Performing element-wise copy (using processItem. StartIndex: %d, SrcLen: %d, TargetSliceForCopyElements Len: %d, Cap: %d)",
			copyStartIndex, srcLen, targetSliceForCopyElements.Len(), targetSliceForCopyElements.Cap())

		if !targetSliceForCopyElements.IsValid() {
			return fmt.Errorf("internal error: target slice for element copy is invalid")
		}
		if targetSliceForCopyElements.Len() < copyStartIndex+srcLen {
			return fmt.Errorf("dest slice %s insufficient length (%d) for element-wise copy (need %d elements starting at index %d)",
				destType, targetSliceForCopyElements.Len(), srcLen, copyStartIndex)
		}

		// If overwriting and targetSliceForCopyElements was reallocated (is new & zeroed),
		// pre-populate it with values from originalDestContentCopy to ensure preservation logic works.
		if c.overwrite && sliceReallocated && originalDestContentCopy.IsValid() {
			copyCount := srcLen
			if originalDestContentCopy.Len() < srcLen {
				copyCount = originalDestContentCopy.Len()
			}
			if copyCount > 0 {
				reflect.Copy(targetSliceForCopyElements.Slice(0, copyCount), originalDestContentCopy.Slice(0, copyCount))
				c.logger.Debugf("   Element-wise overwrite: Pre-populated reallocated target slice with %d elements from original destination snapshot.", copyCount)
			}
		}

		for i := 0; i < srcLen; i++ {
			destIndex := copyStartIndex + i
			destElemSlot := targetSliceForCopyElements.Index(destIndex)

			if !destElemSlot.CanSet() {
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
				if res.valueToSet.IsValid() {
					destElemSlot.Set(res.valueToSet)
				} else {
					destElemSlot.Set(reflect.Zero(destElemSlot.Type()))
				}
				c.logger.Debugf("      Slice element at destIndex %d set via processItem.", destIndex)
			} else {
				// If shouldSet is false, destElemSlot (which holds the original or pre-populated value) is preserved.
				c.logger.Debugf("      Slice element at destIndex %d not set by processItem. Original/pre-populated element value preserved.", destIndex)
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
				if existingVal := dest.MapIndex(destMapKey); existingVal.IsValid() && !mf.IsEmptyValue(existingVal) {
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
			} else {
				c.logger.Debugf("      Map key '%d' NOT SET by processItem (shouldSet=false).", i)
			}
		}
		return nil
	}

	if destValueType.Kind() == reflect.String {
		c.logger.Debugf("   -> Slice to map[T]string logic (key Copiable handling updated)")
		canUseElemAsKey := srcElemType.AssignableTo(destKeyType) || srcElemType.ConvertibleTo(destKeyType)
		if !canUseElemAsKey {
			if !srcElemType.Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) {
				return fmt.Errorf("%w: cannot convert slice elem %s to map key %s and elem type does not implement Copiable", ErrTypeMismatch, srcElemType, destKeyType)
			}
			c.logger.Debugf("    Slice elem %s not directly convertible to map key %s, relying on Copiable for key generation.", srcElemType, destKeyType)
		}

		for i := 0; i < srcLen; i++ {
			srcElemForKey := src.Index(i)

			if c.skipZeroValue && srcElemForKey.IsValid() && srcElemForKey.IsZero() {
				c.logger.Debugf("      Skipping src elem (idx %d) for key (zero value).", i)
				continue
			}
			if c.skipEmptyValue && mf.IsEmptyValue(srcElemForKey) {
				c.logger.Debugf("      Skipping src elem (idx %d) for key (empty value).", i)
				continue
			}
			if c.skipNilSrc && srcElemForKey.IsValid() && mf.IsNilable(srcElemForKey) && srcElemForKey.IsNil() {
				c.logger.Debugf("      Skipping src elem (idx %d) for key (nil value).", i)
				continue
			}

			isKeySensitive := c.checkIsSensitive(srcElemForKey)
			if isKeySensitive && c.skipSensitive {
				c.logger.Debugf("      Skipping sensitive src elem (idx %d, type %s) for key (skipSensitive=true).", i, srcElemForKey.Type())
				continue
			}

			var destMapKey reflect.Value
			copiableUsedForKey := false

			if srcElemForKey.IsValid() && srcElemForKey.Type().Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) && srcElemForKey.CanInterface() {
				c.logger.Debugf("      Src elem (idx %d, type %s) for key is Copiable.", i, srcElemForKey.Type())
				var copiableInst Copiable
				receiverForKey := reflect.New(srcElemForKey.Type())
				var okAssertKey bool

				if receiverForKey.Type().Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) {
					copiableInst, okAssertKey = receiverForKey.Interface().(Copiable)
				}
				if !okAssertKey {
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
					if destMapKey.Type().AssignableTo(destKeyType) {
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
					mapErr := fmt.Errorf("sensitive map key (idx %d, type %s) not transformed by Copiable (Overwrite=false): %w", i, srcElemForKey.Type(), ErrSensitiveTypeCopied)
					if c.handlePotentialError(mapErr, ctx) {
						continue
					}
					return mapErr
				}
			}

			if !copiableUsedForKey {
				if isKeySensitive {
					keyErr := fmt.Errorf("sensitive map key (idx %d, type %s) was not transformed by Copiable: %w", i, srcElemForKey.Type(), ErrSensitiveTypeCopied)
					if c.handlePotentialError(keyErr, ctx) {
						continue
					}
					return keyErr
				}
				if srcElemType.AssignableTo(destKeyType) {
					destMapKey = srcElemForKey
				} else if srcElemType.ConvertibleTo(destKeyType) {
					destMapKey = srcElemForKey.Convert(destKeyType)
				} else {
					keyErr := fmt.Errorf("internal error or unhandled Copiable for key: cannot convert slice elem %s to map key %s (idx %d)", srcElemType, destKeyType, i)
					if c.handlePotentialError(keyErr, ctx) {
						continue
					}
					return keyErr
				}
			}

			if !destMapKey.IsValid() {
				c.logger.Warnf("      Resulting destMapKey for slice idx %d is invalid. Skipping.", i)
				continue
			}

			if !c.overwrite {
				if existingVal := dest.MapIndex(destMapKey); existingVal.IsValid() && !mf.IsEmptyValue(existingVal) {
					c.logger.Debugf("      -> Skipping map key '%v': Overwrite=false and key exists non-empty.", destMapKey.Interface())
					continue
				}
			}

			var destValueStr string
			stringerSrc := srcElemForKey
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
