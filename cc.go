// cc.go
package mm

import (
	"bytes"
	"errors"
	"fmt"
	"log" // Keep standard log for specific warnings/infos like deprecation
	"reflect"
	"runtime"
	"strings"
	"time"
	"unsafe"
)

func (c *Copier) copyRecursive(dest, src reflect.Value, ctx *SysContext) (err error) {
	c.logger.Indent(ctx.depth)
	if c.logger != nil {
		var srcTypeStr, srcKindStr string
		if src.IsValid() {
			srcTypeStr = src.Type().String()
			srcKindStr = src.Kind().String()
		} else {
			srcTypeStr = "<invalid_src_type>"
			srcKindStr = "<invalid_src_kind>"
		}
		c.logger.Debug("COPYRECURSIVE_ENTRY: Dest: %s (%s), Src: %s (%s), Overwrite: %v",
			dest.Type().String(), dest.Kind(), srcTypeStr, srcKindStr, c.overwrite)
	}

	if c.maxDepth >= 0 && ctx.depth > c.maxDepth {
		if c.logger != nil {
			c.logger.Debug("Max depth reached (%d), stopping.", c.maxDepth)
		}
		return nil
	}

	if !dest.IsValid() {
		if c.logger != nil {
			c.logger.Debug("Destination is invalid, skipping.")
		}
		return nil
	}

	originalSrcForInterfaceChecks := src
	effectiveSrc := src

	// --- Source Validity and Nil Checks ---
	if !effectiveSrc.IsValid() {
		if c.logger != nil {
			c.logger.Debug("Source is invalid.")
		}
		if c.skipNilSrc {
			if c.logger != nil {
				c.logger.Debug("Skipping copy due to invalid source (skipNilSrc=true).")
			}
			return nil
		}
		if c.overwrite {
			if dest.CanSet() {
				if c.logger != nil {
					c.logger.Debug("Zeroing settable destination %s due to invalid source.", dest.Type())
				}
				dest.Set(reflect.Zero(dest.Type()))
			} else if c.useUnsafe && dest.CanAddr() && (!isUnexportedStructField(dest) || c.copyUnexported) {
				ptr := unsafe.Pointer(dest.UnsafeAddr())
				zeroVal := reflect.Zero(dest.Type())
				if zeroVal.IsValid() {
					if c.logger != nil {
						c.logger.Debug("UNSAFE Zeroing addressable destination %s due to invalid source.", dest.Type())
					}
					reflect.NewAt(dest.Type(), ptr).Elem().Set(zeroVal)
					runtime.KeepAlive(dest)
				} else {
					if c.logger != nil {
						c.logger.Debug("Cannot zero non-settable destination %s (Zero value invalid for type), src invalid.", dest.Type())
					}
				}
			} else {
				if c.logger != nil {
					c.logger.Debug("Cannot zero non-settable destination %s due to invalid source.", dest.Type())
				}
			}
		} else {
			if c.logger != nil {
				c.logger.Debug("Overwrite=false, skipping zeroing for %s due to invalid source.", dest.Type())
			}
		}
		return nil
	}

	srcKind := effectiveSrc.Kind()
	isSrcConsideredNil := (srcKind == reflect.Ptr || srcKind == reflect.Interface || srcKind == reflect.Map || srcKind == reflect.Slice || srcKind == reflect.Chan || srcKind == reflect.Func) && effectiveSrc.IsNil()

	if isSrcConsideredNil {
		if c.logger != nil {
			c.logger.Debug("Source is considered nil (%s).", effectiveSrc.Type())
		}
		if c.skipNilSrc {
			if c.logger != nil {
				c.logger.Debug("Skipping copy due to nil source (skipNilSrc=true).")
			}
			return nil
		}
		if c.overwrite {
			if dest.CanSet() {
				if c.logger != nil {
					c.logger.Debug("Zeroing settable destination %s due to nil source.", dest.Type())
				}
				dest.Set(reflect.Zero(dest.Type()))
			} else if c.useUnsafe && dest.CanAddr() && (!isUnexportedStructField(dest) || c.copyUnexported) {
				ptr := unsafe.Pointer(dest.UnsafeAddr())
				zeroVal := reflect.Zero(dest.Type())
				if zeroVal.IsValid() {
					if c.logger != nil {
						c.logger.Debug("UNSAFE Zeroing addressable destination %s due to nil source.", dest.Type())
					}
					reflect.NewAt(dest.Type(), ptr).Elem().Set(zeroVal)
					runtime.KeepAlive(dest)
				} else {
					if c.logger != nil {
						c.logger.Debug("Cannot zero non-settable destination %s (Zero value invalid for type), src nil.", dest.Type())
					}
				}
			} else {
				if c.logger != nil {
					c.logger.Debug("Cannot zero non-settable destination %s due to nil source.", dest.Type())
				}
			}
		} else {
			if c.logger != nil {
				c.logger.Debug("Overwrite=false, skipping zeroing for %s due to nil source.", dest.Type())
			}
		}
		return nil
	}

	// --- Cycle Detection ---
	// Defer cycle detection if `dest` is a pointer type that needs special handling,
	// as the special handler might allocate and set `dest`, and cycle detection before that
	// might use a pre-allocation `dest` value incorrectly.
	isDestSpecialPointerType := false
	if dest.Kind() == reflect.Ptr {
		destElemType := dest.Type().Elem()
		if destElemType == reflect.TypeOf(bytes.Buffer{}) || destElemType == reflect.TypeOf(strings.Builder{}) {
			isDestSpecialPointerType = true
		}
	}

	if !isDestSpecialPointerType && c.enableCycleDetect &&
		(originalSrcForInterfaceChecks.Kind() == reflect.Ptr || originalSrcForInterfaceChecks.Kind() == reflect.Map || originalSrcForInterfaceChecks.Kind() == reflect.Slice) &&
		!originalSrcForInterfaceChecks.IsNil() {
		cycleCheckSrc := originalSrcForInterfaceChecks
		var srcPtrForCycle uintptr
		var srcTypeForCycleKey reflect.Type

		switch cycleCheckSrc.Kind() {
		case reflect.Ptr:
			srcPtrForCycle = cycleCheckSrc.Pointer()
			srcTypeForCycleKey = cycleCheckSrc.Type().Elem()
		case reflect.Map, reflect.Slice:
			srcPtrForCycle = cycleCheckSrc.Pointer()
			srcTypeForCycleKey = cycleCheckSrc.Type()
		default:
			srcPtrForCycle = 0
		}

		if srcPtrForCycle != 0 {
			if existingDestVal, visited := ctx.detector.Check(srcPtrForCycle, srcTypeForCycleKey); visited {
				var edvTypeStr, edvKindStr string
				if existingDestVal.IsValid() {
					edvTypeStr = existingDestVal.Type().String()
					edvKindStr = existingDestVal.Kind().String()
				} else {
					edvTypeStr = "<invalid_existing_dest_val>"
					edvKindStr = "<invalid_existing_dest_val_kind>"
				}
				c.logger.Debug("Cycle Check: Visited src Ptr %p (KeyType %s). ExistingDest %s (%s). Current dest %s (%s).",
					unsafe.Pointer(srcPtrForCycle), srcTypeForCycleKey,
					edvTypeStr, edvKindStr,
					dest.Type(), dest.Kind())

				settableDest := dest
				if !dest.CanSet() {
					if c.useUnsafe && dest.CanAddr() && (!isUnexportedStructField(dest) || c.copyUnexported) {
						c.logger.Debug("  Cycle REASSIGN: Current dest %s not settable, attempting unsafe access.", dest.Type())
						if dest.IsValid() {
							tempDest := reflect.NewAt(dest.Type(), unsafe.Pointer(dest.UnsafeAddr())).Elem()
							if tempDest.CanSet() {
								settableDest = tempDest
								c.logger.Debug("  Cycle REASSIGN: Unsafe access to dest %s successful.", settableDest.Type())
							} else {
								c.logger.Debug("  Cycle REASSIGN: Unsafe access to dest %s failed to make it settable.", dest.Type())
							}
						} else {
							c.logger.Debug("  Cycle REASSIGN: Cannot attempt unsafe access on invalid dest.")
						}
					}
				}

				if !settableDest.CanSet() {
					errCycle := fmt.Errorf("cycle detected, current dest %s (%s) not settable for reassignment: %w",
						dest.Type(), dest.Kind(), ErrCycleDetected)
					if c.handlePotentialError(errCycle, ctx) {
						return nil
					}
					return errCycle
				}

				assigned := false
				if existingDestVal.IsValid() {
					if existingDestVal.Type().AssignableTo(settableDest.Type()) {
						settableDest.Set(existingDestVal)
						assigned = true
						c.logger.Debug("  Cycle REASSIGN (Direct Assign): Dest %s (%s) set with existing %s (%s).", settableDest.Type(), settableDest.Kind(), existingDestVal.Type(), existingDestVal.Kind())
					} else if settableDest.Kind() == reflect.Ptr {
						if existingDestVal.Kind() != reflect.Ptr && existingDestVal.CanAddr() && settableDest.Type().Elem() == existingDestVal.Type() {
							settableDest.Set(existingDestVal.Addr())
							assigned = true
							c.logger.Debug("  Cycle REASSIGN (Value->Ptr): Dest Ptr %s set with Addr() of existing Value %s.", settableDest.Type(), existingDestVal.Type())
						} else if existingDestVal.Kind() == reflect.Interface && !existingDestVal.IsNil() {
							elemInInterface := existingDestVal.Elem()
							if elemInInterface.IsValid() {
								if elemInInterface.Type().AssignableTo(settableDest.Type()) {
									settableDest.Set(elemInInterface)
									assigned = true
								} else if elemInInterface.CanAddr() && settableDest.Type().Elem() == elemInInterface.Type() {
									settableDest.Set(elemInInterface.Addr())
									assigned = true
								}
							}
							if assigned {
								c.logger.Debug("  Cycle REASSIGN (Interface Elem->Ptr): Dest Ptr %s set with Elem/Addr of existing Interface %s.", settableDest.Type(), existingDestVal.Type())
							}
						}
					} else if settableDest.Kind() != reflect.Ptr && settableDest.Kind() != reflect.Interface {
						if existingDestVal.Kind() == reflect.Ptr && !existingDestVal.IsNil() && settableDest.Type() == existingDestVal.Type().Elem() {
							settableDest.Set(existingDestVal.Elem())
							assigned = true
							c.logger.Debug("  Cycle REASSIGN (Ptr->Value): Dest %s set with Elem() of existing Ptr %s.", settableDest.Type(), existingDestVal.Type())
						} else if existingDestVal.Kind() == reflect.Interface && !existingDestVal.IsNil() {
							elemInInterface := existingDestVal.Elem()
							if elemInInterface.IsValid() && elemInInterface.Type().AssignableTo(settableDest.Type()) {
								settableDest.Set(elemInInterface)
								assigned = true
								c.logger.Debug("  Cycle REASSIGN (Interface Elem->Value): Dest %s set with Elem of existing Interface %s.", settableDest.Type(), existingDestVal.Type())
							}
						}
					} else if settableDest.Kind() == reflect.Interface {
						if existingDestVal.Type().AssignableTo(settableDest.Type()) {
							settableDest.Set(existingDestVal)
							assigned = true
							c.logger.Debug("  Cycle REASSIGN (ToInterface): Dest Interface %s set with existing %s.", settableDest.Type(), existingDestVal.Type())
						}
					}
				} else {
					c.logger.Debug("  Cycle REASSIGN: existingDestVal from detector is invalid. Cannot assign.")
				}

				if assigned {
					if c.logger != nil {
						c.logger.Debug("  Cycle REASSIGN successful.")
					}
					return nil
				} else {
					errCycle := fmt.Errorf("cycle detected, existing dest %s (%s) not assignable/settable to current dest %s (%s) after checks: %w",
						edvTypeStr, edvKindStr, settableDest.Type(), settableDest.Kind(), ErrCycleDetected)
					if c.handlePotentialError(errCycle, ctx) {
						return nil
					}
					return errCycle
				}
			}
		}
	}

	// --- Custom Copier Interfaces ---
	if dest.CanAddr() && dest.Addr().CanInterface() {
		if copier, ok := dest.Addr().Interface().(CopiableDeprecated); ok {
			if c.logger != nil {
				c.logger.Debug("Destination implements deprecated Copiable, calling Copy...")
			}
			log.Println("[mm info] Using deprecated Copiable interface; consider migrating to new Copiable or MarshalCopier/UnmarshalCopier")
			errDeprecated := copier.Copy(c, originalSrcForInterfaceChecks)
			if c.handlePotentialError(errDeprecated, ctx) {
				return nil
			}
			return errDeprecated
		}
	}
	if originalSrcForInterfaceChecks.CanInterface() {
		if marshaler, ok := originalSrcForInterfaceChecks.Interface().(MarshalCopier); ok {
			if c.logger != nil {
				c.logger.Debug("Source implements MarshalCopier, calling MarshalCopy...")
			}
			ctxCopy := &CopyContext{Settings: struct{ Overwrite, SkipEmpty, SkipZero, SkipNil bool }{c.overwrite, c.skipEmptyValue, c.skipZeroValue, c.skipNilSrc}, Copier: c}
			serialized, errMarshal := marshaler.MarshalCopy(ctxCopy)
			if errMarshal != nil {
				if c.handlePotentialError(errMarshal, ctx) {
					return nil
				}
				return errMarshal
			}
			if c.enableCycleDetect &&
				(originalSrcForInterfaceChecks.Kind() == reflect.Ptr || originalSrcForInterfaceChecks.Kind() == reflect.Map || originalSrcForInterfaceChecks.Kind() == reflect.Slice) &&
				!originalSrcForInterfaceChecks.IsNil() {
				var srcPtrForCycle uintptr
				var srcTypeForCycleKey reflect.Type
				switch originalSrcForInterfaceChecks.Kind() {
				case reflect.Ptr:
					srcPtrForCycle = originalSrcForInterfaceChecks.Pointer()
					srcTypeForCycleKey = originalSrcForInterfaceChecks.Type().Elem()
				case reflect.Map, reflect.Slice:
					srcPtrForCycle = originalSrcForInterfaceChecks.Pointer()
					srcTypeForCycleKey = originalSrcForInterfaceChecks.Type()
				}

				destToTrackForMarshal := dest
				if dest.Kind() == reflect.Ptr {
					destToTrackForMarshal = dest
				} else if dest.CanAddr() && dest.Kind() != reflect.Interface {
					destToTrackForMarshal = dest.Addr()
				}

				if srcPtrForCycle != 0 && destToTrackForMarshal.IsValid() {
					if _, visited := ctx.detector.Check(srcPtrForCycle, srcTypeForCycleKey); !visited {
						ctx.detector.Track(srcPtrForCycle, srcTypeForCycleKey, destToTrackForMarshal)
					}
				}
			}
			return c.copyRecursive(dest, reflect.ValueOf(serialized), ctx)
		}
	}
	if dest.CanAddr() && dest.Addr().CanInterface() {
		if unmarshaler, ok := dest.Addr().Interface().(UnmarshalCopier); ok {
			if c.logger != nil {
				c.logger.Debug("Destination implements UnmarshalCopier, calling UnmarshalCopy...")
			}
			ctxCopy := &CopyContext{Settings: struct{ Overwrite, SkipEmpty, SkipZero, SkipNil bool }{c.overwrite, c.skipEmptyValue, c.skipZeroValue, c.skipNilSrc}, Copier: c}
			errUnmarshal := unmarshaler.UnmarshalCopy(ctxCopy, originalSrcForInterfaceChecks.Interface())
			if c.handlePotentialError(errUnmarshal, ctx) {
				return nil
			}
			return errUnmarshal
		}
	}

	// --- Track Pointers for Cycle Detection (if not done before for special types) ---
	if (isDestSpecialPointerType || true) && // Condition to ensure this runs after special pointer handlers might have set dest
		c.enableCycleDetect &&
		(originalSrcForInterfaceChecks.Kind() == reflect.Ptr || originalSrcForInterfaceChecks.Kind() == reflect.Map || originalSrcForInterfaceChecks.Kind() == reflect.Slice) &&
		!originalSrcForInterfaceChecks.IsNil() {

		var srcPtrForCycle uintptr
		var srcTypeForCycleKey reflect.Type
		switch originalSrcForInterfaceChecks.Kind() {
		case reflect.Ptr:
			srcPtrForCycle = originalSrcForInterfaceChecks.Pointer()
			srcTypeForCycleKey = originalSrcForInterfaceChecks.Type().Elem()
		case reflect.Map, reflect.Slice:
			srcPtrForCycle = originalSrcForInterfaceChecks.Pointer()
			srcTypeForCycleKey = originalSrcForInterfaceChecks.Type()
		}

		destToActuallyTrack := dest
		if dest.Kind() == reflect.Ptr { // If dest is *T, track T -> *T
			// Ensure dest is not nil before trying to track it, esp if it was allocated by a special handler
			if !dest.IsNil() {
				destToActuallyTrack = dest
			} else {
				// If dest is still nil (e.g. special handler decided not to allocate), don't track
				srcPtrForCycle = 0 // effectively skip tracking
			}
		} else if dest.CanAddr() && dest.Kind() != reflect.Interface { // If dest is T, track T -> &T
			destToActuallyTrack = dest.Addr()
		}

		if srcPtrForCycle != 0 && destToActuallyTrack.IsValid() {
			if _, visited := ctx.detector.Check(srcPtrForCycle, srcTypeForCycleKey); !visited {
				var trackedDestTypeStr, trackedDestKindStr, trackedDestPtrStr string
				if destToActuallyTrack.IsValid() {
					trackedDestTypeStr = destToActuallyTrack.Type().String()
					trackedDestKindStr = destToActuallyTrack.Kind().String()
					if (destToActuallyTrack.Kind() == reflect.Ptr || destToActuallyTrack.Kind() == reflect.UnsafePointer) && !destToActuallyTrack.IsNil() {
						trackedDestPtrStr = fmt.Sprintf("%p", unsafe.Pointer(destToActuallyTrack.Pointer()))
					} else if destToActuallyTrack.CanAddr() {
						trackedDestPtrStr = fmt.Sprintf("addr:%p", unsafe.Pointer(destToActuallyTrack.UnsafeAddr()))
					} else {
						trackedDestPtrStr = "<no_ptr_or_addr>"
					}
				} else {
					trackedDestTypeStr = "<invalid_track_dest>"
					trackedDestKindStr = "<invalid_track_dest_kind>"
					trackedDestPtrStr = "<invalid_track_dest_ptr>"
				}
				c.logger.Debug("  Cycle TRACK: Storing srcPtr %p (keyType %s) -> destToTrack %s (%s) ptr/addr %s",
					unsafe.Pointer(srcPtrForCycle), srcTypeForCycleKey,
					trackedDestTypeStr, trackedDestKindStr, trackedDestPtrStr)

				ctx.detector.Track(srcPtrForCycle, srcTypeForCycleKey, destToActuallyTrack)
			}
		}
	}

	// --- Dereference Pointers and Interfaces ---
	destConcrete := dest
	initialDestIsPtr := dest.Kind() == reflect.Ptr

	for destConcrete.Kind() == reflect.Ptr {
		destPtrBeingProcessed := destConcrete
		if destConcrete.IsNil() {
			if destConcrete.CanSet() {
				c.logger.Debug("Initializing nil pointer in dest path: %s", destConcrete.Type())
				newInstance := reflect.New(destConcrete.Type().Elem())
				destConcrete.Set(newInstance)
				destPtrBeingProcessed = newInstance
			} else {
				c.logger.Debug("Cannot set nil pointer in dest path: %s. Proceeding with nil Elem.", destConcrete.Type())
				destConcrete = destConcrete.Elem()
				break
			}
		}
		destConcrete = destPtrBeingProcessed.Elem()
	}

	if !destConcrete.IsValid() && initialDestIsPtr {
		c.logger.Debug("Destination concrete became invalid after dereferencing pointer %s. Cannot copy to it.", dest.Type())
		return nil
	}

	srcConcrete := effectiveSrc
	for srcConcrete.Kind() == reflect.Ptr || srcConcrete.Kind() == reflect.Interface {
		if srcConcrete.IsNil() {
			c.logger.Debug("Source became nil during dereferencing (%s).", srcConcrete.Type())
			if c.skipNilSrc {
				return nil
			}
			if c.overwrite {
				if destConcrete.IsValid() && destConcrete.CanSet() {
					destConcrete.Set(reflect.Zero(destConcrete.Type()))
				} else if dest.CanSet() {
					dest.Set(reflect.Zero(dest.Type()))
				}
			}
			return nil
		}
		srcConcrete = srcConcrete.Elem()
		if !srcConcrete.IsValid() {
			break
		}
	}

	if !srcConcrete.IsValid() {
		c.logger.Debug("Source concrete became invalid after dereferencing %s.", originalSrcForInterfaceChecks.Type())
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite {
			if destConcrete.IsValid() && destConcrete.CanSet() {
				destConcrete.Set(reflect.Zero(destConcrete.Type()))
			} else if dest.CanSet() {
				dest.Set(reflect.Zero(dest.Type()))
			}
		}
		return nil
	}

	if destConcrete.IsValid() && destConcrete.Kind() == reflect.Interface {
		c.logger.Debug("Destination concrete is interface (%s), calling copyInterface with originalSrcForInterfaceChecks (%s)",
			destConcrete.Type(), originalSrcForInterfaceChecks.Type())
		return c.copyInterface(destConcrete, originalSrcForInterfaceChecks, ctx)
	}

	// --- Source Sensitivity (on srcConcrete) ---
	if srcConcrete.IsValid() {
		isActuallySensitive := c.checkIsSensitive(srcConcrete)
		if isActuallySensitive {
			c.logger.Debug("Source concrete type %s is sensitive.", srcConcrete.Type())
			if c.skipSensitive {
				c.logger.Debug("  -> SKIPPING sensitive source concrete type %s due to c.skipSensitive=true.", srcConcrete.Type())
				if c.overwrite && destConcrete.IsValid() && destConcrete.CanSet() {
					destConcrete.Set(reflect.Zero(destConcrete.Type()))
				}
				return nil
			}
			hasMarshalCopierOnOriginal := false
			if originalSrcForInterfaceChecks.IsValid() && originalSrcForInterfaceChecks.CanInterface() &&
				originalSrcForInterfaceChecks.Type().Implements(reflect.TypeOf((*MarshalCopier)(nil)).Elem()) {
				hasMarshalCopierOnOriginal = true
			}
			hasCopiableOnConcrete := srcConcrete.Type().Implements(reflect.TypeOf((*Copiable)(nil)).Elem())

			if !hasMarshalCopierOnOriginal && !hasCopiableOnConcrete {
				errMsg := fmt.Sprintf("sensitive source concrete type %s cannot be copied directly; requires MarshalCopier on original source or Copiable on its own type", srcConcrete.Type())
				c.logger.Warn("  -> ERROR: %s", errMsg)
				errSensitive := fmt.Errorf("%s: %w", errMsg, ErrSensitiveTypeCopied)
				if c.handlePotentialError(errSensitive, ctx) {
					return nil
				}
				return errSensitive
			}
			c.logger.Debug("  Sensitive source concrete type %s has a handler (MarshalCopier or Copiable on concrete) or will be handled by processItem. Proceeding.", srcConcrete.Type())
		}
	}

	if !destConcrete.IsValid() {
		c.logger.Debug("Destination concrete is invalid before kind dispatch. Cannot copy.")
		return nil
	}

	srcConcreteType := srcConcrete.Type()
	destConcreteType := destConcrete.Type()

	// SPECIAL TYPE HANDLING (MOVED BEFORE MAIN DISPATCH)
	// *bytes.Buffer (Pointer to Pointer copy)
	if dest.Type() == reflect.TypeOf(&bytes.Buffer{}) &&
		originalSrcForInterfaceChecks.IsValid() && originalSrcForInterfaceChecks.Type() == reflect.TypeOf(&bytes.Buffer{}) {
		c.logger.Debug("Special Handling for *bytes.Buffer -> *bytes.Buffer")
		if originalSrcForInterfaceChecks.IsNil() {
			if c.skipNilSrc {
				c.logger.Debug("  Src *bytes.Buffer is nil, skipping.")
				return nil
			}
			if c.overwrite && dest.CanSet() && !dest.IsNil() {
				dest.Set(reflect.Zero(dest.Type()))
				c.logger.Debug("  Set dest *bytes.Buffer to nil (source was nil, overwrite=true).")
			}
			return nil
		}
		srcBufferPtr := originalSrcForInterfaceChecks.Interface().(*bytes.Buffer)
		if !c.overwrite && dest.IsValid() && !dest.IsNil() {
			if destBuf, ok := dest.Interface().(*bytes.Buffer); ok && destBuf.Len() > 0 {
				c.logger.Debug("  Skipping *bytes.Buffer (Ptr): Overwrite=false and Dest non-empty.")
				return nil
			}
		}
		if dest.CanSet() {
			newBuf := bytes.NewBuffer(srcBufferPtr.Bytes())
			dest.Set(reflect.ValueOf(newBuf))
			runtime.KeepAlive(newBuf)
			runtime.KeepAlive(dest)
			c.logger.Debug("  Set dest *bytes.Buffer to new buffer with content: '%s'", newBuf.String())
			return nil
		}
		err = fmt.Errorf("cannot set dest pointer for *bytes.Buffer %s", dest.Type())
		if c.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	// *strings.Builder (Pointer to Pointer copy)
	if dest.Type() == reflect.TypeOf(&strings.Builder{}) &&
		originalSrcForInterfaceChecks.IsValid() && originalSrcForInterfaceChecks.Type() == reflect.TypeOf(&strings.Builder{}) {
		c.logger.Debug("Special Handling for *strings.Builder -> *strings.Builder")
		if originalSrcForInterfaceChecks.IsNil() {
			if c.skipNilSrc {
				c.logger.Debug("  Src *strings.Builder is nil, skipping.")
				return nil
			}
			if c.overwrite && dest.CanSet() && !dest.IsNil() {
				dest.Set(reflect.Zero(dest.Type()))
				c.logger.Debug("  Set dest *strings.Builder to nil (source was nil, overwrite=true).")
			}
			return nil
		}
		srcBuilderPtr := originalSrcForInterfaceChecks.Interface().(*strings.Builder)
		if !c.overwrite && dest.IsValid() && !dest.IsNil() {
			if destBuild, ok := dest.Interface().(*strings.Builder); ok && destBuild.Len() > 0 {
				c.logger.Debug("  Skipping *strings.Builder (Ptr): Overwrite=false and Dest non-empty.")
				return nil
			}
		}
		if dest.CanSet() {
			var newBuilderS strings.Builder
			newBuilderS.WriteString(srcBuilderPtr.String())
			dest.Set(reflect.ValueOf(&newBuilderS))
			runtime.KeepAlive(&newBuilderS)
			runtime.KeepAlive(dest)
			c.logger.Debug("  Set dest *strings.Builder to new builder with content: '%s'", newBuilderS.String())
			return nil
		}
		err = fmt.Errorf("cannot set dest pointer for *strings.Builder %s", dest.Type())
		if c.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	// Bytes Buffer (Value) - srcConcrete is bytes.Buffer, destConcrete is bytes.Buffer
	if srcConcrete.IsValid() && destConcrete.IsValid() &&
		srcConcreteType == reflect.TypeOf(bytes.Buffer{}) && destConcreteType == srcConcreteType {
		c.logger.Debug("Handling bytes.Buffer (Value) copy: %s -> %s", srcConcreteType, destConcreteType)

		var srcBufferContent []byte
		if srcConcrete.CanInterface() {
			srcBufVal := srcConcrete.Interface().(bytes.Buffer)
			srcBufferContent = make([]byte, srcBufVal.Len())
			copy(srcBufferContent, srcBufVal.Bytes())
		} else if srcConcrete.CanAddr() && c.useUnsafe {
			srcBufPtr := (*bytes.Buffer)(unsafe.Pointer(srcConcrete.UnsafeAddr()))
			srcBufferContent = make([]byte, srcBufPtr.Len())
			copy(srcBufferContent, srcBufPtr.Bytes())
			runtime.KeepAlive(srcConcrete)
		} else {
			c.logger.Warn("  Cannot reliably get source bytes.Buffer content for value copy. Assuming empty.")
			srcBufferContent = []byte{}
		}
		c.logger.Debug("  Src Buffer Content (for Value copy): %d bytes", len(srcBufferContent))

		destNonEmpty := false
		if destConcrete.IsValid() {
			var destLen int
			if destConcrete.CanAddr() {
				// Since Len() has a pointer receiver, get the address first
				ptrToDestConcrete := destConcrete.Addr()
				if ptrToDestConcrete.CanInterface() { // Check if the pointer can be interfaced
					if bufPtr, ok := ptrToDestConcrete.Interface().(*bytes.Buffer); ok {
						destLen = bufPtr.Len()
					}
				}
			} else {
				c.logger.Debug("   Dest bytes.Buffer (Value) is not addressable, cannot reliably get Len for non-overwrite check.")
			}
			if destLen > 0 {
				destNonEmpty = true
			}
		}

		if !c.overwrite && destNonEmpty {
			c.logger.Debug("  Skipping bytes.Buffer (Value): Overwrite=false and Dest non-empty.")
			return nil
		}

		if destConcrete.CanSet() {
			var newDestBuf bytes.Buffer
			if srcBufferContent != nil {
				newDestBuf.Write(srcBufferContent)
			}
			destConcrete.Set(reflect.ValueOf(newDestBuf))
			c.logger.Debug("  Set dest bytes.Buffer (Value) with new buffer, content length: %d", newDestBuf.Len())
			return nil
		} else if destConcrete.CanAddr() && c.useUnsafe {
			// This path modifies destConcrete in place.
			destBufPtr := (*bytes.Buffer)(unsafe.Pointer(destConcrete.UnsafeAddr()))
			destBufPtr.Reset()
			if srcBufferContent != nil {
				destBufPtr.Write(srcBufferContent)
			}
			runtime.KeepAlive(destConcrete)
			c.logger.Debug("  Modified in-place (unsafe) dest bytes.Buffer (Value). Content length: %d", destBufPtr.Len())
			return nil
		} else {
			if len(srcBufferContent) > 0 {
				err = fmt.Errorf("cannot deep copy non-empty bytes.Buffer value: dest type %s is not modifiable", destConcreteType)
				if c.handlePotentialError(err, ctx) {
					return nil
				}
				return err
			}
			return nil
		}
	}

	// Strings Builder (Value)
	if srcConcrete.IsValid() && destConcrete.IsValid() &&
		srcConcreteType == reflect.TypeOf(strings.Builder{}) && destConcreteType == srcConcreteType {
		c.logger.Debug("Handling strings.Builder (Value) copy: %s -> %s", srcConcreteType, destConcreteType)
		var srcContent string
		// strings.Builder.String() has a VALUE receiver.
		if srcConcrete.CanInterface() {
			builder := srcConcrete.Interface().(strings.Builder) // Get the value
			srcContent = builder.String()                        // Call String() on the value
		} else if srcConcrete.CanAddr() && c.useUnsafe {
			// This case is tricky for value receivers if not CanInterface.
			// For now, assume CanInterface for common cases.
			c.logger.Warn("  Source strings.Builder (Value) is not directly interfaceable. Getting String() content via unsafe address is complex. Assuming empty.")
		} else {
			c.logger.Warn("  Cannot reliably get source strings.Builder content for value copy. Assuming empty.")
		}
		c.logger.Debug("  Src Builder Content (for Value copy): %d chars", len(srcContent))

		destNonEmpty := false
		if destConcrete.IsValid() {
			var destLen int
			// strings.Builder.Len() has a POINTER receiver.
			if destConcrete.CanAddr() {
				ptrToDestConcrete := destConcrete.Addr()
				if ptrToDestConcrete.CanInterface() {
					if sbPtr, ok := ptrToDestConcrete.Interface().(*strings.Builder); ok {
						destLen = sbPtr.Len()
					}
				}
			} else {
				c.logger.Debug("   Dest strings.Builder (Value) is not addressable, cannot reliably get Len for non-overwrite check.")
			}
			if destLen > 0 {
				destNonEmpty = true
			}
		}

		if !c.overwrite && destNonEmpty {
			c.logger.Debug("  Skipping strings.Builder (Value): Overwrite=false and Dest non-empty.")
			return nil
		}

		if destConcrete.CanSet() {
			var newDestBuilder strings.Builder
			newDestBuilder.WriteString(srcContent)
			destConcrete.Set(reflect.ValueOf(newDestBuilder))
			c.logger.Debug("  Set dest strings.Builder (Value) with new builder, content length: %d", newDestBuilder.Len())
			return nil
		} else if destConcrete.CanAddr() && c.useUnsafe {
			destBuilderPtr := (*strings.Builder)(unsafe.Pointer(destConcrete.UnsafeAddr()))
			destBuilderPtr.Reset()
			destBuilderPtr.WriteString(srcContent)
			runtime.KeepAlive(destConcrete)
			c.logger.Debug("  Modified in-place (unsafe) dest strings.Builder (Value). Content length: %d", destBuilderPtr.Len())
			return nil
		} else {
			if len(srcContent) > 0 {
				err = fmt.Errorf("cannot deep copy non-empty strings.Builder value: dest type %s is not modifiable", destConcreteType)
				if c.handlePotentialError(err, ctx) {
					return nil
				}
				return err
			}
			return nil
		}
	}

	dispatchPerformed := false
	if destConcrete.Kind() == reflect.Struct && srcConcrete.Kind() == reflect.Struct {
		// This is the fallback for general structs.
		// We've already handled special struct types (Buffer, Builder) above.
		c.logger.Debug("Dispatching Struct -> Struct directly.")
		ctx.depth++
		err = c.copyStructDriven(destConcrete, srcConcrete, ctx)
		ctx.depth--
		dispatchPerformed = true
	} else if destConcrete.Kind() == reflect.Map && srcConcrete.Kind() == reflect.Map {
		c.logger.Debug("Dispatching Map -> Map directly.")
		ctx.depth++
		err = c.copyMap(destConcrete, srcConcrete, ctx)
		ctx.depth--
		dispatchPerformed = true
	} else if destConcrete.Kind() == reflect.Slice && (srcConcrete.Kind() == reflect.Slice || srcConcrete.Kind() == reflect.Array) {
		c.logger.Debug("Dispatching Slice/Array -> Slice directly.")
		ctx.depth++
		err = c.copySlice(destConcrete, srcConcrete, ctx)
		ctx.depth--
		dispatchPerformed = true
	} else if destConcrete.Kind() == reflect.Array && (srcConcrete.Kind() == reflect.Slice || srcConcrete.Kind() == reflect.Array) {
		c.logger.Debug("Dispatching Slice/Array -> Array directly.")
		ctx.depth++
		err = c.copyArray(destConcrete, srcConcrete, ctx)
		ctx.depth--
		dispatchPerformed = true
	} else if dest.Kind() == reflect.Ptr {
		c.logger.Debug("Dispatching -> Ptr directly (dest: %s, srcConcrete: %s).", dest.Type(), srcConcrete.Type())
		ctx.depth++
		err = c.copyPtr(dest, srcConcrete, ctx)
		ctx.depth--
		dispatchPerformed = true
	}

	if dispatchPerformed {
		if err != nil && c.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	c.logger.Debug("Kind dispatch did not handle %s (%s) -> %s (%s). Trying conversion/assignment.", srcConcreteType, srcConcrete.Kind(), destConcreteType, destConcrete.Kind())
	handledBySpecialTypeOrConversion := false

	// Time
	if srcConcreteType == reflect.TypeOf(time.Time{}) && destConcreteType == srcConcreteType {
		if !c.overwrite && destConcrete.IsValid() && !destConcrete.Interface().(time.Time).IsZero() {
			c.logger.Debug("  Skipping time.Time: Overwrite=false and Dest non-zero.")
			handledBySpecialTypeOrConversion = true
		} else if destConcrete.CanSet() {
			destConcrete.Set(srcConcrete)
			handledBySpecialTypeOrConversion = true
		} else if c.useUnsafe && destConcrete.CanAddr() && (c.copyUnexported || !isUnexportedStructField(destConcrete)) {
			ptr := unsafe.Pointer(destConcrete.UnsafeAddr())
			reflect.NewAt(destConcreteType, ptr).Elem().Set(srcConcrete)
			handledBySpecialTypeOrConversion = true
			runtime.KeepAlive(destConcrete)
		} else {
			err = fmt.Errorf("cannot set time.Time %s", destConcreteType)
		}
		if handledBySpecialTypeOrConversion || err != nil {
			c.logger.Debug("Handled time.Time. Handled=%v, Err: %v", handledBySpecialTypeOrConversion, err)
			if err != nil && c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}
	}

	// Bytes
	if srcConcreteType == reflect.TypeOf([]byte{}) && destConcreteType == srcConcreteType {
		if !c.overwrite && destConcrete.IsValid() && !destConcrete.IsNil() && destConcrete.Len() > 0 {
			c.logger.Debug("  Skipping []byte: Overwrite=false and Dest non-empty.")
			handledBySpecialTypeOrConversion = true
		} else {
			var srcBytes []byte
			if srcConcrete.IsValid() && !srcConcrete.IsNil() {
				srcBytes = srcConcrete.Bytes()
			}
			newSlice := make([]byte, len(srcBytes))
			copy(newSlice, srcBytes)
			if destConcrete.CanSet() {
				destConcrete.Set(reflect.ValueOf(newSlice))
				handledBySpecialTypeOrConversion = true
			} else {
				err = fmt.Errorf("cannot set []byte for type %s", destConcreteType)
			}
		}
		if handledBySpecialTypeOrConversion || err != nil {
			c.logger.Debug("Handled []byte. Handled=%v, Err: %v", handledBySpecialTypeOrConversion, err)
			if err != nil && c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}
	}

	// Stringer/Error to String or Interface{}
	if (destConcrete.Kind() == reflect.String || (destConcrete.Kind() == reflect.Interface && destConcreteType.NumMethod() == 0 && destConcreteType.Name() == "")) &&
		srcConcrete.CanInterface() {
		stringerVal := ""
		isStringerOrError := false
		if stringer, ok := srcConcrete.Interface().(fmt.Stringer); ok {
			stringerVal = stringer.String()
			isStringerOrError = true
			c.logger.Debug("  Source %s is fmt.Stringer.", srcConcreteType)
		} else if errInterface, ok := srcConcrete.Interface().(error); ok {
			stringerVal = errInterface.Error()
			isStringerOrError = true
			c.logger.Debug("  Source %s is error.", srcConcreteType)
		}
		if isStringerOrError {
			shouldSetStringer := c.overwrite ||
				(destConcrete.Kind() == reflect.String && destConcrete.String() == "") ||
				(destConcrete.Kind() == reflect.Interface && (destConcrete.IsNil() || (destConcrete.Elem().IsValid() && isEmptyValue(destConcrete.Elem()))))
			if shouldSetStringer && destConcrete.CanSet() {
				if destConcrete.Kind() == reflect.String {
					destConcrete.SetString(stringerVal)
					handledBySpecialTypeOrConversion = true
				} else if destConcrete.Kind() == reflect.Interface {
					destConcrete.Set(reflect.ValueOf(stringerVal))
					handledBySpecialTypeOrConversion = true
				}
			} else {
				if !destConcrete.CanSet() {
					c.logger.Debug("  Skipping Stringer/Error from %s to non-settable %s.", srcConcreteType, destConcreteType)
				} else {
					c.logger.Debug("  Skipping Stringer/Error from %s to non-empty %s (overwrite=false).", srcConcreteType, destConcreteType)
				}
			}
		}
		if handledBySpecialTypeOrConversion {
			c.logger.Debug("Handled Stringer/Error to String/Interface{}. Set: %v", handledBySpecialTypeOrConversion)
			return nil
		}
	}

	// Attempt explicit type conversion then Go's ConvertibleTo
	if srcConcreteType != destConcreteType && !srcConcreteType.AssignableTo(destConcreteType) {
		converted, conversionSuccessful, errConvert := c.tryConvert(srcConcrete, destConcreteType)
		if errConvert != nil && !errors.Is(errConvert, ErrNoConverter) {
			if c.handlePotentialError(errConvert, ctx) {
				return nil
			}
			return errConvert
		}
		if conversionSuccessful {
			if c.skipZeroValue && converted.IsValid() && converted.IsZero() {
				c.logger.Debug("Skipping assign of converted value (zero).")
				return nil
			}
			if c.skipEmptyValue && isEmptyValue(converted) {
				c.logger.Debug("Skipping assign of converted value (empty).")
				return nil
			}
			shouldSetConverted := c.overwrite || (destConcrete.IsValid() && (isBasicTypeZero(destConcrete) || isEmptyValue(destConcrete))) || !destConcrete.IsValid()
			if shouldSetConverted {
				if destConcrete.CanSet() {
					destConcrete.Set(converted)
					c.logger.Debug("Successfully set converted %s -> %s.", srcConcreteType, destConcreteType)
					return nil
				}
				if c.useUnsafe && destConcrete.CanAddr() && (c.copyUnexported || !isUnexportedStructField(destConcrete)) {
					ptr := unsafe.Pointer(destConcrete.UnsafeAddr())
					reflect.NewAt(destConcreteType, ptr).Elem().Set(converted)
					runtime.KeepAlive(destConcrete)
					c.logger.Debug("Unsafe set converted %s -> %s.", srcConcreteType, destConcreteType)
					return nil
				} else {
					err = fmt.Errorf("converted %s -> %s, but dest not settable", srcConcreteType, destConcreteType)
				}
				if err != nil && c.handlePotentialError(err, ctx) {
					return nil
				}
				return err
			}
			c.logger.Debug("Converted %s -> %s, but dest not overwritten (merge).", srcConcreteType, destConcreteType)
			return nil
		}
	}

	// Direct assignment for simple kinds if types are assignable
	isSimpleKindForDirectAssign := true
	switch srcConcrete.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array, reflect.Interface, reflect.Chan, reflect.Func:
		isSimpleKindForDirectAssign = false
	}
	if isSimpleKindForDirectAssign && srcConcreteType.AssignableTo(destConcreteType) {
		if c.skipZeroValue && srcConcrete.IsZero() {
			c.logger.Debug("Skipping assign of %s (zero).", srcConcreteType)
			return nil
		}
		if c.skipEmptyValue && isEmptyValue(srcConcrete) {
			c.logger.Debug("Skipping assign of %s (empty).", srcConcreteType)
			return nil
		}
		shouldSetSimpleKind := c.overwrite || (destConcrete.IsValid() && (isBasicTypeZero(destConcrete) || isEmptyValue(destConcrete))) || !destConcrete.IsValid()
		if shouldSetSimpleKind {
			if destConcrete.CanSet() {
				destConcrete.Set(srcConcrete)
				return nil
			}
			if c.useUnsafe && destConcrete.CanAddr() && (c.copyUnexported || !isUnexportedStructField(destConcrete)) {
				ptr := unsafe.Pointer(destConcrete.UnsafeAddr())
				reflect.NewAt(destConcreteType, ptr).Elem().Set(srcConcrete)
				runtime.KeepAlive(destConcrete)
				return nil
			} else {
				err = fmt.Errorf("cannot set assignable %s -> %s (not settable)", srcConcreteType, destConcreteType)
			}
			if err != nil && c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}
		c.logger.Debug("Assignable %s -> %s, but dest not overwritten (merge).", srcConcreteType, destConcreteType)
		return nil
	}

	// --- Fallback Kind Dispatch ---
	c.logger.Debug("Fallback dispatching: DestKind: %s (%s), SrcKind: %s (%s)", destConcrete.Kind(), destConcreteType, srcConcrete.Kind(), srcConcreteType)
	ctx.depth++
	defer func() { ctx.depth-- }()

	switch destConcrete.Kind() {
	case reflect.Struct:
		if srcConcrete.Kind() == reflect.Map {
			err = c.copyMapToStruct(destConcrete, srcConcrete, ctx)
		} else {
			err = fmt.Errorf("cannot copy %s (%s) to struct %s: %w", srcConcreteType, srcConcrete.Kind(), destConcreteType, ErrTypeMismatch)
		}
	case reflect.Slice:
		if srcConcrete.Kind() == reflect.Map {
			err = c.copyMapToSlice(destConcrete, srcConcrete, ctx)
		} else {
			err = fmt.Errorf("cannot copy %s (%s) to slice %s: %w", srcConcreteType, srcConcrete.Kind(), destConcreteType, ErrTypeMismatch)
		}
	case reflect.Map:
		if srcConcrete.Kind() == reflect.Struct {
			err = c.copyStructToMap(destConcrete, srcConcrete, ctx)
		} else if srcConcrete.Kind() == reflect.Slice || srcConcrete.Kind() == reflect.Array {
			err = c.copySliceToMap(destConcrete, srcConcrete, ctx)
		} else {
			err = fmt.Errorf("cannot copy %s (%s) to map %s: %w", srcConcreteType, srcConcrete.Kind(), destConcreteType, ErrTypeMismatch)
		}
	case reflect.Chan:
		if srcConcrete.Kind() != reflect.Chan {
			err = fmt.Errorf("cannot copy %s (%s) to chan %s: %w", srcConcreteType, srcConcrete.Kind(), destConcreteType, ErrTypeMismatch)
		} else {
			err = c.copyChan(destConcrete, srcConcrete, ctx)
		}
	default:
		err = fmt.Errorf("unhandled copy combination: src %s (%s) to dest %s (%s): %w",
			srcConcreteType, srcConcrete.Kind(), destConcreteType, destConcrete.Kind(), ErrTypeMismatch)
	}

	if err != nil {
		if c.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}
	return nil
}

func isUnexportedStructField(v reflect.Value) bool {
	return v.IsValid() && !v.CanSet()
}
