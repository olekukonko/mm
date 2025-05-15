// co.go
package mm

import (
	"errors"
	"fmt"
	"reflect"
	"unsafe"
)

func (c *Copier) copyPtr(dest, src reflect.Value, ctx *SysContext) (err error) {
	// ... (copyPtr logic remains largely the same as previous corrected version) ...
	// Ensure logging indent/dedent matches entry/exit
	destType := dest.Type()
	if destType.Kind() != reflect.Ptr {
		return fmt.Errorf("copyPtr: destination type %s is not a pointer", destType)
	}
	destElemType := destType.Elem()
	c.logger.Indent(ctx.depth + 1)
	defer c.logger.Indent(ctx.depth) // Ensure reset on exit
	c.logger.Debug("Copying -> Ptr (dest ptr type: %s, src value for elem: %s)", destType, src.Type())

	if !src.IsValid() { /* ... (handle invalid src) ... */
		c.logger.Debug("   Source value (for Ptr element) is invalid.")
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite && dest.CanSet() && !dest.IsNil() {
			c.logger.Debug("   Setting dest Ptr %s to nil due to invalid source.", destType)
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}
	srcKind := src.Kind()
	if (srcKind == reflect.Ptr || srcKind == reflect.Interface || srcKind == reflect.Slice || srcKind == reflect.Map || srcKind == reflect.Chan || srcKind == reflect.Func) && src.IsNil() { /* ... (handle nil src value) ... */
		c.logger.Debug("   Source value (for Ptr element) is a nil pointer/interface/collection.")
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite && dest.CanSet() && !dest.IsNil() {
			c.logger.Debug("   Setting dest Ptr %s to nil.", destType)
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	isSensitive := c.checkIsSensitive(src) // Use helper
	if isSensitive && c.skipSensitive {    /* ... (handle skip sensitive) ... */
		c.logger.Debug("   -> SKIPPING Sensitive src value for Ptr element (c.skipSensitive=true).")
		if c.overwrite && dest.CanSet() && !dest.IsNil() {
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	copiableToCallType := destElemType
	if isSensitive && src.IsValid() && src.Type().Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) {
		copiableToCallType = src.Type()
	}

	if isSensitive && !copiableToCallType.Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) { /* ... (handle sensitive + no Copiable error) ... */
		c.logger.Debug("   -> SKIPPING Sensitive src value for Ptr element: Type %s (chosen for Copiable) has no Copiable.", copiableToCallType)
		if c.overwrite && dest.CanSet() && !dest.IsNil() {
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}
	if c.skipZeroValue && src.IsValid() && src.IsZero() { /* ... (handle skip zero) ... */
		c.logger.Debug("   -> Skipping Ptr element assign: Src value for element is zero.")
		if c.overwrite && dest.CanSet() && !dest.IsNil() {
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}
	if c.skipEmptyValue && isEmptyValue(src) { /* ... (handle skip empty) ... */
		c.logger.Debug("   -> Skipping Ptr element assign: Src value for element is empty.")
		if c.overwrite && dest.CanSet() && !dest.IsNil() {
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	var destElem reflect.Value
	if !dest.IsValid() {
		return fmt.Errorf("destination pointer value invalid in copyPtr (original dest type: %s)", destType)
	}

	if dest.IsNil() { /* ... (initialize nil pointer dest) ... */
		if !dest.CanSet() {
			return fmt.Errorf("cannot set nil pointer dest %s", destType)
		}
		c.logger.Debug("   Initializing nil dest Ptr %s of elem type %s", destType, destElemType)
		newDestPtr := reflect.New(destElemType)
		dest.Set(newDestPtr)
		destElem = newDestPtr.Elem()
		var elemAddr uintptr
		if destElem.CanAddr() {
			elemAddr = destElem.UnsafeAddr()
		}
		c.logger.Debug("   Initialized nil dest Ptr %s, Elem is %s (addr of Elem: %p, Ptr itself: %p)", destType, destElem.Type(), unsafe.Pointer(elemAddr), unsafe.Pointer(newDestPtr.Pointer()))
	} else { /* ... (handle non-nil pointer dest) ... */
		destElem = dest.Elem()
		var elemAddr uintptr
		if destElem.CanAddr() {
			elemAddr = destElem.UnsafeAddr()
		}
		c.logger.Debug("   Dest Ptr %s is not nil. Elem is %s (addr of Elem: %p, Ptr itself: %p)", destType, destElem.Type(), unsafe.Pointer(elemAddr), unsafe.Pointer(dest.Pointer()))
		if !c.overwrite && destElem.IsValid() && !isBasicTypeZero(destElem) && !isEmptyValue(destElem) {
			c.logger.Debug("   -> Skipping Ptr element assign: Overwrite=false and dest element non-zero/non-empty.")
			return nil
		}
	}

	if !destElem.IsValid() {
		return fmt.Errorf("destination element invalid for pointer %s after setup", destType)
	}
	if !destElem.CanSet() {
		return fmt.Errorf("cannot set pointer element of type %s (destination element not settable)", destElem.Type())
	}

	// --- Copiable on Element Type or Src Value's Type ---
	copiableUsed := false
	if copiableToCallType.Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) && src.IsValid() && src.CanInterface() {
		c.logger.Debug("   Src value (type %s) to be handled by Copiable on type %s for Ptr's element (%s)...", src.Type(), copiableToCallType, destElem.Type())
		// ... (Copiable instantiation logic - reuse the corrected one from processItem conceptually) ...
		var copiableInst Copiable
		var okAssert bool
		receiverProvider := reflect.New(copiableToCallType)
		if receiverProvider.CanInterface() {
			copiableInst, okAssert = receiverProvider.Interface().(Copiable)
		}
		if !okAssert && copiableToCallType.Kind() != reflect.Interface {
			if receiverProvider.Elem().CanInterface() {
				copiableInst, okAssert = receiverProvider.Elem().Interface().(Copiable)
			}
		}
		if !okAssert {
			err = fmt.Errorf("Copiable (copyPtr): failed to assert instance of %s for item %s", copiableToCallType, dest.Type().String()+".Elem")
			if c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}

		ctxCopy := &CopyContext{
			FieldName: dest.Type().String() + ".Elem",
			Settings:  struct{ Overwrite, SkipEmpty, SkipZero, SkipNil bool }{c.overwrite, c.skipEmptyValue, c.skipZeroValue, c.skipNilSrc},
			Overwrite: false, // Copiable implementer sets this
			Copier:    c,
			SetValue: func(valueFromCopiable interface{}) error { /* ... (SetValue logic - same as before) ... */
				if !destElem.CanSet() {
					return fmt.Errorf("Copiable.SetValue: cannot set pointer element of type %s", destElem.Type())
				}
				if valueFromCopiable == nil {
					destElem.Set(reflect.Zero(destElem.Type()))
					c.logger.Debug("     Copiable.SetValue set pointer element to zero value (nil received).")
					return nil
				}
				vv := reflect.ValueOf(valueFromCopiable)
				if !vv.IsValid() {
					destElem.Set(reflect.Zero(destElem.Type()))
					c.logger.Debug("     Copiable.SetValue set pointer element to zero value (invalid reflect.Value received).")
					return nil
				}
				if vv.Type().AssignableTo(destElem.Type()) {
					destElem.Set(vv)
				} else if vv.Type().ConvertibleTo(destElem.Type()) {
					destElem.Set(vv.Convert(destElem.Type()))
				} else {
					return fmt.Errorf("Copiable.SetValue: type mismatch for pointer element: cannot assign %s to %s", vv.Type(), destElem.Type())
				}
				c.logger.Debug("     Copiable.SetValue successfully set pointer element to value of type %s", vv.Type())
				return nil
			},
		}
		valueReturnedByCopiable, errCopy := copiableInst.Copy(ctxCopy, src.Interface())
		if errCopy != nil { /* ... (handle Copiable.Copy error) ... */
			copyErr := fmt.Errorf("Copiable.Copy for pointer's element (dest type %s) failed: %w", destElem.Type(), errCopy)
			if c.handlePotentialError(copyErr, ctx) {
				return nil
			}
			return copyErr
		}

		if ctxCopy.Overwrite {
			copiableUsed = true
			if errSetValue := ctxCopy.SetValue(valueReturnedByCopiable); errSetValue != nil { /* ... (handle SetValue error) ... */
				setErr := fmt.Errorf("setting value from Copiable.Copy for pointer's element: %w", errSetValue)
				if c.handlePotentialError(setErr, ctx) {
					return nil
				}
				return setErr
			}
		} else if isSensitive { /* ... (handle sensitive + no overwrite error) ... */
			sensitiveErr := fmt.Errorf("sensitive pointer's element (dest type %s) not overwritten by Copiable: %w", destElem.Type(), ErrSensitiveTypeCopied)
			if c.handlePotentialError(sensitiveErr, ctx) {
				return nil
			}
			return sensitiveErr
		}

		if copiableUsed {
			c.logger.Debug("   Copiable handled pointer's element. Finished copyPtr.")
			return nil
		}
		c.logger.Debug("   Copiable for pointer's element ran but did not set Overwrite. Proceeding to recursive copy for element.")
	}

	c.logger.Debug("   Recursing on dest element (%s) <- src value (%s)", destElem.Type(), src.Type())
	return c.copyRecursive(destElem, src, ctx) // src is already the concrete value for the element
}

func (c *Copier) copyInterface(dest, src reflect.Value, ctx *SysContext) (err error) {
	c.logger.Indent(ctx.depth + 1)
	defer c.logger.Indent(ctx.depth) // Ensure reset on exit
	c.logger.Debug("Copying -> Interface (src type: %s (kind %s) -> dest type: %s)", src.Type(), src.Kind(), dest.Type())

	if !dest.CanSet() {
		return fmt.Errorf("destination interface %s is not settable", dest.Type())
	}

	// Handle nil/invalid source
	if !src.IsValid() || ((src.Kind() == reflect.Interface || src.Kind() == reflect.Ptr) && src.IsNil()) {
		logMsg := "invalid"
		if src.IsValid() {
			logMsg = "nil pointer/interface"
		}
		c.logger.Debug("   Source (for Interface dest) is %s.", logMsg)
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}

	// Cycle detection (same as before)
	actualSrcForCycleCheck := src
	if src.Kind() == reflect.Interface && !src.IsNil() {
		actualSrcForCycleCheck = src.Elem()
	}
	if c.enableCycleDetect && actualSrcForCycleCheck.Kind() == reflect.Ptr && !actualSrcForCycleCheck.IsNil() {
		srcPtr := actualSrcForCycleCheck.Pointer()
		srcElemTypeForKey := actualSrcForCycleCheck.Type().Elem()
		if existingDestPointer, visited := ctx.detector.Check(srcPtr, srcElemTypeForKey); visited {
			if existingDestPointer.IsValid() && existingDestPointer.Type().AssignableTo(dest.Type()) {
				c.logger.Debug("   Cycle REASSIGN (Interface): Dest %s set with existing visited Ptr %s.", dest.Type(), existingDestPointer.Type())
				dest.Set(existingDestPointer)
				return nil
			} else {
				c.logger.Debug("     Cycle (Interface): Cannot assign existing Ptr %s to interface type %s. Fall through.", existingDestPointer.Type(), dest.Type())
			}
		}
	}

	// Get concrete value from src
	srcConcreteVal := src
	originalSrcKindBeforeDeref := src.Kind()
	if srcConcreteVal.Kind() == reflect.Interface && !srcConcreteVal.IsNil() {
		srcConcreteVal = srcConcreteVal.Elem()
		originalSrcKindBeforeDeref = srcConcreteVal.Kind()
	}
	isSrcConcretePtr := false
	if srcConcreteVal.Kind() == reflect.Ptr {
		isSrcConcretePtr = true
		if !srcConcreteVal.IsNil() {
			srcConcreteVal = srcConcreteVal.Elem()
		} else {
			srcConcreteVal = reflect.Value{}
		}
	}

	if !srcConcreteVal.IsValid() { /* ... (handle invalid concrete source) ... */
		c.logger.Debug("   Source concrete value (for interface) is invalid after dereferencing.")
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}

	// Sensitivity & Skip checks on concrete value (same as before)
	isSensitive := c.checkIsSensitive(srcConcreteVal)
	if isSensitive && c.skipSensitive { /* ... skip ... */
		c.logger.Debug("   SKIPPING Sensitive src concrete (type %s) for Interface (skipSensitive=true)", srcConcreteVal.Type())
		if c.overwrite && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}
	if isSensitive && !srcConcreteVal.Type().Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) { /* ... skip ... */
		c.logger.Debug("   SKIPPING Sensitive src concrete (type %s) for Interface: Type has no Copiable", srcConcreteVal.Type())
		if c.overwrite && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}
	if c.skipZeroValue && srcConcreteVal.IsZero() { /* ... skip ... */
		c.logger.Debug("   Skipping interface assign: Src concrete (type %s) is zero.", srcConcreteVal.Type())
		if c.overwrite && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}
	if c.skipEmptyValue && isEmptyValue(srcConcreteVal) { /* ... skip ... */
		c.logger.Debug("   Skipping interface assign: Src concrete (type %s) is empty.", srcConcreteVal.Type())
		if c.overwrite && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}

	// Overwrite check for destination interface (same as before)
	if !c.overwrite && dest.IsValid() && !dest.IsNil() {
		if destElemIf := dest.Elem(); destElemIf.IsValid() && !isEmptyValue(destElemIf) {
			c.logger.Debug("   Skipping interface assign: Overwrite=false, dest interface non-empty (contains %s).", destElemIf.Type())
			return nil
		}
	}

	// Handle Stringer/error to empty interface{} (same as before)
	isDestEmptyInterface := dest.Type().NumMethod() == 0 && dest.Type().Name() == ""
	if isDestEmptyInterface && srcConcreteVal.CanInterface() {
		if stringer, ok := srcConcreteVal.Interface().(fmt.Stringer); ok {
			c.logger.Debug("   Interface: Source (type %s) is fmt.Stringer, dest is empty interface. Setting string value.", srcConcreteVal.Type())
			dest.Set(reflect.ValueOf(stringer.String()))
			return nil
		}
		if errInterface, ok := srcConcreteVal.Interface().(error); ok {
			c.logger.Debug("   Interface: Source (type %s) is error, dest is empty interface. Setting error string value.", srcConcreteVal.Type())
			dest.Set(reflect.ValueOf(errInterface.Error()))
			return nil
		}
	}

	var valueToSetInInterface reflect.Value
	copiableUsed := false

	// Copiable on concrete source type (same as before)
	if srcConcreteVal.Type().Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) && srcConcreteVal.CanInterface() { /* ... (Copiable logic remains same) ... */
		c.logger.Debug("   Src concrete value (type %s) implements Copiable for interface assignment...", srcConcreteVal.Type())
		var copiableInst Copiable
		var okAssert bool
		receiverForCopiable := reflect.New(srcConcreteVal.Type())
		if receiverForCopiable.CanInterface() {
			copiableInst, okAssert = receiverForCopiable.Interface().(Copiable)
		}
		if !okAssert && receiverForCopiable.Elem().CanInterface() {
			copiableInst, okAssert = receiverForCopiable.Elem().Interface().(Copiable)
		}
		if !okAssert {
			err = fmt.Errorf("Copiable (copyInterface): failed assert %s", srcConcreteVal.Type())
			if c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}
		ctxCopy := &CopyContext{Settings: struct{ Overwrite, SkipEmpty, SkipZero, SkipNil bool }{c.overwrite, c.skipEmptyValue, c.skipZeroValue, c.skipNilSrc}, Overwrite: false, Copier: c, SetValue: func(valFromCopiable interface{}) error { /*...*/ return nil }}
		valueReturnedByCopiable, errCopy := copiableInst.Copy(ctxCopy, srcConcreteVal.Interface())
		if errCopy != nil {
			return fmt.Errorf("Copiable.Copy for interface value (src type %s): %w", srcConcreteVal.Type(), errCopy)
		}

		if ctxCopy.Overwrite {
			copiableUsed = true
			if valueReturnedByCopiable == nil {
				valueToSetInInterface = reflect.Zero(dest.Type())
			} else {
				valueToSetInInterface = reflect.ValueOf(valueReturnedByCopiable)
			}
			if valueToSetInInterface.IsValid() && !valueToSetInInterface.Type().AssignableTo(dest.Type()) {
				err = fmt.Errorf("Copiable for interface (src %s) returned %s, not assignable to dest interface %s", srcConcreteVal.Type(), valueToSetInInterface.Type(), dest.Type())
				if c.handlePotentialError(err, ctx) {
					return nil
				}
				return err
			}
			c.logger.Debug("     Copiable.Copy provided value of type %s for interface %s.", valueToSetInInterface.Type(), dest.Type())
		} else if isSensitive {
			return fmt.Errorf("sensitive interface value (src type %s) not overwritten by Copiable: %w", srcConcreteVal.Type(), ErrSensitiveTypeCopied)
		}
	}

	// *** FIX for Interface Copy Type Mismatch ***
	if !copiableUsed {
		// Determine the target type for the holder.
		// If a converter exists from srcConcrete to dest.Type(), use dest.Type().
		// Otherwise, use srcConcrete's type.
		finalTargetElemType := srcConcreteVal.Type() // Default to source concrete type
		convertedSrcVal := srcConcreteVal            // Use original concrete val by default

		// Check for type converter: srcConcreteType -> destType (the interface itself)
		// This allows converting src to something assignable to the dest interface.
		if converted, canConvert, convErr := c.tryConvert(srcConcreteVal, dest.Type()); canConvert && convErr == nil {
			c.logger.Debug("   Interface: Using converted value type %s for destination interface %s.", converted.Type(), dest.Type())
			finalTargetElemType = converted.Type()
			convertedSrcVal = converted // Use the converted value for the copy
		} else if convErr != nil && !errors.Is(convErr, ErrNoConverter) {
			return convErr // Propagate real conversion error
		} else {
			c.logger.Debug("   Interface: No specific converter found for %s -> %s. Using source concrete type %s.", srcConcreteVal.Type(), dest.Type(), finalTargetElemType)
		}

		// Create holder based on whether the original source was a pointer or value,
		// using the determined finalTargetElemType.
		var holder reflect.Value
		if isSrcConcretePtr || originalSrcKindBeforeDeref == reflect.Ptr {
			holder = reflect.New(finalTargetElemType) // Create *T (where T is finalTargetElemType)
			c.logger.Debug("   Interface: Original src was Ptr-like. Creating Ptr holder *%s.", finalTargetElemType)

			// Cycle track original source pointer to the new pointer holder
			if c.enableCycleDetect && actualSrcForCycleCheck.IsValid() && actualSrcForCycleCheck.Kind() == reflect.Ptr && !actualSrcForCycleCheck.IsNil() {
				ctx.detector.Track(actualSrcForCycleCheck.Pointer(), actualSrcForCycleCheck.Type().Elem(), holder)
			}
			// Copy into the element of the new pointer holder
			if errRec := c.copyRecursive(holder.Elem(), convertedSrcVal, ctx); errRec != nil {
				return fmt.Errorf("deep copying Ptr's content for interface (holder.Elem type %s from concreteSrcVal type %s): %w", holder.Elem().Type(), convertedSrcVal.Type(), errRec)
			}
		} else {
			holder = reflect.New(finalTargetElemType).Elem() // Create T (where T is finalTargetElemType)
			c.logger.Debug("   Interface: Original src was Value-like. Creating Value holder %s.", finalTargetElemType)
			// Copy directly into the new value holder
			if errRec := c.copyRecursive(holder, convertedSrcVal, ctx); errRec != nil {
				return fmt.Errorf("deep copying Value for interface (holder type %s from concreteSrcVal type %s): %w", holder.Type(), convertedSrcVal.Type(), errRec)
			}
		}
		valueToSetInInterface = holder
	}
	// *** End FIX ***

	// Final checks and set (same as before)
	if !valueToSetInInterface.IsValid() {
		dest.Set(reflect.Zero(dest.Type()))
		return nil
	}
	if c.skipZeroValue && valueToSetInInterface.IsZero() {
		if c.overwrite {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}
	if c.skipEmptyValue && isEmptyValue(valueToSetInInterface) {
		if c.overwrite {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}
	if !valueToSetInInterface.Type().AssignableTo(dest.Type()) {
		return fmt.Errorf("internal error: final value type %s not assignable to dest interface %s", valueToSetInInterface.Type(), dest.Type())
	}

	c.logger.Debug("   Setting dest interface %s with value of type %s (Kind: %s)", dest.Type(), valueToSetInInterface.Type(), valueToSetInInterface.Kind())
	dest.Set(valueToSetInInterface)
	return nil
}
