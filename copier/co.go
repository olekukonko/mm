// copier/co.go
package copier

import (
	"fmt"
	"github.com/olekukonko/mm/mf"
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
		c.logger.Debugf("   -> SKIPPING Sensitive src value for Ptr element (c.skipSensitive=true). Dest Ptr %s preserved.", destType)
		return nil
	}

	copiableInterface := reflect.TypeOf((*Copiable)(nil)).Elem()
	var copiableToCallType reflect.Type
	var copiableReceiver reflect.Value

	// Determine the type that implements Copiable and prepare its receiver
	if src.IsValid() && src.Type().Implements(copiableInterface) {
		copiableToCallType = src.Type()
		copiableReceiver = src // Assume src is already the correct receiver or can be made so
		if copiableToCallType.Kind() != reflect.Ptr && src.Kind() == reflect.Ptr && !src.IsNil() {
			copiableReceiver = src.Elem() // If Copiable is on T, but src is *T
		}
		c.logger.Debugf("   Copiable check: Using Copiable from source type %s.", copiableToCallType)
	} else if destElemType.Implements(copiableInterface) {
		copiableToCallType = destElemType
		copiableReceiver = reflect.New(destElemType).Elem() // Receiver is T
		c.logger.Debugf("   Copiable check: Using Copiable from dest element type %s.", copiableToCallType)
	} else if dest.Type().Implements(copiableInterface) { // Check if *T implements Copiable
		copiableToCallType = dest.Type()
		copiableReceiver = reflect.New(destElemType) // Receiver is *T (dest.Type())
		c.logger.Debugf("   Copiable check: Using Copiable from dest pointer type %s.", copiableToCallType)
	}

	if isSensitive && (copiableToCallType == nil || !copiableToCallType.Implements(copiableInterface)) {
		errMsg := fmt.Sprintf("sensitive pointer's target (dest type %s from src type %s) has no Copiable implementation on relevant types", dest.Type(), src.Type())
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
	if c.skipEmptyValue && mf.IsEmptyValue(src) {
		c.logger.Debugf("   -> Skipping Ptr element assign: Src value for element is empty.")
		if c.overwrite && dest.CanSet() && !dest.IsNil() {
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	var destElem reflect.Value
	originalDestIsNil := dest.IsNil()

	if originalDestIsNil {
		if !dest.CanSet() {
			return fmt.Errorf("cannot set nil pointer dest %s", destType)
		}
		c.logger.Debugf("   Initializing nil dest Ptr %s of elem type %s", destType, destElemType)
		newAllocatedPtr := reflect.New(destElemType)
		dest.Set(newAllocatedPtr)
		destElem = newAllocatedPtr.Elem()
	} else {
		destElem = dest.Elem()
		if !c.overwrite && destElem.IsValid() && !mf.IsBasicTypeZero(destElem) && !mf.IsEmptyValue(destElem) {
			c.logger.Debugf("   -> Skipping Ptr element assign: Overwrite=false and dest element non-zero/non-empty.")
			return nil
		}
	}

	if !destElem.IsValid() && destElemType.Kind() != reflect.Interface { // Allow invalid destElem if its type is interface (e.g. for nil *interface{})
		return fmt.Errorf("destination element invalid for pointer %s after setup (elem type %s)", destType, destElemType)
	}

	copiableUsed := false
	if copiableToCallType != nil && copiableToCallType.Implements(copiableInterface) && src.IsValid() && src.CanInterface() {
		c.logger.Debugf("   Src value (type %s) to be handled by Copiable on type %s for Ptr's target (%s)...", src.Type(), copiableToCallType, dest.Type())

		var copiableInst Copiable
		var okAssert bool

		if !copiableReceiver.IsValid() {
			return fmt.Errorf("internal error: copiableReceiver not valid for type %s", copiableToCallType)
		}

		// Ensure receiver is interfaceable to call .Interface().(Copiable)
		// If copiableReceiver is already the instance (e.g. src was passed), use it.
		// Otherwise, it's from reflect.New().
		var receiverForAssert reflect.Value = copiableReceiver
		if copiableReceiver.Type() != copiableToCallType && copiableReceiver.Kind() == reflect.Ptr && copiableToCallType.Kind() != reflect.Ptr {
			// e.g. copiableToCallType is T, copiableReceiver is *T (from New(T)) -> use Elem()
			if !copiableReceiver.IsNil() {
				receiverForAssert = copiableReceiver.Elem()
			}
		}

		if receiverForAssert.CanInterface() {
			copiableInst, okAssert = receiverForAssert.Interface().(Copiable)
		}
		// If the method has a pointer receiver but copiableToCallType is a value type,
		// and receiverForAssert is that value type.
		if !okAssert && receiverForAssert.Kind() != reflect.Ptr && receiverForAssert.CanAddr() {
			addrOfReceiver := receiverForAssert.Addr()
			if addrOfReceiver.Type().Implements(copiableInterface) && addrOfReceiver.CanInterface() {
				copiableInst, okAssert = addrOfReceiver.Interface().(Copiable)
			}
		}

		if !okAssert {
			err = fmt.Errorf("Copiable (copyPtr): failed to assert receiver (type %s, method holder type %s) to Copiable for item %s", receiverForAssert.Type(), copiableToCallType, dest.Type().String())
			if c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}

		var setValueFunc func(valueFromCopiable interface{}) error
		setValueFunc = func(valueFromCopiable interface{}) error {
			if valueFromCopiable == nil {
				targetIsPointerFieldItself := copiableToCallType == dest.Type()

				if targetIsPointerFieldItself || (destElemType.Kind() == reflect.Interface && copiableToCallType == destElemType) {
					if !dest.CanSet() {
						return fmt.Errorf("Copiable.SetValue: cannot set pointer %s to nil", dest.Type())
					}
					dest.Set(reflect.Zero(dest.Type()))
					c.logger.Debugf("     Copiable.SetValue set pointer %s to nil.", dest.Type())
				} else {
					currentDestElemForZero := dest.Elem()
					if !currentDestElemForZero.IsValid() {
						return fmt.Errorf("Copiable.SetValue: destElem for %s is invalid for zeroing", dest.Type())
					}
					if !currentDestElemForZero.CanSet() {
						return fmt.Errorf("Copiable.SetValue: cannot set element %s to zero", currentDestElemForZero.Type())
					}
					if dest.IsNil() {
						if !dest.CanSet() {
							return fmt.Errorf("Copiable.SetValue: cannot initialize nil pointer %s for element zeroing", dest.Type())
						}
						dest.Set(reflect.New(destElemType))
						currentDestElemForZero = dest.Elem()
						c.logger.Debugf("     Copiable.SetValue: Initialized dest pointer %s for element zeroing.", dest.Type())
					}
					currentDestElemForZero.Set(reflect.Zero(currentDestElemForZero.Type()))
					c.logger.Debugf("     Copiable.SetValue set element %s (of %s) to zero.", currentDestElemForZero.Type(), dest.Type())
				}
				return nil
			}

			vv := reflect.ValueOf(valueFromCopiable)
			if !vv.IsValid() {
				// Handle invalid value from Copiable by treating it as if Copiable returned nil.
				// This means we effectively call the nil handling path of SetValue.
				if copiableToCallType == dest.Type() || (destElemType.Kind() == reflect.Interface && copiableToCallType == destElemType) {
					if !dest.CanSet() {
						return fmt.Errorf("Copiable.SetValue: cannot set pointer %s to nil (from invalid Copiable value)", dest.Type())
					}
					dest.Set(reflect.Zero(dest.Type()))
				} else {
					currentDestElemForZero := dest.Elem()
					if !currentDestElemForZero.IsValid() {
						return fmt.Errorf("Copiable.SetValue: destElem for %s is invalid for zeroing (from invalid Copiable value)", dest.Type())
					}
					if !currentDestElemForZero.CanSet() {
						return fmt.Errorf("Copiable.SetValue: cannot set element %s to zero (from invalid Copiable value)", currentDestElemForZero.Type())
					}
					if dest.IsNil() {
						if !dest.CanSet() {
							return fmt.Errorf("Copiable.SetValue: cannot init nil pointer %s for element zeroing (from invalid Copiable value)", dest.Type())
						}
						dest.Set(reflect.New(destElemType))
						currentDestElemForZero = dest.Elem()
					}
					currentDestElemForZero.Set(reflect.Zero(currentDestElemForZero.Type()))
				}
				c.logger.Debugf("     Copiable.SetValue (from invalid reflect.Value) handled as nil for Ptr %s", dest.Type())
				return nil
			}

			targetIsPointerFieldItself := copiableToCallType == dest.Type() || (copiableToCallType == src.Type() && src.Type() == dest.Type())

			if targetIsPointerFieldItself {
				if vv.Type().AssignableTo(dest.Type()) {
					if !dest.CanSet() {
						return fmt.Errorf("Copiable.SetValue: cannot set pointer %s with %s", dest.Type(), vv.Type())
					}
					dest.Set(vv)
					c.logger.Debugf("     Copiable.SetValue set pointer %s with assignable pointer type %s.", dest.Type(), vv.Type())
				} else if vv.Type().ConvertibleTo(dest.Type()) {
					if !dest.CanSet() {
						return fmt.Errorf("Copiable.SetValue: cannot set pointer %s with convertible %s", dest.Type(), vv.Type())
					}
					dest.Set(vv.Convert(dest.Type()))
					c.logger.Debugf("     Copiable.SetValue set pointer %s with converted pointer type %s.", dest.Type(), vv.Type())
				} else {
					return fmt.Errorf("Copiable.SetValue for ptr field %s: Copiable on pointer type returned %s, not assignable/convertible to %s", dest.Type(), vv.Type(), dest.Type())
				}
			} else {
				currentDestElem := dest.Elem()
				if !currentDestElem.IsValid() {
					return fmt.Errorf("Copiable.SetValue: destElem for %s is invalid for setting value", dest.Type())
				}
				if !currentDestElem.CanSet() {
					return fmt.Errorf("Copiable.SetValue: cannot set element %s with %s", currentDestElem.Type(), vv.Type())
				}

				if dest.IsNil() {
					if !dest.CanSet() {
						return fmt.Errorf("Copiable.SetValue: cannot initialize nil pointer %s for element", dest.Type())
					}
					dest.Set(reflect.New(destElemType))
					currentDestElem = dest.Elem()
					c.logger.Debugf("     Copiable.SetValue: Initialized dest pointer %s for element set.", dest.Type())
				}

				if vv.Type().AssignableTo(currentDestElem.Type()) {
					currentDestElem.Set(vv)
					c.logger.Debugf("     Copiable.SetValue set element %s (of %s) with assignable value %s.", currentDestElem.Type(), dest.Type(), vv.Type())
				} else if vv.Type().ConvertibleTo(currentDestElem.Type()) {
					currentDestElem.Set(vv.Convert(currentDestElem.Type()))
					c.logger.Debugf("     Copiable.SetValue set element %s (of %s) with converted value %s.", currentDestElem.Type(), dest.Type(), vv.Type())
				} else {
					return fmt.Errorf("Copiable.SetValue for ptr field %s: Copiable on element type returned %s, not assignable/convertible to element %s", dest.Type(), vv.Type(), currentDestElem.Type())
				}
			}
			return nil
		}

		ctxCopyForPtr := &CopyContext{
			FieldName: dest.Type().String(),
			Settings:  struct{ Overwrite, SkipEmpty, SkipZero, SkipNil bool }{c.overwrite, c.skipEmptyValue, c.skipZeroValue, c.skipNilSrc},
			Overwrite: false,
			Copier:    c,
			SetValue:  setValueFunc,
		}

		valueReturnedByCopiable, errCopy := copiableInst.Copy(ctxCopyForPtr, src.Interface())
		if errCopy != nil {
			copyErr := fmt.Errorf("Copiable.Copy for pointer's target (dest type %s, Copiable on %s) failed: %w", dest.Type(), copiableToCallType, errCopy)
			if c.handlePotentialError(copyErr, ctx) {
				return nil
			}
			return copyErr
		}

		if ctxCopyForPtr.Overwrite {
			copiableUsed = true
			if errSetValue := ctxCopyForPtr.SetValue(valueReturnedByCopiable); errSetValue != nil {
				setErr := fmt.Errorf("setting value from Copiable.Copy for pointer's target: %w", errSetValue)
				if c.handlePotentialError(setErr, ctx) {
					return nil
				}
				return setErr
			}
		} else if isSensitive {
			sensitiveErr := fmt.Errorf("sensitive pointer's target (dest type %s) not overwritten by Copiable: %w", dest.Type(), ErrSensitiveTypeCopied)
			if c.handlePotentialError(sensitiveErr, ctx) {
				return nil
			}
			return sensitiveErr
		}

		if copiableUsed {
			c.logger.Debugf("   Copiable handled pointer's target. Finished copyPtr.")
			return nil
		}
		c.logger.Debugf("   Copiable for pointer's target ran but did not set Overwrite. Proceeding to recursive copy for element.")
	}

	if !dest.IsNil() {
		destElem = dest.Elem()
	} else {
		c.logger.Debugf("   Dest Ptr %s is nil and Copiable did not set it. Cannot recurse on element.", dest.Type())
		return nil
	}

	if !destElem.IsValid() {
		if destElemType.Kind() == reflect.Interface && dest.IsNil() && dest.CanSet() {
			c.logger.Debugf("   Dest Ptr %s is nil ptr to interface. Recursive copy on dest Ptr itself.", dest.Type())
			return c.CopyRecursive(dest, src, ctx)
		}
		return fmt.Errorf("destination element became invalid before recursive call in copyPtr for %s (src: %s)", dest.Type(), src.Type())
	}
	if !destElem.CanSet() {
		return fmt.Errorf("destination element %s is not settable for recursive copy in copyPtr", destElem.Type())
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

	if !src.IsValid() || ((src.Kind() == reflect.Interface || src.Kind() == reflect.Ptr) && src.IsNil()) {
		logMsg := "invalid"
		if src.IsValid() {
			logMsg = "nil pointer/interface"
		}
		c.logger.Debugf("   Source (for Interface dest) is %s.", logMsg)
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}

	actualSrcForCycleCheck := src
	if src.Kind() == reflect.Interface && !src.IsNil() {
		actualSrcForCycleCheck = src.Elem()
	}
	if c.enableCycleDetect && actualSrcForCycleCheck.IsValid() && actualSrcForCycleCheck.Kind() == reflect.Ptr && !actualSrcForCycleCheck.IsNil() {
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

	srcToProcess := src
	var ultimateSrcConcreteVal reflect.Value = srcToProcess
	originalSrcKindBeforeUltimateDeref := ultimateSrcConcreteVal.Kind()

	for ultimateSrcConcreteVal.Kind() == reflect.Interface || ultimateSrcConcreteVal.Kind() == reflect.Ptr {
		if ultimateSrcConcreteVal.IsNil() {
			ultimateSrcConcreteVal = reflect.Value{}
			break
		}
		if ultimateSrcConcreteVal.Kind() == reflect.Ptr && originalSrcKindBeforeUltimateDeref != reflect.Ptr {
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

	if srcToProcess.IsValid() && srcToProcess.Kind() == reflect.Interface && !srcToProcess.IsNil() {
		if srcToProcess.Type().Implements(copiableInterfaceType) {
			copiableTargetVal = srcToProcess
			copiableTargetType = srcToProcess.Type()
			c.logger.Debugf("   Chosen Copiable target: original interface type %s", copiableTargetType)
		} else {
			c.logger.Debugf("   Copiable target (from interface element): concrete type %s", copiableTargetType)
		}
	} else if copiableTargetType.Implements(copiableInterfaceType) {
		c.logger.Debugf("   Chosen Copiable target: concrete type %s", copiableTargetType)
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
	if c.skipEmptyValue && mf.IsEmptyValue(ultimateSrcConcreteVal) {
		c.logger.Debugf("   Skipping interface assign: Src concrete (type %s) is empty.", ultimateSrcConcreteVal.Type())
		if c.overwrite && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}

	if !c.overwrite && dest.IsValid() && !dest.IsNil() {
		if destElemIf := dest.Elem(); destElemIf.IsValid() && !mf.IsEmptyValue(destElemIf) {
			c.logger.Debugf("   Skipping interface assign: Overwrite=false, dest interface non-empty (contains %s).", destElemIf.Type())
			return nil
		}
	}

	isDestEmptyInterface := dest.Type().NumMethod() == 0 && dest.Type().Name() == ""
	if isDestEmptyInterface && (copiableTargetType == nil || !copiableTargetType.Implements(copiableInterfaceType)) {
		if ultimateSrcConcreteVal.CanInterface() {
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
	}

	var valueToSetInInterface reflect.Value
	copiableUsed := false

	if copiableTargetType != nil && copiableTargetType.Implements(copiableInterfaceType) && copiableTargetVal.CanInterface() {
		c.logger.Debugf("   Src value (type %s, from copiableTargetVal type %s) implements Copiable for interface assignment...", src.Type(), copiableTargetType)
		var copiableInst Copiable
		var okAssert bool

		var receiverForCopiableCall reflect.Value
		if copiableTargetType.Kind() == reflect.Interface {
			receiverForCopiableCall = copiableTargetVal
		} else if copiableTargetType.Kind() == reflect.Ptr {
			receiverForCopiableCall = reflect.New(copiableTargetType.Elem())
		} else {
			receiverForCopiableCall = reflect.New(copiableTargetType).Elem()
		}

		if receiverForCopiableCall.CanInterface() {
			copiableInst, okAssert = receiverForCopiableCall.Interface().(Copiable)
		}
		if !okAssert && receiverForCopiableCall.Kind() != reflect.Ptr && receiverForCopiableCall.CanAddr() {
			if receiverForCopiableCall.Addr().CanInterface() {
				if receiverForCopiableCall.Addr().Type().Implements(copiableInterfaceType) {
					copiableInst, okAssert = receiverForCopiableCall.Addr().Interface().(Copiable)
				}
			}
		}

		if !okAssert {
			err = fmt.Errorf("Copiable (copyInterface): failed assert %s to Copiable using receiver %s", copiableTargetType, receiverForCopiableCall.Type())
			if c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}
		ctxCopyInterfaceSetValueFunc := func(valFromCopiable interface{}) error { return nil }
		ctxCopyInterface := &CopyContext{
			FieldName: "interfaceValue",
			Settings:  struct{ Overwrite, SkipEmpty, SkipZero, SkipNil bool }{c.overwrite, c.skipEmptyValue, c.skipZeroValue, c.skipNilSrc},
			Overwrite: false, Copier: c,
			SetValue: ctxCopyInterfaceSetValueFunc,
		}

		valueReturnedByCopiable, errCopy := copiableInst.Copy(ctxCopyInterface, copiableTargetVal.Interface())
		if errCopy != nil {
			err = fmt.Errorf("Copiable.Copy for interface value (src type %s, Copiable on %s): %w", src.Type(), copiableTargetType, errCopy)
			if c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}

		if ctxCopyInterface.Overwrite {
			copiableUsed = true
			if valueReturnedByCopiable == nil {
				valueToSetInInterface = reflect.Zero(dest.Type())
			} else {
				valueToSetInInterface = reflect.ValueOf(valueReturnedByCopiable)
			}
			if valueToSetInInterface.IsValid() {
				if !valueToSetInInterface.Type().AssignableTo(dest.Type()) {
					err = fmt.Errorf("Copiable for interface (src %s, Copiable on %s) returned %s, not assignable to dest interface %s", src.Type(), copiableTargetType, valueToSetInInterface.Type(), dest.Type())
					if c.handlePotentialError(err, ctx) {
						return nil
					}
					return err
				}
			} else if valueToSetInInterface.Kind() == reflect.Invalid && dest.Type().Kind() == reflect.Interface {
			} else {
				err = fmt.Errorf("Copiable for interface (src %s, Copiable on %s) resulted in an unexpected invalid value for dest interface %s", src.Type(), copiableTargetType, dest.Type())
				if c.handlePotentialError(err, ctx) {
					return nil
				}
				return err
			}
			c.logger.Debugf("     Copiable.Copy provided value of type %v for interface %s.", valueToSetInInterface.Type(), dest.Type())
		} else if isSensitive {
			err = fmt.Errorf("sensitive interface value (src type %s, concrete %s, Copiable on %s) not overwritten by Copiable: %w", srcToProcess.Type(), ultimateSrcConcreteVal.Type(), copiableTargetType, ErrSensitiveTypeCopied)
			if c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}
	}

	if !copiableUsed {
		typeForHolder := ultimateSrcConcreteVal.Type()
		valueToCopyIntoHolder := ultimateSrcConcreteVal

		convertedSuccessfully := false
		if !typeForHolder.AssignableTo(dest.Type()) {
			if convMap, ok := c.typeConverters[ultimateSrcConcreteVal.Type()]; ok {
				for destConvType, converterFunc := range convMap {
					valAssignable := destConvType.AssignableTo(dest.Type())
					ptrAssignable := false
					if destConvType.Kind() != reflect.Interface {
						ptrAssignable = reflect.PtrTo(destConvType).AssignableTo(dest.Type())
					}

					if valAssignable || ptrAssignable {
						c.logger.Debugf("   Interface: Trying converter %s -> %s for dest interface %s", ultimateSrcConcreteVal.Type(), destConvType, dest.Type())
						convertedValFromFunc, convErr := converterFunc(ultimateSrcConcreteVal)
						if convErr == nil && convertedValFromFunc.IsValid() {
							if convertedValFromFunc.Type().AssignableTo(dest.Type()) {
								typeForHolder = convertedValFromFunc.Type()
								valueToCopyIntoHolder = convertedValFromFunc
								originalSrcKindBeforeUltimateDeref = valueToCopyIntoHolder.Kind()
								c.logger.Debugf("   Interface: Used converter. New holder type %s. Original kind for holder logic: %s", typeForHolder, originalSrcKindBeforeUltimateDeref)
								convertedSuccessfully = true
								break
							} else if convertedValFromFunc.CanAddr() && convertedValFromFunc.Addr().Type().AssignableTo(dest.Type()) {
								typeForHolder = convertedValFromFunc.Type()
								valueToCopyIntoHolder = convertedValFromFunc
								originalSrcKindBeforeUltimateDeref = reflect.Ptr
								c.logger.Debugf("   Interface: Used converter (value to ptr). New holder base type %s. Original kind for holder logic forced to Ptr.", typeForHolder)
								convertedSuccessfully = true
								break
							}
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

		if typeForHolder.Kind() == reflect.Interface {
			ccType := valueToCopyIntoHolder.Type()
			if valueToCopyIntoHolder.Type().AssignableTo(dest.Type()) {
				if ccType.Kind() == reflect.Ptr {
					holder = reflect.New(ccType.Elem())
					if !valueToCopyIntoHolder.IsNil() {
						if errRec := c.CopyRecursive(holder.Elem(), valueToCopyIntoHolder.Elem(), ctx); errRec != nil {
							return fmt.Errorf("deep copying to Ptr holder.Elem (typeForHolder was interface, src was Ptr %s) for interface: %w", ccType, errRec)
						}
					}
				} else {
					holder = reflect.New(ccType).Elem()
					if errRec := c.CopyRecursive(holder, valueToCopyIntoHolder, ctx); errRec != nil {
						return fmt.Errorf("deep copying to Value holder (typeForHolder was interface, src was Value %s) for interface: %w", ccType, errRec)
					}
				}
				c.logger.Debugf("   Interface: typeForHolder was interface, created new holder of type %s based on valueToCopyIntoHolder's type %s.", holder.Type(), ccType)
			} else {
				return fmt.Errorf("internal logic error: typeForHolder is interface %s, but valueToCopyIntoHolder %s not assignable to dest %s", typeForHolder, valueToCopyIntoHolder.Type(), dest.Type())
			}
		} else if shouldHolderBePointer {
			holder = reflect.New(typeForHolder)
			c.logger.Debugf("   Interface: Creating Ptr holder *%s.", typeForHolder)
			if errRec := c.CopyRecursive(holder.Elem(), valueToCopyIntoHolder, ctx); errRec != nil {
				return fmt.Errorf("deep copying to Ptr holder.Elem for interface: %w", errRec)
			}
		} else {
			holder = reflect.New(typeForHolder).Elem()
			c.logger.Debugf("   Interface: Creating Value holder %s.", typeForHolder)
			if errRec := c.CopyRecursive(holder, valueToCopyIntoHolder, ctx); errRec != nil {
				return fmt.Errorf("deep copying to Value holder for interface: %w", errRec)
			}
		}

		if c.enableCycleDetect && actualSrcForCycleCheck.IsValid() && actualSrcForCycleCheck.Kind() == reflect.Ptr && !actualSrcForCycleCheck.IsNil() {
			destToTrackForCycle := holder
			if holder.IsValid() && holder.Kind() != reflect.Ptr && holder.Kind() != reflect.Interface && holder.CanAddr() {
				if holder.Addr().Type().AssignableTo(dest.Type()) {
					destToTrackForCycle = holder.Addr()
				}
			}
			if destToTrackForCycle.IsValid() &&
				(destToTrackForCycle.Kind() == reflect.Ptr || destToTrackForCycle.Kind() == reflect.Interface) {
				ctx.Detector.Track(actualSrcForCycleCheck.Pointer(), actualSrcForCycleCheck.Type().Elem(), destToTrackForCycle)
			}
		}
		valueToSetInInterface = holder
	}

	if !valueToSetInInterface.IsValid() {
		if dest.Type().Kind() == reflect.Interface {
			dest.Set(reflect.Zero(dest.Type()))
			c.logger.Debugf("   Setting dest interface %s to typed nil because valueToSetInInterface is invalid.", dest.Type())
			return nil
		}
		return fmt.Errorf("internal error: valueToSetInInterface became invalid for non-interface dest %s", dest.Type())

	}

	if c.skipZeroValue && valueToSetInInterface.IsValid() && valueToSetInInterface.IsZero() && valueToSetInInterface.Kind() != reflect.Interface {
		c.logger.Debugf("   Skipping set of zero value (type %s) into interface (skipZeroValue=true)", valueToSetInInterface.Type())
		if c.overwrite {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}
	if c.skipEmptyValue && mf.IsEmptyValue(valueToSetInInterface) {
		c.logger.Debugf("   Skipping set of empty value (type %s) into interface (skipEmptyValue=true)", valueToSetInInterface.Type())
		if c.overwrite {
			dest.Set(reflect.Zero(dest.Type()))
		}
		return nil
	}

	if !valueToSetInInterface.Type().AssignableTo(dest.Type()) {
		return fmt.Errorf("internal error or type mismatch: final value type %s not assignable to dest interface %s", valueToSetInInterface.Type(), dest.Type())
	}

	c.logger.Debugf("   Setting dest interface %s with value of type %s (Kind: %s)", dest.Type(), valueToSetInInterface.Type(), valueToSetInInterface.Kind())
	dest.Set(valueToSetInInterface)
	return nil
}
