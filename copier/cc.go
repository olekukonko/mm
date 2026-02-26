package copier

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

func (c *Copier) CopyRecursive(dest, src reflect.Value, ctx *SysContext) (err error) {
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
		c.logger.Debugf("COPYRECURSIVE_ENTRY: Dest: %s (%s), Src: %s (%s), Overwrite: %v", dest.Type().String(), dest.Kind(), srcTypeStr, srcKindStr, c.overwrite)
	}

	if c.maxDepth >= 0 && ctx.depth > c.maxDepth {
		if c.logger != nil {
			c.logger.Debugf("Max depth reached (%d), stopping.", c.maxDepth)
		}
		return nil
	}

	if !dest.IsValid() {
		if c.logger != nil {
			c.logger.Debugf("Destination is invalid, skipping.")
		}
		return nil
	}

	originalSrcForInterfaceChecks := src
	effectiveSrc := src

	// --- Source Validity and Nil Checks ---
	if !effectiveSrc.IsValid() {
		if c.logger != nil {
			c.logger.Debugf("Source is invalid.")
		}
		if c.skipNilSrc {
			if c.logger != nil {
				c.logger.Debugf("Skipping copy due to invalid source (skipNilSrc=true).")
			}
			return nil
		}
		if c.overwrite {
			if dest.CanSet() {
				if c.logger != nil {
					c.logger.Debugf("Zeroing settable destination %s due to invalid source.", dest.Type())
				}
				dest.Set(reflect.Zero(dest.Type()))
			} else if c.useUnsafe && dest.CanAddr() && (!isUnexportedStructField(dest) || c.copyUnexported) {
				ptr := unsafe.Pointer(dest.UnsafeAddr())
				zeroVal := reflect.Zero(dest.Type())
				if zeroVal.IsValid() {
					if c.logger != nil {
						c.logger.Debugf("UNSAFE Zeroing addressable destination %s due to invalid source.", dest.Type())
					}
					reflect.NewAt(dest.Type(), ptr).Elem().Set(zeroVal)
					runtime.KeepAlive(dest)
				} else {
					if c.logger != nil {
						c.logger.Debugf("Cannot zero non-settable destination %s (Zero value invalid for type), src invalid.", dest.Type())
					}
				}
			} else {
				if c.logger != nil {
					c.logger.Debugf("Cannot zero non-settable destination %s due to invalid source.", dest.Type())
				}
			}
		} else {
			if c.logger != nil {
				c.logger.Debugf("Overwrite=false, skipping zeroing for %s due to invalid source.", dest.Type())
			}
		}
		return nil
	}

	srcKind := effectiveSrc.Kind()
	isSrcConsideredNil := (srcKind == reflect.Ptr || srcKind == reflect.Interface || srcKind == reflect.Map || srcKind == reflect.Slice || srcKind == reflect.Chan || srcKind == reflect.Func) && effectiveSrc.IsNil()

	if isSrcConsideredNil {
		if c.logger != nil {
			c.logger.Debugf("Source is considered nil (%s).", effectiveSrc.Type())
		}
		if c.skipNilSrc {
			if c.logger != nil {
				c.logger.Debugf("Skipping copy due to nil source (skipNilSrc=true).")
			}
			return nil
		}
		if c.overwrite {
			if dest.CanSet() {
				if c.logger != nil {
					c.logger.Debugf("Zeroing settable destination %s due to nil source.", dest.Type())
				}
				dest.Set(reflect.Zero(dest.Type()))
			} else if c.useUnsafe && dest.CanAddr() && (!isUnexportedStructField(dest) || c.copyUnexported) {
				ptr := unsafe.Pointer(dest.UnsafeAddr())
				zeroVal := reflect.Zero(dest.Type())
				if zeroVal.IsValid() {
					if c.logger != nil {
						c.logger.Debugf("UNSAFE Zeroing addressable destination %s due to nil source.", dest.Type())
					}
					reflect.NewAt(dest.Type(), ptr).Elem().Set(zeroVal)
					runtime.KeepAlive(dest)
				} else {
					if c.logger != nil {
						c.logger.Debugf("Cannot zero non-settable destination %s (Zero value invalid for type), src nil.", dest.Type())
					}
				}
			} else {
				if c.logger != nil {
					c.logger.Debugf("Cannot zero non-settable destination %s due to nil source.", dest.Type())
				}
			}
		} else {
			if c.logger != nil {
				c.logger.Debugf("Overwrite=false, skipping zeroing for %s due to nil source.", dest.Type())
			}
		}
		return nil
	}

	// --- Cycle Detection ---
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
			if existingDestVal, visited := ctx.Detector.Check(srcPtrForCycle, srcTypeForCycleKey); visited {
				var edvTypeStr, edvKindStr string
				if existingDestVal.IsValid() {
					edvTypeStr = existingDestVal.Type().String()
					edvKindStr = existingDestVal.Kind().String()
				} else {
					edvTypeStr = "<invalid_existing_dest_val>"
					edvKindStr = "<invalid_existing_dest_val_kind>"
				}
				c.logger.Debugf("Cycle Check: Visited src Ptr %p (KeyType %s). ExistingDest %s (%s). Current dest %s (%s).",
					unsafe.Pointer(srcPtrForCycle), srcTypeForCycleKey,
					edvTypeStr, edvKindStr,
					dest.Type(), dest.Kind())

				// `dest` is the current argument to CopyRecursive.
				// `existingDestVal` is the value previously tracked for `srcPtrForCycle`.

				// Case 1: Current `dest` is a pointer itself (e.g. *Cyclic field, or top-level CopyWith[*Cyclic])
				// and `existingDestVal` IS that same pointer. This means we are trying to populate the object
				// this pointer refers to, and a cycle points back to this pointer. We must proceed to populate.
				if dest.Kind() == reflect.Ptr && existingDestVal.Kind() == reflect.Ptr && dest.Pointer() == existingDestVal.Pointer() {
					c.logger.Debugf("  Cycle REASSIGN (Self-Ptr): `dest` (%p) is `existingDestVal`. Population of pointed-to struct must proceed.", unsafe.Pointer(dest.Pointer()))
					// No assignment needed here, `assigned` remains false, `isSelfRefFallThrough` logic will handle fall-through.
				} else if dest.Kind() != reflect.Ptr && dest.Kind() != reflect.Interface && // `dest` is a value type (e.g. struct)
					existingDestVal.Kind() == reflect.Ptr && !existingDestVal.IsNil() && dest.Type() == existingDestVal.Type().Elem() && // `existingDestVal` is a pointer to `dest`'s type
					dest.CanAddr() && existingDestVal.Pointer() == dest.UnsafeAddr() { // and `existingDestVal` points to `dest`
					c.logger.Debugf("  Cycle REASSIGN (Ptr->Self-Value): `existingDestVal` (%p) points to `dest` itself (%s @ %p). Population must proceed.",
						unsafe.Pointer(existingDestVal.Pointer()), dest.Type(), unsafe.Pointer(dest.UnsafeAddr()))
					// No assignment, `assigned` remains false.
				} else {
					// Standard reassignment logic if not a self-reference needing population.
					settableDest := dest
					if !dest.CanSet() {
						if c.useUnsafe && dest.CanAddr() && (!isUnexportedStructField(dest) || c.copyUnexported) {
							if dest.IsValid() {
								tempDest := reflect.NewAt(dest.Type(), unsafe.Pointer(dest.UnsafeAddr())).Elem()
								if tempDest.CanSet() {
									settableDest = tempDest
								}
							}
						}
					}

					if !settableDest.CanSet() {
						errCycle := fmt.Errorf("cycle detected, current dest %s (%s) not settable for reassignment (standard path): %w",
							dest.Type(), dest.Kind(), ErrCycleDetected)
						if c.handlePotentialError(errCycle, ctx) {
							return nil
						}
						return errCycle
					}

					if existingDestVal.IsValid() {
						if existingDestVal.Type().AssignableTo(settableDest.Type()) {
							settableDest.Set(existingDestVal)
							c.logger.Debugf("  Cycle REASSIGN (Direct Assign): Dest %s (%s) set with existing %s (%s).", settableDest.Type(), settableDest.Kind(), existingDestVal.Type(), existingDestVal.Kind())
							return nil // Successfully reassigned
						} else if settableDest.Kind() == reflect.Ptr {
							if existingDestVal.Kind() != reflect.Ptr && existingDestVal.CanAddr() && settableDest.Type().Elem() == existingDestVal.Type() {
								settableDest.Set(existingDestVal.Addr())
								c.logger.Debugf("  Cycle REASSIGN (Value->Ptr): Dest Ptr %s set with Addr() of existing Value %s.", settableDest.Type(), existingDestVal.Type())
								return nil
							} else if existingDestVal.Kind() == reflect.Interface && !existingDestVal.IsNil() {
								elemInInterface := existingDestVal.Elem()
								if elemInInterface.IsValid() {
									if elemInInterface.Type().AssignableTo(settableDest.Type()) {
										settableDest.Set(elemInInterface)
										return nil
									} else if elemInInterface.CanAddr() && settableDest.Type().Elem() == elemInInterface.Type() {
										settableDest.Set(elemInInterface.Addr())
										return nil
									}
								}
							}
						} else if settableDest.Kind() != reflect.Ptr && settableDest.Kind() != reflect.Interface { // Value type
							if existingDestVal.Kind() == reflect.Ptr && !existingDestVal.IsNil() && settableDest.Type() == existingDestVal.Type().Elem() {
								settableDest.Set(existingDestVal.Elem()) // Not a self-ref, so assign
								c.logger.Debugf("  Cycle REASSIGN (Ptr->Value, different instance): Dest %s set with Elem() of existing Ptr %s.", settableDest.Type(), existingDestVal.Type())
								return nil
							}
							// Other cases for value type...
						} else if settableDest.Kind() == reflect.Interface {
							if existingDestVal.Type().AssignableTo(settableDest.Type()) {
								settableDest.Set(existingDestVal)
								return nil
							}
						}
					}
					// If no assignment happened, and it wasn't a self-ref fall-through handled above, then it's an error.
					errCycle := fmt.Errorf("cycle detected, existing dest %s (%s) not assignable/settable to current dest %s (%s) after all checks: %w",
						edvTypeStr, edvKindStr, settableDest.Type(), settableDest.Kind(), ErrCycleDetected)
					if c.handlePotentialError(errCycle, ctx) {
						return nil
					}
					return errCycle
				}
				// If we reached here, it means it was one of the self-reference fall-through cases.
				c.logger.Debugf("  Cycle REASSIGN: Fall-through for self-reference, proceeding with population.")
			}
		}
	}

	// --- Custom Copier Interfaces ---
	if dest.CanAddr() && dest.Addr().CanInterface() {
		if copier, ok := dest.Addr().Interface().(CopiableDeprecated); ok {
			if c.logger != nil {
				c.logger.Debugf("Destination implements deprecated Copiable, calling Copy...")
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
				c.logger.Debugf("Source implements MarshalCopier, calling MarshalCopy...")
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
				} else if dest.Kind() != reflect.Interface && dest.CanAddr() {
					destToTrackForMarshal = dest.Addr()
				}

				if srcPtrForCycle != 0 && destToTrackForMarshal.IsValid() {
					if _, visited := ctx.Detector.Check(srcPtrForCycle, srcTypeForCycleKey); !visited {
						ctx.Detector.Track(srcPtrForCycle, srcTypeForCycleKey, destToTrackForMarshal)
					}
				}
			}
			return c.CopyRecursive(dest, reflect.ValueOf(serialized), ctx)
		}
	}
	if dest.CanAddr() && dest.Addr().CanInterface() {
		if unmarshaler, ok := dest.Addr().Interface().(UnmarshalCopier); ok {
			if c.logger != nil {
				c.logger.Debugf("Destination implements UnmarshalCopier, calling UnmarshalCopy...")
			}
			ctxCopy := &CopyContext{Settings: struct{ Overwrite, SkipEmpty, SkipZero, SkipNil bool }{c.overwrite, c.skipEmptyValue, c.skipZeroValue, c.skipNilSrc}, Copier: c}
			errUnmarshal := unmarshaler.UnmarshalCopy(ctxCopy, originalSrcForInterfaceChecks.Interface())
			if c.handlePotentialError(errUnmarshal, ctx) {
				return nil
			}
			return errUnmarshal
		}
	}

	// --- Track Pointers for Cycle Detection (General Case) ---
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

		destToActuallyTrack := dest
		if dest.Kind() != reflect.Ptr && dest.Kind() != reflect.Interface && dest.CanAddr() {
			destToActuallyTrack = dest.Addr()
		}
		if srcPtrForCycle != 0 && destToActuallyTrack.IsValid() &&
			(destToActuallyTrack.Kind() == reflect.Ptr || destToActuallyTrack.Kind() == reflect.Map || destToActuallyTrack.Kind() == reflect.Slice) {
			if _, visited := ctx.Detector.Check(srcPtrForCycle, srcTypeForCycleKey); !visited {
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
				c.logger.Debugf("  Cycle TRACK: Storing srcPtr %p (keyType %s) -> destToTrack %s (%s) ptr/addr %s",
					unsafe.Pointer(srcPtrForCycle), srcTypeForCycleKey,
					trackedDestTypeStr, trackedDestKindStr, trackedDestPtrStr)
				ctx.Detector.Track(srcPtrForCycle, srcTypeForCycleKey, destToActuallyTrack)
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
				c.logger.Debugf("Initializing nil pointer in dest path: %s", destConcrete.Type())
				newInstance := reflect.New(destConcrete.Type().Elem())
				destConcrete.Set(newInstance)
				destPtrBeingProcessed = newInstance
			} else {
				c.logger.Debugf("Cannot set nil pointer in dest path: %s. Proceeding with nil Elem.", destConcrete.Type())
				destConcrete = destConcrete.Elem()
				break
			}
		}
		destConcrete = destPtrBeingProcessed.Elem()
	}

	if !destConcrete.IsValid() && initialDestIsPtr {
		c.logger.Debugf("Destination concrete became invalid after dereferencing pointer %s. Cannot copy to it.", dest.Type())
		return nil
	}

	srcConcrete := effectiveSrc
	for srcConcrete.Kind() == reflect.Ptr || srcConcrete.Kind() == reflect.Interface {
		if srcConcrete.IsNil() {
			c.logger.Debugf("Source became nil during dereferencing (%s).", srcConcrete.Type())
			if c.skipNilSrc {
				return nil
			}
			if c.overwrite {
				targetForZero := destConcrete
				if !targetForZero.IsValid() || !targetForZero.CanSet() {
					targetForZero = dest
				}
				if targetForZero.IsValid() && targetForZero.CanSet() {
					targetForZero.Set(reflect.Zero(targetForZero.Type()))
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
		c.logger.Debugf("Source concrete became invalid after dereferencing %s.", originalSrcForInterfaceChecks.Type())
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite {
			targetForZero := destConcrete
			if !targetForZero.IsValid() || !targetForZero.CanSet() {
				targetForZero = dest
			}
			if targetForZero.IsValid() && targetForZero.CanSet() {
				targetForZero.Set(reflect.Zero(targetForZero.Type()))
			}
		}
		return nil
	}

	if destConcrete.IsValid() && destConcrete.Kind() == reflect.Interface {
		c.logger.Debugf("Destination concrete is interface (%s), calling copyInterface with originalSrcForInterfaceChecks (%s)",
			destConcrete.Type(), originalSrcForInterfaceChecks.Type())
		return c.copyInterface(destConcrete, originalSrcForInterfaceChecks, ctx)
	}

	if srcConcrete.IsValid() {
		isActuallySensitive := c.checkIsSensitive(srcConcrete)
		if isActuallySensitive {
			c.logger.Debugf("Source concrete type %s is sensitive.", srcConcrete.Type())
			if c.skipSensitive {
				c.logger.Debugf("  -> SKIPPING sensitive source concrete type %s due to c.skipSensitive=true.", srcConcrete.Type())
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
				c.logger.Warnf("  -> ERROR: %s", errMsg)
				errSensitive := fmt.Errorf("%s: %w", errMsg, ErrSensitiveTypeCopied)
				if c.handlePotentialError(errSensitive, ctx) {
					return nil
				}
				return errSensitive
			}
			c.logger.Debugf("  Sensitive source concrete type %s has a handler (MarshalCopier or Copiable on concrete) or will be handled by processItem. Proceeding.", srcConcrete.Type())
		}
	}

	if !destConcrete.IsValid() {
		c.logger.Debugf("Destination concrete is invalid before kind dispatch. Cannot copy (original dest type %s).", dest.Type())
		return nil
	}

	srcConcreteType := srcConcrete.Type()
	destConcreteType := destConcrete.Type()

	if dest.Type() == reflect.TypeOf(&bytes.Buffer{}) &&
		originalSrcForInterfaceChecks.IsValid() && originalSrcForInterfaceChecks.Type() == reflect.TypeOf(&bytes.Buffer{}) {
		if originalSrcForInterfaceChecks.IsNil() {
			if c.skipNilSrc {
				return nil
			}
			if c.overwrite && dest.CanSet() && !dest.IsNil() {
				dest.Set(reflect.Zero(dest.Type()))
			}
			return nil
		}
		srcBufferPtr := originalSrcForInterfaceChecks.Interface().(*bytes.Buffer)
		if !c.overwrite && dest.IsValid() && !dest.IsNil() {
			if destBuf, ok := dest.Interface().(*bytes.Buffer); ok && destBuf.Len() > 0 {
				return nil
			}
		}
		if dest.CanSet() {
			newBuf := bytes.NewBuffer(srcBufferPtr.Bytes())
			dest.Set(reflect.ValueOf(newBuf))
			runtime.KeepAlive(newBuf)
			runtime.KeepAlive(dest)
			return nil
		}
		err = fmt.Errorf("cannot set dest pointer for *bytes.Buffer %s", dest.Type())
		if c.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	if dest.Type() == reflect.TypeOf(&strings.Builder{}) &&
		originalSrcForInterfaceChecks.IsValid() && originalSrcForInterfaceChecks.Type() == reflect.TypeOf(&strings.Builder{}) {
		if originalSrcForInterfaceChecks.IsNil() {
			if c.skipNilSrc {
				return nil
			}
			if c.overwrite && dest.CanSet() && !dest.IsNil() {
				dest.Set(reflect.Zero(dest.Type()))
			}
			return nil
		}
		srcBuilderPtr := originalSrcForInterfaceChecks.Interface().(*strings.Builder)
		if !c.overwrite && dest.IsValid() && !dest.IsNil() {
			if destBuild, ok := dest.Interface().(*strings.Builder); ok && destBuild.Len() > 0 {
				return nil
			}
		}
		if dest.CanSet() {
			var newBuilderS strings.Builder
			newBuilderS.WriteString(srcBuilderPtr.String())
			dest.Set(reflect.ValueOf(&newBuilderS))
			runtime.KeepAlive(&newBuilderS)
			runtime.KeepAlive(dest)
			return nil
		}
		err = fmt.Errorf("cannot set dest pointer for *strings.Builder %s", dest.Type())
		if c.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	if srcConcrete.IsValid() && destConcrete.IsValid() &&
		srcConcreteType == reflect.TypeOf(bytes.Buffer{}) && destConcreteType == srcConcreteType {
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
		}
		destNonEmpty := false
		if destConcrete.IsValid() {
			var destLen int
			if destConcrete.CanAddr() {
				if bufPtr, ok := destConcrete.Addr().Interface().(*bytes.Buffer); ok {
					destLen = bufPtr.Len()
				}
			}
			if destLen > 0 {
				destNonEmpty = true
			}
		}
		if !c.overwrite && destNonEmpty {
			return nil
		}
		if destConcrete.CanSet() {
			var newDestBuf bytes.Buffer
			if srcBufferContent != nil {
				newDestBuf.Write(srcBufferContent)
			}
			destConcrete.Set(reflect.ValueOf(newDestBuf))
			return nil
		} else if destConcrete.CanAddr() && c.useUnsafe {
			destBufPtr := (*bytes.Buffer)(unsafe.Pointer(destConcrete.UnsafeAddr()))
			destBufPtr.Reset()
			if srcBufferContent != nil {
				destBufPtr.Write(srcBufferContent)
			}
			runtime.KeepAlive(destConcrete)
			return nil
		} else {
			if len(srcBufferContent) > 0 {
				err = fmt.Errorf("cannot deep copy non-empty bytes.Buffer value: dest type %s not modifiable", destConcreteType)
				if c.handlePotentialError(err, ctx) {
					return nil
				}
				return err
			}
			return nil
		}
	}
	if srcConcrete.IsValid() && destConcrete.IsValid() &&
		srcConcreteType == reflect.TypeOf(strings.Builder{}) && destConcreteType == srcConcreteType {
		var srcContent string
		if srcConcrete.CanInterface() {
			builder := srcConcrete.Interface().(strings.Builder)
			srcContent = builder.String()
		}
		destNonEmpty := false
		if destConcrete.IsValid() {
			var destLen int
			if destConcrete.CanAddr() {
				if sbPtr, ok := destConcrete.Addr().Interface().(*strings.Builder); ok {
					destLen = sbPtr.Len()
				}
			}
			if destLen > 0 {
				destNonEmpty = true
			}
		}
		if !c.overwrite && destNonEmpty {
			return nil
		}
		if destConcrete.CanSet() {
			var newDestBuilder strings.Builder
			newDestBuilder.WriteString(srcContent)
			destConcrete.Set(reflect.ValueOf(newDestBuilder))
			return nil
		} else if destConcrete.CanAddr() && c.useUnsafe {
			destBuilderPtr := (*strings.Builder)(unsafe.Pointer(destConcrete.UnsafeAddr()))
			destBuilderPtr.Reset()
			destBuilderPtr.WriteString(srcContent)
			runtime.KeepAlive(destConcrete)
			return nil
		} else {
			if len(srcContent) > 0 {
				err = fmt.Errorf("cannot deep copy non-empty strings.Builder value: dest type %s not modifiable", destConcreteType)
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
		ctx.depth++
		err = c.copyStructDriven(destConcrete, srcConcrete, ctx)
		ctx.depth--
		dispatchPerformed = true
	} else if destConcrete.Kind() == reflect.Map && srcConcrete.Kind() == reflect.Map {
		ctx.depth++
		err = c.copyMap(destConcrete, srcConcrete, ctx)
		ctx.depth--
		dispatchPerformed = true
	} else if destConcrete.Kind() == reflect.Slice && (srcConcrete.Kind() == reflect.Slice || srcConcrete.Kind() == reflect.Array) {
		ctx.depth++
		err = c.copySlice(destConcrete, srcConcrete, ctx)
		ctx.depth--
		dispatchPerformed = true
	} else if destConcrete.Kind() == reflect.Array && (srcConcrete.Kind() == reflect.Slice || srcConcrete.Kind() == reflect.Array) {
		ctx.depth++
		err = c.copyArray(destConcrete, srcConcrete, ctx)
		ctx.depth--
		dispatchPerformed = true
	} else if dest.Kind() == reflect.Ptr {
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

	handledBySpecialTypeOrConversion := false
	if srcConcreteType == reflect.TypeOf(time.Time{}) && destConcreteType == srcConcreteType {
		if !c.overwrite && destConcrete.IsValid() && !destConcrete.Interface().(time.Time).IsZero() {
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
			if err != nil && c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}
	}

	if srcConcreteType == reflect.TypeOf([]byte{}) && destConcreteType == srcConcreteType {
		if !c.overwrite && destConcrete.IsValid() && !destConcrete.IsNil() && destConcrete.Len() > 0 {
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
			if err != nil && c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}
	}

	if (destConcrete.Kind() == reflect.String || (destConcrete.Kind() == reflect.Interface && destConcreteType.NumMethod() == 0 && destConcreteType.Name() == "")) &&
		srcConcrete.CanInterface() {
		stringerVal := ""
		isStringerOrError := false
		if stringer, ok := srcConcrete.Interface().(fmt.Stringer); ok {
			stringerVal = stringer.String()
			isStringerOrError = true
		} else if errInterface, ok := srcConcrete.Interface().(error); ok {
			stringerVal = errInterface.Error()
			isStringerOrError = true
		}
		if isStringerOrError {
			shouldSetStringer := c.overwrite ||
				(destConcrete.Kind() == reflect.String && destConcrete.String() == "") ||
				(destConcrete.Kind() == reflect.Interface && (destConcrete.IsNil() || (destConcrete.Elem().IsValid() && IsEmptyValue(destConcrete.Elem()))))
			if shouldSetStringer && destConcrete.CanSet() {
				if destConcrete.Kind() == reflect.String {
					destConcrete.SetString(stringerVal)
					handledBySpecialTypeOrConversion = true
				} else if destConcrete.Kind() == reflect.Interface {
					destConcrete.Set(reflect.ValueOf(stringerVal))
					handledBySpecialTypeOrConversion = true
				}
			}
		}
		if handledBySpecialTypeOrConversion {
			return nil
		}
	}

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
				return nil
			}
			if c.skipEmptyValue && IsEmptyValue(converted) {
				return nil
			}
			shouldSetConverted := c.overwrite || (destConcrete.IsValid() && (IsBasicTypeZero(destConcrete) || IsEmptyValue(destConcrete))) || !destConcrete.IsValid()
			if shouldSetConverted {
				if destConcrete.CanSet() {
					destConcrete.Set(converted)
					return nil
				}
				if c.useUnsafe && destConcrete.CanAddr() && (c.copyUnexported || !isUnexportedStructField(destConcrete)) {
					ptr := unsafe.Pointer(destConcrete.UnsafeAddr())
					reflect.NewAt(destConcreteType, ptr).Elem().Set(converted)
					runtime.KeepAlive(destConcrete)
					return nil
				} else {
					err = fmt.Errorf("converted %s -> %s, but dest not settable", srcConcreteType, destConcreteType)
				}
				if err != nil && c.handlePotentialError(err, ctx) {
					return nil
				}
				return err
			}
			return nil
		}
	}

	isSimpleKindForDirectAssign := true
	switch srcConcrete.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array, reflect.Interface, reflect.Chan, reflect.Func:
		isSimpleKindForDirectAssign = false
	}
	if isSimpleKindForDirectAssign && srcConcreteType.AssignableTo(destConcreteType) {
		if c.skipZeroValue && srcConcrete.IsZero() {
			return nil
		}
		if c.skipEmptyValue && IsEmptyValue(srcConcrete) {
			return nil
		}
		shouldSetSimpleKind := c.overwrite || (destConcrete.IsValid() && (IsBasicTypeZero(destConcrete) || IsEmptyValue(destConcrete))) || !destConcrete.IsValid()
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
		return nil
	}

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
