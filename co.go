// co.go
package mm

import (
	"fmt"
	"reflect"
)

func (c *Copier) copyPtr(dest, src reflect.Value, ctx *SysContext) (err error) {
	destType := dest.Type()
	if destType.Kind() != reflect.Ptr {
		return fmt.Errorf("copyPtr: destination type %s is not a pointer", destType)
	}
	destElemType := destType.Elem()
	c.logger.Indent(ctx.depth + 1)
	defer c.logger.Indent(ctx.depth)
	c.logger.Debugf("Copying -> Ptr (dest ptr type: %s, src value for elem: %s)", destType, src.Type())

	if !src.IsValid() {
		c.logger.Debugf("   Source value (for Ptr element) is invalid.")
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite && dest.CanSet() && !dest.IsNil() {
			c.logger.Debugf("   Setting dest Ptr %s to nil due to invalid source.", destType)
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}
	srcKind := src.Kind()
	if (srcKind == reflect.Ptr || srcKind == reflect.Interface || srcKind == reflect.Slice || srcKind == reflect.Map || srcKind == reflect.Chan || srcKind == reflect.Func) && src.IsNil() {
		c.logger.Debugf("   Source value (for Ptr element) is a nil pointer/interface/collection.")
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite && dest.CanSet() && !dest.IsNil() {
			c.logger.Debugf("   Setting dest Ptr %s to nil.", destType)
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	isSensitive := c.checkIsSensitive(src)
	if isSensitive && c.skipSensitive {
		c.logger.Debugf("   -> SKIPPING Sensitive src value for Ptr element (c.skipSensitive=true).")
		if c.overwrite && dest.CanSet() && !dest.IsNil() {
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	copiableInterface := reflect.TypeOf((*Copiable)(nil)).Elem()
	copiableToCallType := destElemType // Default to Copiable on destination element type
	if src.IsValid() && src.Type().Implements(copiableInterface) {
		copiableToCallType = src.Type() // Prefer Copiable on source value's type
	} else if destElemType.Implements(copiableInterface) {
		// copiableToCallType is already destElemType
	} else if reflect.PtrTo(destElemType).Implements(copiableInterface) { // Check *DestElemType
		copiableToCallType = reflect.PtrTo(destElemType)
	}

	if isSensitive && !copiableToCallType.Implements(copiableInterface) {
		errMsg := fmt.Sprintf("sensitive pointer's element (dest type %s from src type %s) has no Copiable implementation on relevant types (%s, %s, *%s)", destElemType, src.Type(), src.Type(), destElemType, destElemType)
		c.logger.Warnf("   -> ERROR: %s", errMsg)
		errSensitive := fmt.Errorf("%s: %w", errMsg, ErrSensitiveTypeCopied)
		if c.handlePotentialError(errSensitive, ctx) {
			return nil
		}
		return errSensitive
	}

	if c.skipZeroValue && src.IsValid() && src.IsZero() {
		c.logger.Debugf("   -> Skipping Ptr element assign: Src value for element is zero.")
		if c.overwrite && dest.CanSet() && !dest.IsNil() {
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}
	if c.skipEmptyValue && isEmptyValue(src) {
		c.logger.Debugf("   -> Skipping Ptr element assign: Src value for element is empty.")
		if c.overwrite && dest.CanSet() && !dest.IsNil() {
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	var destElem reflect.Value

	if !dest.IsValid() {
		return fmt.Errorf("destination pointer value invalid in copyPtr (original dest type: %s)", destType)
	}

	if dest.IsNil() {
		if !dest.CanSet() {
			return fmt.Errorf("cannot set nil pointer dest %s", destType)
		}
		c.logger.Debugf("   Initializing nil dest Ptr %s of elem type %s", destType, destElemType)
		newAllocatedPtr := reflect.New(destElemType)
		dest.Set(newAllocatedPtr)
		destElem = newAllocatedPtr.Elem()
	} else {
		destElem = dest.Elem()
		// No need to log here, CopyRecursive would have logged original dest. Elem info if needed.
		if !c.overwrite && destElem.IsValid() && !isBasicTypeZero(destElem) && !isEmptyValue(destElem) {
			c.logger.Debugf("   -> Skipping Ptr element assign: Overwrite=false and dest element non-zero/non-empty.")
			return nil
		}
	}

	if !destElem.IsValid() {
		return fmt.Errorf("destination element invalid for pointer %s after setup", destType)
	}
	if !destElem.CanSet() {
		return fmt.Errorf("cannot set pointer element of type %s (destination element not settable)", destElem.Type())
	}

	copiableUsed := false
	if copiableToCallType.Implements(copiableInterface) && src.IsValid() && src.CanInterface() {
		c.logger.Debugf("   Src value (type %s) to be handled by Copiable on type %s for Ptr's element (%s)...", src.Type(), copiableToCallType, destElem.Type())
		var copiableInst Copiable
		var okAssert bool
		receiverProvider := reflect.New(copiableToCallType)
		if copiableToCallType.Kind() == reflect.Ptr {
			if receiverProvider.CanInterface() {
				copiableInst, okAssert = receiverProvider.Interface().(Copiable)
			}
		} else {
			if receiverProvider.Elem().CanInterface() {
				copiableInst, okAssert = receiverProvider.Elem().Interface().(Copiable)
			}
			if !okAssert && receiverProvider.CanInterface() {
				copiableInst, okAssert = receiverProvider.Interface().(Copiable)
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
			Overwrite: false,
			Copier:    c,
			SetValue: func(valueFromCopiable interface{}) error {
				if !destElem.CanSet() {
					return fmt.Errorf("Copiable.SetValue: cannot set pointer element of type %s", destElem.Type())
				}
				if valueFromCopiable == nil {
					destElem.Set(reflect.Zero(destElem.Type()))
					c.logger.Debugf("     Copiable.SetValue set pointer element to zero value (nil received).")
					return nil
				}
				vv := reflect.ValueOf(valueFromCopiable)
				if !vv.IsValid() {
					destElem.Set(reflect.Zero(destElem.Type()))
					c.logger.Debugf("     Copiable.SetValue set pointer element to zero value (invalid reflect.Value received).")
					return nil
				}
				if vv.Type().AssignableTo(destElem.Type()) {
					destElem.Set(vv)
				} else if vv.Type().ConvertibleTo(destElem.Type()) {
					destElem.Set(vv.Convert(destElem.Type()))
				} else {
					return fmt.Errorf("Copiable.SetValue: type mismatch for pointer element: cannot assign %s to %s", vv.Type(), destElem.Type())
				}
				c.logger.Debugf("     Copiable.SetValue successfully set pointer element to value of type %s", vv.Type())
				return nil
			},
		}
		valueReturnedByCopiable, errCopy := copiableInst.Copy(ctxCopy, src.Interface())
		if errCopy != nil {
			copyErr := fmt.Errorf("Copiable.Copy for pointer's element (dest type %s) failed: %w", destElem.Type(), errCopy)
			if c.handlePotentialError(copyErr, ctx) {
				return nil
			}
			return copyErr
		}

		if ctxCopy.Overwrite {
			copiableUsed = true
			if errSetValue := ctxCopy.SetValue(valueReturnedByCopiable); errSetValue != nil {
				setErr := fmt.Errorf("setting value from Copiable.Copy for pointer's element: %w", errSetValue)
				if c.handlePotentialError(setErr, ctx) {
					return nil
				}
				return setErr
			}
		} else if isSensitive {
			sensitiveErr := fmt.Errorf("sensitive pointer's element (dest type %s) not overwritten by Copiable: %w", destElem.Type(), ErrSensitiveTypeCopied)
			if c.handlePotentialError(sensitiveErr, ctx) {
				return nil
			}
			return sensitiveErr
		}

		if copiableUsed {
			c.logger.Debugf("   Copiable handled pointer's element. Finished copyPtr.")
			return nil
		}
		c.logger.Debugf("   Copiable for pointer's element ran but did not set Overwrite. Proceeding to recursive copy for element.")
	}

	c.logger.Debugf("   Recursing on dest element (%s) <- src value (%s)", destElem.Type(), src.Type())
	return c.CopyRecursive(destElem, src, ctx)
}

// copyInterface handles copying a source value into a destination interface.
// `src` is the original source value passed for the interface slot (could be anything).
// `dest` is the interface slot itself.
func (c *Copier) copyInterface(dest, src reflect.Value, ctx *SysContext) (err error) {
	c.logger.Indent(ctx.depth + 1)
	defer c.logger.Indent(ctx.depth)
	c.logger.Debugf("Copying -> Interface (src type: %s (kind %s) -> dest type: %s)", src.Type(), src.Kind(), dest.Type())

	if !dest.CanSet() {
		return fmt.Errorf("destination interface %s is not settable", dest.Type())
	}

	// Handle nil/invalid source for the interface
	if !src.IsValid() || ((src.Kind() == reflect.Interface || src.Kind() == reflect.Ptr) && src.IsNil()) {
		logMsg := "invalid"
		if src.IsValid() { // It's valid but a nil pointer/interface
			logMsg = "nil pointer/interface"
		}
		c.logger.Debugf("   Source (for Interface dest) is %s.", logMsg)
		if c.skipNilSrc {
			return nil // Skip setting the interface
		}
		if c.overwrite && dest.IsValid() && !dest.IsNil() { // If overwriting, set interface to its zero value (nil interface)
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}

	// actualSrcForCycleCheck is src or src.Elem() if src is interface, used for cycle keying.
	actualSrcForCycleCheck := src
	if src.Kind() == reflect.Interface && !src.IsNil() {
		actualSrcForCycleCheck = src.Elem()
	}
	// Cycle detection for pointers being put into interfaces
	if c.enableCycleDetect && actualSrcForCycleCheck.Kind() == reflect.Ptr && !actualSrcForCycleCheck.IsNil() {
		srcPtr := actualSrcForCycleCheck.Pointer()
		srcElemTypeForKey := actualSrcForCycleCheck.Type().Elem()
		if existingDestPointer, visited := ctx.Detector.Check(srcPtr, srcElemTypeForKey); visited {
			if existingDestPointer.IsValid() && existingDestPointer.Type().AssignableTo(dest.Type()) {
				c.logger.Debugf("   Cycle REASSIGN (Interface): Dest %s set with existing Visited Ptr %s.", dest.Type(), existingDestPointer.Type())
				dest.Set(existingDestPointer)
				return nil
			} else {
				c.logger.Debugf("     Cycle (Interface): Cannot assign existing Ptr type %s to interface type %s. Fall through.", existingDestPointer.Type(), dest.Type())
			}
		}
	}

	// Determine the concrete value and its original structure (pointer or value) from the source 'src'.
	// 'srcToProcess' is what we'll examine for Copiable and concrete type.
	srcToProcess := src
	var ultimateSrcConcreteVal reflect.Value = srcToProcess
	originalSrcKindBeforeUltimateDeref := ultimateSrcConcreteVal.Kind() // Kind of srcToProcess

	for ultimateSrcConcreteVal.Kind() == reflect.Interface || ultimateSrcConcreteVal.Kind() == reflect.Ptr {
		if ultimateSrcConcreteVal.IsNil() {
			ultimateSrcConcreteVal = reflect.Value{}
			break
		}
		if ultimateSrcConcreteVal.Kind() == reflect.Ptr && originalSrcKindBeforeUltimateDeref != reflect.Ptr {
			// Update only if we encounter a pointer for the first time at this level of deref
			originalSrcKindBeforeUltimateDeref = reflect.Ptr
		}
		ultimateSrcConcreteVal = ultimateSrcConcreteVal.Elem()
		if !ultimateSrcConcreteVal.IsValid() {
			break
		}
	}

	if !ultimateSrcConcreteVal.IsValid() {
		c.logger.Debugf("   Source concrete value (for interface) is invalid after dereferencing.")
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}

	isSensitive := c.checkIsSensitive(ultimateSrcConcreteVal)
	copiableInterfaceType := reflect.TypeOf((*Copiable)(nil)).Elem()

	copiableTargetVal := ultimateSrcConcreteVal
	copiableTargetType := ultimateSrcConcreteVal.Type()

	if srcToProcess.IsValid() && srcToProcess.Kind() == reflect.Interface && !srcToProcess.IsNil() &&
		srcToProcess.Type().Implements(copiableInterfaceType) {
		copiableTargetVal = srcToProcess
		copiableTargetType = srcToProcess.Type()
		c.logger.Debugf("   Chosen Copiable target: original interface type %s", copiableTargetType)
	} else if ultimateSrcConcreteVal.Type().Implements(copiableInterfaceType) {
		c.logger.Debugf("   Chosen Copiable target: ultimate concrete type %s", copiableTargetType)
	} else {
		copiableTargetType = nil
	}

	if isSensitive && c.skipSensitive {
		c.logger.Debugf("   SKIPPING Sensitive src concrete (type %s) for Interface (skipSensitive=true)", ultimateSrcConcreteVal.Type())
		if c.overwrite && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}

	if isSensitive && (copiableTargetType == nil || !copiableTargetType.Implements(copiableInterfaceType)) {
		errSensitive := fmt.Errorf("sensitive interface source (concrete %s, original src type %s) lacks a suitable Copiable implementation: %w", ultimateSrcConcreteVal.Type(), srcToProcess.Type(), ErrSensitiveTypeCopied)
		if c.handlePotentialError(errSensitive, ctx) {
			return nil
		}
		return errSensitive
	}

	if c.skipZeroValue && ultimateSrcConcreteVal.IsValid() && ultimateSrcConcreteVal.IsZero() {
		c.logger.Debugf("   Skipping interface assign: Src concrete (type %s) is zero.", ultimateSrcConcreteVal.Type())
		if c.overwrite && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}
	if c.skipEmptyValue && isEmptyValue(ultimateSrcConcreteVal) {
		c.logger.Debugf("   Skipping interface assign: Src concrete (type %s) is empty.", ultimateSrcConcreteVal.Type())
		if c.overwrite && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}

	if !c.overwrite && dest.IsValid() && !dest.IsNil() {
		if destElemIf := dest.Elem(); destElemIf.IsValid() && !isEmptyValue(destElemIf) {
			c.logger.Debugf("   Skipping interface assign: Overwrite=false, dest interface non-empty (contains %s).", destElemIf.Type())
			return nil
		}
	}

	isDestEmptyInterface := dest.Type().NumMethod() == 0 && dest.Type().Name() == ""
	if isDestEmptyInterface && ultimateSrcConcreteVal.CanInterface() {
		if stringer, ok := ultimateSrcConcreteVal.Interface().(fmt.Stringer); ok {
			c.logger.Debugf("   Interface: Source (type %s) is fmt.Stringer, dest is empty interface. Setting string value.", ultimateSrcConcreteVal.Type())
			dest.Set(reflect.ValueOf(stringer.String()))
			return nil
		}
		if errInterface, ok := ultimateSrcConcreteVal.Interface().(error); ok {
			c.logger.Debugf("   Interface: Source (type %s) is error, dest is empty interface. Setting error string value.", ultimateSrcConcreteVal.Type())
			dest.Set(reflect.ValueOf(errInterface.Error()))
			return nil
		}
	}

	var valueToSetInInterface reflect.Value
	copiableUsed := false

	if copiableTargetType != nil && copiableTargetType.Implements(copiableInterfaceType) && copiableTargetVal.CanInterface() {
		c.logger.Debugf("   Src value (type %s) implements Copiable for interface assignment...", copiableTargetType)
		var copiableInst Copiable
		var okAssert bool

		receiverProvider := reflect.New(copiableTargetType)
		if copiableTargetType.Kind() == reflect.Ptr || copiableTargetType.Kind() == reflect.Interface {
			if receiverProvider.CanInterface() {
				copiableInst, okAssert = receiverProvider.Interface().(Copiable)
			}
		} else {
			if receiverProvider.Elem().CanInterface() {
				copiableInst, okAssert = receiverProvider.Elem().Interface().(Copiable)
			}
			if !okAssert && receiverProvider.CanInterface() {
				copiableInst, okAssert = receiverProvider.Interface().(Copiable)
			}
		}

		if !okAssert {
			err = fmt.Errorf("Copiable (copyInterface): failed assert %s to Copiable", copiableTargetType)
			if c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}
		ctxCopy := &CopyContext{
			FieldName: "interfaceValue",
			Settings:  struct{ Overwrite, SkipEmpty, SkipZero, SkipNil bool }{c.overwrite, c.skipEmptyValue, c.skipZeroValue, c.skipNilSrc},
			Overwrite: false, Copier: c,
			SetValue: func(valFromCopiable interface{}) error { return nil }}

		valueReturnedByCopiable, errCopy := copiableInst.Copy(ctxCopy, copiableTargetVal.Interface())
		if errCopy != nil {
			err = fmt.Errorf("Copiable.Copy for interface value (src type %s): %w", copiableTargetType, errCopy)
			if c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}

		if ctxCopy.Overwrite {
			copiableUsed = true
			if valueReturnedByCopiable == nil {
				valueToSetInInterface = reflect.Zero(dest.Type())
			} else {
				valueToSetInInterface = reflect.ValueOf(valueReturnedByCopiable)
			}
			// Ensure the value from Copiable is assignable to the destination interface
			if valueToSetInInterface.IsValid() { // It can be an invalid value if reflect.Zero(interfaceType) was used
				if !valueToSetInInterface.Type().AssignableTo(dest.Type()) {
					err = fmt.Errorf("Copiable for interface (src %s) returned %s, not assignable to dest interface %s", copiableTargetType, valueToSetInInterface.Type(), dest.Type())
					if c.handlePotentialError(err, ctx) {
						return nil
					}
					return err
				}
			} else if valueToSetInInterface.Kind() == reflect.Invalid && dest.Type().Kind() == reflect.Interface {
				// This is fine, reflect.Zero(interfaceType) is an invalid value, representing typed nil interface
			} else {
				// This case (invalid value but not for an interface type) should ideally not happen
				err = fmt.Errorf("Copiable for interface (src %s) resulted in an unexpected invalid value for dest interface %s", copiableTargetType, dest.Type())
				if c.handlePotentialError(err, ctx) {
					return nil
				}
				return err
			}
			c.logger.Debugf("     Copiable.Copy provided value of type %v for interface %s.", valueToSetInInterface.Type(), dest.Type())
		} else if isSensitive {
			err = fmt.Errorf("sensitive interface value (src type %s, concrete %s) not overwritten by Copiable: %w", srcToProcess.Type(), ultimateSrcConcreteVal.Type(), ErrSensitiveTypeCopied)
			if c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}
	}

	if !copiableUsed {
		typeForHolder := ultimateSrcConcreteVal.Type()
		valueToCopyIntoHolder := ultimateSrcConcreteVal

		// Attempt to use type converters
		// We want to see if ultimateSrcConcreteVal can be converted to *any* type X,
		// such that X (or *X) is assignable to dest.Type() (the interface).
		convertedSuccessfully := false
		if convMap, ok := c.typeConverters[ultimateSrcConcreteVal.Type()]; ok {
			for destConvType, converterFunc := range convMap {
				// Check if destConvType or *destConvType is assignable to the target interface type
				valAssignable := reflect.New(destConvType).Elem().Type().AssignableTo(dest.Type())
				ptrAssignable := reflect.New(destConvType).Type().AssignableTo(dest.Type())

				if valAssignable || ptrAssignable {
					convertedVal, convErr := converterFunc(ultimateSrcConcreteVal)
					if convErr == nil && convertedVal.IsValid() {
						// Now check if the *result* of the conversion is assignable
						if convertedVal.Type().AssignableTo(dest.Type()) {
							typeForHolder = convertedVal.Type()
							valueToCopyIntoHolder = convertedVal // This is the value we'll deep copy if it's complex
							c.logger.Debugf("   Interface: Using converter %s -> %s. Holder will be based on %s.", ultimateSrcConcreteVal.Type(), destConvType, typeForHolder)
							convertedSuccessfully = true
							break
						} else if convertedVal.CanAddr() && convertedVal.Addr().Type().AssignableTo(dest.Type()) {
							// The converter returned T, but interface needs *T
							// We will make a holder of *T, and copy T into its element.
							typeForHolder = convertedVal.Type() // Holder will be *T, based on element T
							valueToCopyIntoHolder = convertedVal
							originalSrcKindBeforeUltimateDeref = reflect.Ptr // Treat as if original was pointer for holder creation
							c.logger.Debugf("   Interface: Using converter %s -> %s. Holder will be *%s (by taking Addr of converted).", ultimateSrcConcreteVal.Type(), destConvType, typeForHolder)
							convertedSuccessfully = true
							break
						}
					}
				}
			}
		}
		if convertedSuccessfully {
			c.logger.Debugf("   Interface: After checking converters, effective type for holder is %s, value to copy is %s", typeForHolder, valueToCopyIntoHolder.Type())
		}

		var holder reflect.Value
		shouldHolderBePointer := originalSrcKindBeforeUltimateDeref == reflect.Ptr

		if typeForHolder.Kind() == reflect.Ptr { // If converter returned a pointer type
			shouldHolderBePointer = true
			// Holder needs to be of type *T, and we'll copy element of valueToCopyIntoHolder into holder's element.
			// Example: converter returns *SimpleDest. typeForHolder is *SimpleDest.
			// holder = reflect.New(*SimpleDest.Elem()) which is reflect.New(SimpleDest) -> *SimpleDest
			holder = reflect.New(typeForHolder.Elem()) // holder is *T
			if valueToCopyIntoHolder.Kind() == reflect.Ptr && !valueToCopyIntoHolder.IsNil() {
				if errRec := c.CopyRecursive(holder.Elem(), valueToCopyIntoHolder.Elem(), ctx); errRec != nil {
					return fmt.Errorf("deep copying to Ptr holder.Elem (typeForHolder was Ptr) for interface: %w", errRec)
				}
			} else if valueToCopyIntoHolder.Kind() != reflect.Ptr { // Converter returned *T, but valueToCopy is T. This shouldn't happen with good converter.
				err = fmt.Errorf("type mismatch: converter indicated pointer type %s but provided value %s", typeForHolder, valueToCopyIntoHolder.Type())
				if c.handlePotentialError(err, ctx) {
					return nil
				}
				return err
			} else { // valueToCopyIntoHolder is nil pointer
				// holder remains a pointer to a zero value; assign Zero to make it nil pointer if interface allows
				if dest.Type().Kind() == reflect.Interface { // Ensure we are setting an interface
					holder = reflect.Zero(typeForHolder) // e.g. (*SimpleDest)(nil)
				}
			}
			c.logger.Debugf("   Interface: Creating Ptr holder %s (typeForHolder was Ptr).", typeForHolder)
		} else if shouldHolderBePointer { // Original was pointer, or converter implies pointer result for interface
			holder = reflect.New(typeForHolder) // Holder is *T
			c.logger.Debugf("   Interface: Creating Ptr holder *%s.", typeForHolder)
			if errRec := c.CopyRecursive(holder.Elem(), valueToCopyIntoHolder, ctx); errRec != nil {
				return fmt.Errorf("deep copying to Ptr holder.Elem for interface: %w", errRec)
			}
		} else { // Value type for holder
			holder = reflect.New(typeForHolder).Elem() // Holder is T
			c.logger.Debugf("   Interface: Creating Value holder %s.", typeForHolder)
			if errRec := c.CopyRecursive(holder, valueToCopyIntoHolder, ctx); errRec != nil {
				return fmt.Errorf("deep copying to Value holder for interface: %w", errRec)
			}
		}

		if c.enableCycleDetect && actualSrcForCycleCheck.Kind() == reflect.Ptr && !actualSrcForCycleCheck.IsNil() {
			trackableDest := holder
			if holder.IsValid() && holder.Kind() != reflect.Ptr && holder.CanAddr() {
				trackableDest = holder.Addr()
			}
			if trackableDest.IsValid() && (trackableDest.Kind() == reflect.Ptr || trackableDest.Kind() == reflect.Interface) {
				ctx.Detector.Track(actualSrcForCycleCheck.Pointer(), actualSrcForCycleCheck.Type().Elem(), trackableDest)
			}
		}
		valueToSetInInterface = holder
	}

	if !valueToSetInInterface.IsValid() {
		dest.Set(reflect.Zero(dest.Type()))
		c.logger.Debugf("   Setting dest interface %s to typed nil because valueToSetInInterface is invalid.", dest.Type())
		return nil
	}

	if c.skipZeroValue && valueToSetInInterface.IsValid() && valueToSetInInterface.IsZero() && valueToSetInInterface.Kind() != reflect.Interface {
		c.logger.Debugf("   Skipping set of zero value (type %s) into interface (skipZeroValue=true)", valueToSetInInterface.Type())
		if c.overwrite {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}
	if c.skipEmptyValue && isEmptyValue(valueToSetInInterface) {
		c.logger.Debugf("   Skipping set of empty value (type %s) into interface (skipEmptyValue=true)", valueToSetInInterface.Type())
		if c.overwrite {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}

	if !valueToSetInInterface.Type().AssignableTo(dest.Type()) {
		// If it's not assignable, but the value inside is (e.g. interface holds *T, dest is interface{})
		// This should ideally be caught by type converter logic making typeForHolder correct.
		// For now, if direct assignment fails, this is an error.
		return fmt.Errorf("internal error: final value type %s not assignable to dest interface %s", valueToSetInInterface.Type(), dest.Type())
	}

	c.logger.Debugf("   Setting dest interface %s with value of type %s (Kind: %s)", dest.Type(), valueToSetInInterface.Type(), valueToSetInInterface.Kind())
	dest.Set(valueToSetInInterface)
	return nil
}
