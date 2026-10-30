package copier

import (
	"fmt"
	"reflect"
)

type processItemResult struct {
	valueToSet reflect.Value
	shouldSet  bool
	err        error
}

func getNameLazy(nameCtx interface{}) string {
	switch v := nameCtx.(type) {
	case string:
		return v
	case int:
		return fmt.Sprintf("[%d]", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func (c *Copier) processItem(
	src reflect.Value,
	itemNameCtx interface{},
	baseDestType reflect.Type,
	isSrcItemSensitive bool,
	ctxSys *SysContext,
) (result processItemResult) {

	// The item name is part of error messages, so it is always computed. The
	// type/kind strings on the other hand are only used by debug logging.
	itemName := getNameLazy(itemNameCtx)
	var srcTypeStr, srcKindStr string
	isValidSrc := src.IsValid()
	if c.debugMode {
		if isValidSrc {
			srcTypeStr, srcKindStr = src.Type().String(), src.Kind().String()
		} else {
			srcTypeStr, srcKindStr = "<invalid_src_type>", "<invalid_src_kind>"
		}
	}
	c.logger.Debugf("PROCESS_ITEM Start: '%s' (Src: %s, Kind: %s -> BaseDestType: %s, ItemSensitiveFlags: srcItemSensitive=%v)", itemName, srcTypeStr, srcKindStr, baseDestType, isSrcItemSensitive)

	defer func() {
		status := "OK"
		errMsg := ""
		if result.err != nil {
			status = "ERROR"
			errMsg = fmt.Sprintf(". Err: %v", result.err)
		}
		valTypeStr := "<N/A>"
		if result.valueToSet.IsValid() {
			valTypeStr = result.valueToSet.Type().String()
		}
		c.logger.Debugf("PROCESS_ITEM End: '%s'. Status: %s, ShouldSet: %v, ValueToSetType: %s%s", itemName, status, result.shouldSet, valTypeStr, errMsg)
	}()

	copiableInterfaceType := reflect.TypeOf((*Copiable)(nil)).Elem()
	var copiableToCallType reflect.Type
	var srcConcreteForCopiableCheck reflect.Value = src
	var canUseSrcConcreteForCopiable bool
	var canAttemptCopiable bool = false
	var srcValueForCopiableInterface interface{}

	isValidSrc = src.IsValid()

	if isSrcItemSensitive && c.skipSensitive {
		c.logger.Debugf("  Skipping '%s' (sensitive item, skipSensitive=true).", itemName)
		return processItemResult{shouldSet: false}
	}
	if c.skipZeroValue && isValidSrc && src.IsZero() {
		c.logger.Debugf("  Skipping '%s' (zero value).", itemName)
		return processItemResult{shouldSet: false}
	}
	if c.skipEmptyValue && IsEmptyValue(src) {
		c.logger.Debugf("  Skipping '%s' (empty value).", itemName)
		return processItemResult{shouldSet: false}
	}
	if isValidSrc {
		k := src.Kind()
		if (k == reflect.Ptr || k == reflect.Interface || k == reflect.Map || k == reflect.Slice || k == reflect.Chan || k == reflect.Func) && src.IsNil() {
			if c.skipNilSrc {
				c.logger.Debugf("  Skipping '%s' (nil src value, skipNilSrc=true).", itemName)
				return processItemResult{shouldSet: false}
			}
		}
	} else {
		if c.skipNilSrc {
			c.logger.Debugf("  Skipping '%s' (invalid src value, skipNilSrc=true).", itemName)
			return processItemResult{shouldSet: false}
		}
		c.logger.Debugf("  Proceeding with invalid src for '%s' (skipNilSrc=false) -> Fallback.", itemName)
		goto PrepareFallback
	}

	if isValidSrc && src.Kind() == reflect.Interface && !src.IsNil() {
		srcConcreteForCopiableCheck = src.Elem()
	}
	canUseSrcConcreteForCopiable = srcConcreteForCopiableCheck.IsValid() && srcConcreteForCopiableCheck.Type().Implements(copiableInterfaceType)

	if isSrcItemSensitive {
		if canUseSrcConcreteForCopiable {
			copiableToCallType = srcConcreteForCopiableCheck.Type()
			c.logger.Debugf("   Sensitive item '%s': Using Copiable from source's concrete type %s.", itemName, copiableToCallType)
		} else if baseDestType.Implements(copiableInterfaceType) {
			copiableToCallType = baseDestType
			c.logger.Debugf("   Sensitive item '%s': Source type (or its concrete) lacks Copiable, using Copiable from dest type %s.", itemName, baseDestType)
		} else if reflect.PtrTo(baseDestType).Implements(copiableInterfaceType) {
			copiableToCallType = reflect.PtrTo(baseDestType)
			c.logger.Debugf("   Sensitive item '%s': Source/Dest lack Copiable, using Copiable from pointer to dest type *%s.", itemName, baseDestType)
		} else {
			logSrcTypeForError := "<invalid_src_type>"
			if isValidSrc {
				logSrcTypeForError = src.Type().String()
				if srcConcreteForCopiableCheck.IsValid() && src.Kind() == reflect.Interface {
					logSrcTypeForError = fmt.Sprintf("%s (concrete %s)", logSrcTypeForError, srcConcreteForCopiableCheck.Type())
				}
			}
			errMsg := fmt.Sprintf("sensitive item '%s' (src type %s) requires Copiable on source's (concrete) type, dest type (%s), or pointer to dest type (*%s), but none found", itemName, logSrcTypeForError, baseDestType, baseDestType)
			c.logger.Warnf("  -> ERROR: %s.", errMsg)
			sensitiveErr := fmt.Errorf("%s: %w", errMsg, ErrSensitiveTypeCopied)
			if c.handlePotentialError(sensitiveErr, ctxSys) {
				return processItemResult{shouldSet: false}
			}
			return processItemResult{err: sensitiveErr}
		}
	} else {
		if canUseSrcConcreteForCopiable {
			copiableToCallType = srcConcreteForCopiableCheck.Type()
		} else if baseDestType.Implements(copiableInterfaceType) {
			copiableToCallType = baseDestType
		} else if reflect.PtrTo(baseDestType).Implements(copiableInterfaceType) {
			copiableToCallType = reflect.PtrTo(baseDestType)
		} else {
			copiableToCallType = baseDestType
		}
		if copiableToCallType != baseDestType || baseDestType.Implements(copiableInterfaceType) {
			c.logger.Debugf("   Non-sensitive item '%s': Chosen type for Copiable check is %s.", itemName, copiableToCallType)
		}
	}

	if copiableToCallType.Implements(copiableInterfaceType) && isValidSrc {
		if copiableToCallType == srcConcreteForCopiableCheck.Type() {
			if srcConcreteForCopiableCheck.CanInterface() {
				srcValueForCopiableInterface = srcConcreteForCopiableCheck.Interface()
				canAttemptCopiable = true
			}
		} else {
			if src.CanInterface() {
				srcValueForCopiableInterface = src.Interface()
				canAttemptCopiable = true
			}
		}
	}

	if canAttemptCopiable {
		c.logger.Debugf("  Attempting Copiable execution for '%s' using Copiable on type %s...", itemName, copiableToCallType)

		var copiableInst Copiable
		var okAssert bool
		var instanceToCallOn reflect.Value

		if copiableToCallType.Kind() == reflect.Ptr {
			instanceToCallOn = reflect.New(copiableToCallType.Elem())
		} else {
			instanceToCallOn = reflect.New(copiableToCallType).Elem()
		}

		if instanceToCallOn.IsValid() && instanceToCallOn.CanInterface() {
			copiableInst, okAssert = instanceToCallOn.Interface().(Copiable)
		}
		if !okAssert && instanceToCallOn.IsValid() && instanceToCallOn.CanAddr() {
			addrVal := instanceToCallOn.Addr()
			if addrVal.IsValid() && addrVal.CanInterface() {
				copiableInst, okAssert = addrVal.Interface().(Copiable)
			}
		}

		if !okAssert {
			itemErr := fmt.Errorf("Copiable (processItem): failed to assert receiver (type %s, method holder type %s) to Copiable for item '%s'", instanceToCallOn.Type(), copiableToCallType, itemName)
			if c.handlePotentialError(itemErr, ctxSys) {
				return processItemResult{shouldSet: false}
			}
			return processItemResult{err: itemErr}
		}

		copierSettings := struct{ Overwrite, SkipEmpty, SkipZero, SkipNil bool }{c.overwrite, c.skipEmptyValue, c.skipZeroValue, c.skipNilSrc}
		ctxCopy := &CopyContext{
			FieldName: itemName,
			Settings:  copierSettings,
			Overwrite: false,
			SetValue:  func(value interface{}) error { return nil },
			Copier:    c,
		}

		valueReturnedByCopy, errCopy := copiableInst.Copy(ctxCopy, srcValueForCopiableInterface)
		if errCopy != nil {
			itemErr := fmt.Errorf("Copiable.Copy for '%s' (type %s) failed: %w", itemName, copiableToCallType, errCopy)
			if c.handlePotentialError(itemErr, ctxSys) {
				return processItemResult{shouldSet: false}
			}
			return processItemResult{err: itemErr}
		}

		if ctxCopy.Overwrite {
			if !c.overwrite && isSrcItemSensitive {
				c.logger.Warnf("    Copiable for sensitive '%s' set Overwrite=true, but copier is in Merge mode. This is an error for sensitive items.", itemName)
				sensitiveErr := fmt.Errorf("sensitive item '%s' (type %s) Copiable wants overwrite, but copier is in Merge mode: %w", itemName, src.Type().String(), ErrSensitiveTypeCopied)
				if c.handlePotentialError(sensitiveErr, ctxSys) {
					return processItemResult{shouldSet: false}
				}
				return processItemResult{err: sensitiveErr}
			}

			if valueReturnedByCopy != nil {
				valToRet := reflect.ValueOf(valueReturnedByCopy)
				if !valToRet.IsValid() {
					c.logger.Debugf("    Copiable for '%s' returned non-nil but reflect.ValueOf is invalid; will set zero of baseDestType %s.", itemName, baseDestType)
					return processItemResult{valueToSet: reflect.Zero(baseDestType), shouldSet: true}
				}
				if valToRet.Type().AssignableTo(baseDestType) {
					c.logger.Debugf("    Copiable for '%s' provided assignable value type %s for baseDestType %s.", itemName, valToRet.Type(), baseDestType)
					return processItemResult{valueToSet: valToRet, shouldSet: true}
				}
				if valToRet.Type().ConvertibleTo(baseDestType) {
					converted := valToRet.Convert(baseDestType)
					c.logger.Debugf("    Copiable for '%s' provided convertible value type %s, converted to %s for baseDestType %s.", itemName, valToRet.Type(), converted.Type(), baseDestType)
					return processItemResult{valueToSet: converted, shouldSet: true}
				}
				itemErr := fmt.Errorf("copiable for '%s' returned type %s, which is not assignable or convertible to destination type %s", itemName, valToRet.Type(), baseDestType)
				if c.handlePotentialError(itemErr, ctxSys) {
					return processItemResult{shouldSet: false}
				}
				return processItemResult{err: itemErr}
			} else {
				c.logger.Debugf("    Copiable for '%s' wants to set nil/zero value (returned nil) for baseDestType %s.", itemName, baseDestType)
				return processItemResult{valueToSet: reflect.Zero(baseDestType), shouldSet: true}
			}
		} else {
			if isSrcItemSensitive {
				c.logger.Warnf("    Sensitive item '%s' was handled by Copiable, but Copiable.Overwrite was false. This is an error for sensitive items.", itemName)
				sensitiveErr := fmt.Errorf("sensitive item '%s' (type %s) was handled by Copiable, but Copiable.Overwrite was false: %w", itemName, src.Type().String(), ErrSensitiveTypeCopied)
				if c.handlePotentialError(sensitiveErr, ctxSys) {
					return processItemResult{shouldSet: false}
				}
				return processItemResult{err: sensitiveErr}
			}
			c.logger.Debugf("    Copiable for '%s' ran but did not set Overwrite=true. Item will not be set by Copiable path.", itemName)
			return processItemResult{shouldSet: false}
		}
	}

PrepareFallback:
	if isSrcItemSensitive {
		c.logger.Errorf("INTERNAL LOGIC WARNING: Sensitive item '%s' (Src: %s) reached recursive copy fallback unexpectedly.", itemName, src.Type().String())
		sensitiveErr := fmt.Errorf("internal error or misconfiguration: sensitive item '%s' (type %s) attempted recursive copy fallback without custom handling: %w", itemName, src.Type().String(), ErrSensitiveTypeCopied)
		if c.handlePotentialError(sensitiveErr, ctxSys) {
			return processItemResult{shouldSet: false}
		}
		return processItemResult{err: sensitiveErr}
	}

	c.logger.Debugf("  -> Falling back to recursive copy for non-sensitive item '%s'.", itemName)

	if baseDestType == nil || baseDestType.Kind() == reflect.Invalid {
		itemErr := fmt.Errorf("internal error: cannot create holder for invalid baseDestType for item '%s'", itemName)
		if c.handlePotentialError(itemErr, ctxSys) {
			return processItemResult{shouldSet: false}
		}
		return processItemResult{err: itemErr}
	}
	holder := reflect.New(baseDestType).Elem()

	if errRec := c.CopyRecursive(holder, src, ctxSys); errRec != nil {
		itemErr := fmt.Errorf("recursive copy for '%s' (Src: %s -> DestHolder: %s) failed: %w", itemName, src.Type().String(), holder.Type(), errRec)
		return processItemResult{err: itemErr}
	}

	c.logger.Debugf("  Recursive copy for '%s' successful. Holder type: %s", itemName, holder.Type())
	return processItemResult{valueToSet: holder, shouldSet: true}
}
