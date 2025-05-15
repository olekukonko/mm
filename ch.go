// ch.go
package mm

import (
	"fmt"
	"reflect"
)

// copyChan copies data from a source channel to a destination channel.
func (c *Copier) copyChan(dest, src reflect.Value, ctx *SysContext) error {
	destType := dest.Type()
	srcType := src.Type()

	c.logger.Indent(ctx.depth)
	c.logger.Debug("Copying Channel -> Channel (%s -> %s)", srcType, destType)

	if destType.Kind() != reflect.Chan || srcType.Kind() != reflect.Chan {
		return fmt.Errorf("type mismatch: dest %s and src %s must both be channels", destType, srcType)
	}

	if src.IsNil() {
		c.logger.Debug("   Source channel is nil.")
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite && dest.CanSet() && dest.IsValid() && !dest.IsNil() {
			c.logger.Debug("   Zeroing destination channel (setting to nil).")
			dest.Set(reflect.Zero(destType)) // Makes channel nil
		}
		return nil
	}

	// --- Type-level Sensitivity and Copiable for the channel type itself ---
	// This part remains as it's about the channel container, not individual elements.
	// processItem is not used here.
	isSensitiveChannelType := false
	// Use c.checkIsSensitive for the src channel value itself, if the channel type could be sensitive
	// However, sensitivity for a "channel type" is less common than for its elements.
	// The original logic checked srcType.Implements(), which is for the type definition.
	if srcType.Implements(reflect.TypeOf((*Sensitivity)(nil)).Elem()) {
		// Create a zero value of the channel type to call Sensitive()
		// This is tricky as channel itself doesn't have methods in that sense unless it's a named type.
		// For a `type MyChan chan int` where `MyChan` implements Sensitivity.
		zeroSrcChan := reflect.Zero(srcType)
		if zeroSrcChan.IsValid() && zeroSrcChan.CanInterface() {
			if sensitiveInstance, ok := zeroSrcChan.Interface().(Sensitivity); ok && sensitiveInstance.Sensitive() {
				isSensitiveChannelType = true
			}
		}
	}

	if isSensitiveChannelType && c.skipSensitive {
		c.logger.Debug("   -> SKIPPING Sensitive src channel type %s (c.skipSensitive=true).", srcType)
		if c.overwrite && dest.CanSet() && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	if isSensitiveChannelType && !srcType.Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) {
		c.logger.Debug("   -> SKIPPING Sensitive src channel type %s: No Copiable implementation on the channel type.", srcType)
		if c.debugMode {
			// log.Printf("[mm debug] Sensitive channel type %s skipped due to missing Copiable on type", srcType)
		}
		if c.overwrite && dest.CanSet() && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	copiableUsedForChannelItself := false
	if srcType.Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) && src.IsValid() && src.CanInterface() {
		c.logger.Debug("   Src channel type %s implements Copiable, calling Copy for the channel itself...", srcType)

		// Robust Copiable instantiation for srcType
		var copiableInst Copiable
		receiverForCopiable := reflect.New(srcType)
		var okAssert bool
		if receiverForCopiable.Type().Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) {
			copiableInst, okAssert = receiverForCopiable.Interface().(Copiable)
		}
		if !okAssert && srcType.Kind() != reflect.Ptr && srcType.Kind() != reflect.Interface {
			valReceiver := receiverForCopiable.Elem()
			if valReceiver.Type().Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) && valReceiver.CanInterface() {
				copiableInst, okAssert = valReceiver.Interface().(Copiable)
			}
		}
		if !okAssert {
			err := fmt.Errorf("Copiable (channel type): failed to assert instance of %s to Copiable", srcType)
			if c.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}

		ctxCopyChannel := &CopyContext{
			FieldName: srcType.String(), // Context for the channel type itself
			Settings:  struct{ Overwrite, SkipEmpty, SkipZero, SkipNil bool }{c.overwrite, c.skipEmptyValue, c.skipZeroValue, c.skipNilSrc},
			Overwrite: false,
			SetValue: func(value interface{}) error {
				if !dest.CanSet() {
					return fmt.Errorf("cannot set destination channel of type %s (Copiable on channel type)", destType)
				}
				valueVal := reflect.ValueOf(value)
				if !valueVal.IsValid() { // Handle nil from Copiable
					dest.Set(reflect.Zero(destType))
					return nil
				}
				if valueVal.Type().AssignableTo(destType) {
					dest.Set(valueVal)
					return nil
				}
				if valueVal.Type().ConvertibleTo(destType) {
					dest.Set(valueVal.Convert(destType))
					return nil
				}
				return fmt.Errorf("cannot assign Copiable value of type %s to destination channel of type %s", valueVal.Type(), destType)
			},
			Copier: c,
		}
		valueFromCopiable, err := copiableInst.Copy(ctxCopyChannel, src.Interface())
		if err != nil {
			chanErr := fmt.Errorf("Copiable.Copy for channel type %s failed: %w", srcType, err)
			if c.handlePotentialError(chanErr, ctx) {
				return nil
			}
			return chanErr
		}

		if ctxCopyChannel.Overwrite {
			copiableUsedForChannelItself = true
			if err := ctxCopyChannel.SetValue(valueFromCopiable); err != nil { // SetValue will handle nil valueFromCopiable
				chanErr := fmt.Errorf("setting Copiable value for channel type %s: %w", srcType, err)
				if c.handlePotentialError(chanErr, ctx) {
					return nil
				}
				return chanErr
			}
			c.logger.Debug("     Copiable.Copy set value for channel itself.")
		} else if isSensitiveChannelType {
			chanErr := fmt.Errorf("sensitive channel type %s not overwritten by Copiable: %w", srcType, ErrSensitiveTypeCopied)
			if c.handlePotentialError(chanErr, ctx) {
				return nil
			}
			return chanErr
		}

		if copiableUsedForChannelItself {
			c.logger.Debug("   Copiable handled the channel type %s itself. Skipping element-wise copy.", srcType)
			return nil // Copiable on the channel type itself has full control.
		}
		c.logger.Debug("   Copiable for channel type %s ran but did not set Overwrite. Proceeding with element copy.", srcType)
	}
	// --- End of Type-level Copiable for channel ---

	// Setup destination channel if needed
	if !dest.IsValid() || dest.IsNil() {
		if !dest.CanSet() {
			return fmt.Errorf("cannot set nil channel dest %s", destType)
		}
		// Make new channel with same type, direction, and capacity as source, but using destType
		// src.Cap() is correct. destType.ChanDir() provides the direction for the new channel.
		dest.Set(reflect.MakeChan(destType, src.Cap()))
		c.logger.Debug("   Initialized nil channel dest %s with capacity %d, direction %s.", destType, src.Cap(), destType.ChanDir())
	} else if c.overwrite {
		if dest.CanSet() {
			c.logger.Debug("   Overwriting destination channel. Replacing with new channel matching source capacity/type.")
			newDestChan := reflect.MakeChan(destType, src.Cap())
			dest.Set(newDestChan)
		} else {
			c.logger.Debug("   Destination channel exists but cannot be set for overwrite. Will attempt to send elements to existing channel.")
			// Elements will be sent to the existing dest channel. If it's full, TrySend will fail.
		}
	} else { // Merge mode
		c.logger.Debug("   Merging into existing destination channel %s.", destType)
		// Elements will be sent to the existing dest channel.
	}

	if !dest.IsValid() || dest.IsNil() { // Re-check after potential setup/overwrite
		c.logger.Debug("   Destination channel is nil after setup, cannot copy elements.")
		// This can happen if Copiable on channel type set it to nil and Overwrite was true.
		return nil
	}

	destElemType := destType.Elem()

	// Element processing loop
	elementIndex := 0
	for {
		val, ok := src.TryRecv()
		if !ok {
			c.logger.Debug("   Source channel empty or closed after receiving %d elements.", elementIndex)
			break
		}

		itemName := fmt.Sprintf("chanElem[%d]", elementIndex)
		itemIsSensitive := c.checkIsSensitive(val) // Check sensitivity of the received element

		res := c.processItem(val, itemName, destElemType, itemIsSensitive, ctx)

		if res.err != nil {
			// If processItem returns an error, it means it wasn't collected.
			return fmt.Errorf("processing channel element %d: %w", elementIndex, res.err)
		}

		if res.shouldSet {
			var valueToSend reflect.Value
			if res.valueToSet.IsValid() {
				valueToSend = res.valueToSet
			} else {
				valueToSend = reflect.Zero(destElemType) // Ensure we send a typed zero if valueToSet is invalid
			}

			if !dest.TrySend(valueToSend) {
				c.logger.Debug("   Failed to send channel element %d (type %s) to destination (full or closed). Stopping element copy.", elementIndex, valueToSend.Type())
				break // Stop if send fails
			}
			c.logger.Debug("   Sent channel element %d (type %s) to destination.", elementIndex, valueToSend.Type())
		} else {
			c.logger.Debug("   Channel element %d not sent by processItem (skipped or Copiable handled non-overwrite).", elementIndex)
		}
		elementIndex++
	}

	c.logger.Debug("Finished copyChan (%s -> %s)", srcType, destType)
	return nil
}
