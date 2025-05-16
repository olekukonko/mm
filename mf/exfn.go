package mf

import (
	"math"
	"reflect"
	"sort"
	"time"
)

func IsNilOrInvalid(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
}

func IsNilInterface(val any) bool {
	if val == nil {
		return true
	}
	rv := reflect.ValueOf(val)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}

func IsEmptyInterface(pV any, oV reflect.Value) bool {
	if IsNilInterface(pV) {
		return true
	}
	rv := reflect.ValueOf(pV)
	if !rv.IsValid() {
		return true
	}
	switch rv.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return rv.Len() == 0
	case reflect.Bool:
		return !rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return rv.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return rv.Float() == 0
	case reflect.Complex64, reflect.Complex128:
		return rv.Complex() == complex(0, 0)
	case reflect.Ptr, reflect.Interface:
		return rv.IsNil()
	case reflect.Struct:
		if oV.IsValid() && oV.Type() == reflect.TypeOf(time.Time{}) {
			return oV.IsZero()
		}
		return false
	default:
		return false
	}
}

func IsZeroInterface(pV any, oV reflect.Value) bool {
	if !oV.IsValid() {
		return true
	}
	return oV.IsZero()
}

func IsNumericKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return true
	default:
		return false
	}
}

func IsIntegerKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	default:
		return false
	}
}

func IsSignedKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	default:
		return false
	}
}

func IsUnsignedKind(k reflect.Kind) bool {
	switch k {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	default:
		return false
	}
}

func IsFloatKind(k reflect.Kind) bool {
	switch k {
	case reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

func IsComplexKind(k reflect.Kind) bool {
	switch k {
	case reflect.Complex64, reflect.Complex128:
		return true
	default:
		return false
	}
}

func IsSkippedField(field reflect.StructField, tagName string) bool {
	tagValue := field.Tag.Get(tagName)
	return tagValue == "-"
}

func FitsInInt(v int64, t reflect.Type) bool {
	b := t.Bits()
	min, max := int64(0), int64(0)
	switch b {
	case 8:
		min, max = math.MinInt8, math.MaxInt8
	case 16:
		min, max = math.MinInt16, math.MaxInt16
	case 32:
		min, max = math.MinInt32, math.MaxInt32
	case 64:
		if t.Kind() == reflect.Int {
			min, max = math.MinInt, math.MaxInt
		} else {
			return true
		}
	default:
		return false
	}
	return v >= min && v <= max
}

func FitsInUint(v uint64, t reflect.Type) bool {
	b := t.Bits()
	max := uint64(0)
	switch b {
	case 8:
		max = math.MaxUint8
	case 16:
		max = math.MaxUint16
	case 32:
		max = math.MaxUint32
	case 64:
		if t.Kind() == reflect.Uint {
			max = math.MaxUint
		} else {
			return true
		}
	default:
		return false
	}
	return v <= max
}

func FitsInUintToSigned(v uint64, t reflect.Type) bool {
	b := t.Bits()
	max := int64(0)
	switch b {
	case 8:
		max = math.MaxInt8
	case 16:
		max = math.MaxInt16
	case 32:
		max = math.MaxInt32
	case 64:
		if t.Kind() == reflect.Int {
			max = math.MaxInt
		} else {
			max = math.MaxInt64
		}
	default:
		return false
	}
	return v <= uint64(max)
}

func UniqueSorted(s []string) []string {
	if len(s) < 2 {
		return s
	}
	u := make(map[string]struct{}, len(s))
	for _, v := range s {
		u[v] = struct{}{}
	}
	r := make([]string, 0, len(u))
	for k := range u {
		r = append(r, k)
	}
	sort.Strings(r)
	return r
}
