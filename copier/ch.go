package copier

import (
	"fmt"
	"reflect"
)

func (c *Copier) copyChan(dest, src reflect.Value, ctx *SysContext) error {
	destType := dest.Type()
	srcType := src.Type()

	c.logger.Debugf("Copying Channel -> Channel (%s -> %s)", srcType, destType)

	if destType.Kind() != reflect.Chan || srcType.Kind() != reflect.Chan {
		return fmt.Errorf("type mismatch: dest %s and src %s must both be channels", destType, srcType)
	}

	if src.IsNil() {
		c.logger.Debugf("   Source channel is nil.")
		if c.skipNilSrc {
			return nil
		}
		if c.overwrite && dest.CanSet() && dest.IsValid() && !dest.IsNil() {
			c.logger.Debugf("   Zeroing destination channel (setting to nil).")
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	isSensitiveChannelType := false
	if srcType.Implements(reflect.TypeOf((*Sensitivity)(nil)).Elem()) {
		zeroSrcChan := reflect.Zero(srcType)
		if zeroSrcChan.IsValid() && zeroSrcChan.CanInterface() {
			if sensitiveInstance, ok := zeroSrcChan.Interface().(Sensitivity); ok && sensitiveInstance.Sensitive() {
				isSensitiveChannelType = true
			}
		}
	}

	if isSensitiveChannelType && c.skipSensitive {
		c.logger.Debugf("   -> SKIPPING Sensitive src channel type %s (c.skipSensitive=true).", srcType)
		if c.overwrite && dest.CanSet() && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	if isSensitiveChannelType && !srcType.Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) {
		c.logger.Debugf("   -> SKIPPING Sensitive src channel type %s: No Copiable implementation on the channel type.", srcType)
		if c.overwrite && dest.CanSet() && dest.IsValid() && !dest.IsNil() {
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	copiableUsedForChannelItself := false
	if srcType.Implements(reflect.TypeOf((*Copiable)(nil)).Elem()) && src.IsValid() && src.CanInterface() {
		c.logger.Debugf("   Src channel type %s implements Copiable, calling Copy for the channel itself...", srcType)

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
			FieldName: srcType.String(),
			Settings:  struct{ Overwrite, SkipEmpty, SkipZero, SkipNil bool }{c.overwrite, c.skipEmptyValue, c.skipZeroValue, c.skipNilSrc},
			Overwrite: false,
			SetValue: func(value interface{}) error {
				if !dest.CanSet() {
					return fmt.Errorf("cannot set destination channel of type %s (Copiable on channel type)", destType)
				}
				valueVal := reflect.ValueOf(value)
				if !valueVal.IsValid() {
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
			if err := ctxCopyChannel.SetValue(valueFromCopiable); err != nil {
				chanErr := fmt.Errorf("setting Copiable value for channel type %s: %w", srcType, err)
				if c.handlePotentialError(chanErr, ctx) {
					return nil
				}
				return chanErr
			}
			c.logger.Debugf("     Copiable.Copy set value for channel itself.")
		} else if isSensitiveChannelType {
			chanErr := fmt.Errorf("sensitive channel type %s not overwritten by Copiable: %w", srcType, ErrSensitiveTypeCopied)
			if c.handlePotentialError(chanErr, ctx) {
				return nil
			}
			return chanErr
		}

		if copiableUsedForChannelItself {
			c.logger.Debugf("   Copiable handled the channel type %s itself. Skipping element-wise copy.", srcType)
			return nil
		}
		c.logger.Debugf("   Copiable for channel type %s ran but did not set Overwrite. Proceeding with element copy.", srcType)
	}

	if !dest.IsValid() || dest.IsNil() {
		if !dest.CanSet() {
			return fmt.Errorf("cannot set nil channel dest %s", destType)
		}
		dest.Set(reflect.MakeChan(destType, src.Cap()))
		c.logger.Debugf("   Initialized nil channel dest %s with capacity %d, direction %s.", destType, src.Cap(), destType.ChanDir())
	} else if c.overwrite {
		if dest.CanSet() {
			c.logger.Debugf("   Overwriting destination channel. Replacing with new channel matching source capacity/type.")
			newDestChan := reflect.MakeChan(destType, src.Cap())
			dest.Set(newDestChan)
		} else {
			c.logger.Debugf("   Destination channel exists but cannot be set for overwrite. Will attempt to send elements to existing channel.")
		}
	} else {
		c.logger.Debugf("   Merging into existing destination channel %s.", destType)
	}

	if !dest.IsValid() || dest.IsNil() {
		c.logger.Debugf("   Destination channel is nil after setup, cannot copy elements.")
		return nil
	}

	destElemType := destType.Elem()

	elementIndex := 0
	for {
		val, ok := src.TryRecv()
		if !ok {
			c.logger.Debugf("   Source channel empty or closed after receiving %d elements.", elementIndex)
			break
		}

		itemIsSensitive := c.checkIsSensitive(val)
		res := c.processItem(val, elementIndex, destElemType, itemIsSensitive, ctx)

		if res.err != nil {
			return fmt.Errorf("processing channel element %d: %w", elementIndex, res.err)
		}

		if res.shouldSet {
			var valueToSend reflect.Value
			if res.valueToSet.IsValid() {
				valueToSend = res.valueToSet
			} else {
				valueToSend = reflect.Zero(destElemType)
			}

			if !dest.TrySend(valueToSend) {
				c.logger.Debugf("   Failed to send channel element %d (type %s) to destination (full or closed). Stopping element copy.", elementIndex, valueToSend.Type())
				break
			}
			c.logger.Debugf("   Sent channel element %d (type %s) to destination.", elementIndex, valueToSend.Type())
		} else {
			c.logger.Debugf("   Channel element %d not sent by processItem (skipped or Copiable handled non-overwrite).", elementIndex)
		}
		elementIndex++
	}

	c.logger.Debugf("Finished copyChan (%s -> %s)", srcType, destType)
	return nil
}
