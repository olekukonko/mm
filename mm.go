// mm.go
package mm

import (
	"bytes"
	"fmt"
	"github.com/olekukonko/ll"
	"github.com/olekukonko/mm/copier"
	"github.com/olekukonko/mm/cycle"
	"reflect"
	"strings"
	"unsafe"
)

// Default Copier and Package-Level Functions

var logger = ll.New("mm")
var defaultCopier = copier.NewCopier(copier.WithLogger(logger))

// Copy creates a new value of type T and copies data from src into it, using the default Copier.
func Copy[T any](src interface{}) (T, error) {
	return CopyWith[T](defaultCopier, src)
}

// CopyWith creates a new value of type T and copies data from src into it, using the provided Copier.
func CopyWith[T any](c *copier.Copier, src interface{}) (T, error) {
	var zero T
	destReflectType := reflect.TypeOf(zero) // Get type of T

	if src == nil {
		if c.SkipNilSrc() {
			logger.Debugf("CopyWith: Source is nil and skipNilSrc=true, returning zero value for T.")
			return zero, nil
		}
		logger.Debugf("CopyWith: Source is nil and skipNilSrc=false, returning ErrSourceIsNil.")
		errToRet := copier.ErrSourceIsNil
		// Use FinalizeResult to handle panicOnError consistently even for this upfront check
		return zero, c.FinalizeResult(errToRet, &copier.SysContext{Errors: make([]error, 0)})
	}

	// Handle T being an interface type (e.g., any)
	// src is guaranteed non-nil here
	if destReflectType == nil { // T is an interface type
		srcVal := reflect.ValueOf(src)
		// If src is an interface, get its concrete element. If src is already concrete, use it.
		if srcVal.Kind() == reflect.Interface && !srcVal.IsNil() {
			srcVal = srcVal.Elem()
		}

		if !srcVal.IsValid() {
			if c.SkipNilSrc() {
				logger.Debugf("CopyWith[interface T]: Source became invalid after Elem() and skipNilSrc=true, returning zero T.")
				return zero, nil
			}
			logger.Debugf("CopyWith[interface T]: Source became invalid after Elem() and skipNilSrc=false, returning ErrSourceIsNil.")
			errToRet := copier.ErrSourceIsNil
			return zero, c.FinalizeResult(errToRet, &copier.SysContext{Errors: make([]error, 0)})
		}

		concreteDestVal := reflect.New(srcVal.Type()).Elem()
		ctx := &copier.SysContext{Visited: make(map[cycle.VisitKey]reflect.Value), Errors: make([]error, 0), Detector: cycle.New(cycle.WithLogger(logger))}

		// For interface T, if srcVal is a pointer, we track it to the new concreteDestVal's address
		// This helps if the interface T is expected to hold a pointer that might be part of a cycle.
		if c.CycleDetect() && srcVal.Kind() == reflect.Ptr && !srcVal.IsNil() {
			// Ensure concreteDestVal is addressable before taking Addr()
			if concreteDestVal.CanAddr() {
				logger.Debugf("CopyWith[interface T]: Priming cycle Detector for top-level ptr source: %p (%s) -> %p (%s)",
					unsafe.Pointer(srcVal.Pointer()), srcVal.Type(),
					unsafe.Pointer(concreteDestVal.Addr().Pointer()), concreteDestVal.Addr().Type())
				ctx.Detector.Track(srcVal.Pointer(), srcVal.Type().Elem(), concreteDestVal.Addr())
			} else if concreteDestVal.Kind() == reflect.Ptr { // If concreteDestVal itself is a pointer type (e.g. T is *SomeType)
				logger.Debugf("CopyWith[interface T]: Priming cycle Detector for top-level ptr source (dest is ptr): %p (%s) -> destVal itself %s",
					unsafe.Pointer(srcVal.Pointer()), srcVal.Type(), concreteDestVal.Type())
				ctx.Detector.Track(srcVal.Pointer(), srcVal.Type().Elem(), concreteDestVal) // Track to the pointer value directly
			}
		}

		err := c.CopyRecursive(concreteDestVal, reflect.ValueOf(src), ctx)
		finalErr := c.FinalizeResult(err, ctx)
		if finalErr != nil {
			return zero, finalErr
		}
		finalDest, ok := concreteDestVal.Interface().(T)
		if !ok {
			var typeNameT string
			typeNameT = "<target interface type T>"
			return zero, fmt.Errorf("internal error: cannot assert copy result type %s to target interface type %s", concreteDestVal.Type(), typeNameT)
		}
		return finalDest, nil
	}

	// T is a non-interface type
	srcReflectVal := reflect.ValueOf(src)

	if destReflectType.Kind() == reflect.Struct {
		destPtr := reflect.New(destReflectType)
		destElem := destPtr.Elem()

		ctx := &copier.SysContext{Visited: make(map[cycle.VisitKey]reflect.Value), Errors: make([]error, 0), Detector: cycle.New(cycle.WithLogger(logger))}

		if c.CycleDetect() && srcReflectVal.IsValid() && srcReflectVal.Kind() == reflect.Ptr && !srcReflectVal.IsNil() {
			if srcReflectVal.Type() == destPtr.Type() {
				logger.Debugf("CopyWith[struct T]: Priming cycle Detector for top-level object: %p (%s) -> %p (%s)",
					unsafe.Pointer(srcReflectVal.Pointer()), srcReflectVal.Type(),
					unsafe.Pointer(destPtr.Pointer()), destPtr.Type())
				ctx.Detector.Track(srcReflectVal.Pointer(), srcReflectVal.Type().Elem(), destPtr)
			}
		}

		err := c.CopyRecursive(destElem, srcReflectVal, ctx)
		finalErr := c.FinalizeResult(err, ctx)
		if finalErr != nil {
			return zero, finalErr
		}
		finalDest, ok := destElem.Interface().(T)
		if !ok {
			return zero, fmt.Errorf("internal error: cannot assert copy result struct type %s to target type %s", destElem.Type(), destReflectType)
		}
		return finalDest, nil
	} else { // T is not struct, not interface (e.g. slice, map, ptr, basic)
		destVal := reflect.New(destReflectType).Elem() // Create element of type T
		ctx := &copier.SysContext{Visited: make(map[cycle.VisitKey]reflect.Value), Errors: make([]error, 0), Detector: cycle.New(cycle.WithLogger(logger))}

		// Prime cycle Detector for top-level pointers, but NOT for special pointer types like *bytes.Buffer
		// which have their own explicit handling that shouldn't be short-circuited by cycle detection at the top level.
		isSpecialPointerType := false
		if destReflectType.Kind() == reflect.Ptr {
			elemType := destReflectType.Elem()
			if elemType == reflect.TypeOf(bytes.Buffer{}) || elemType == reflect.TypeOf(strings.Builder{}) {
				isSpecialPointerType = true
			}
		}

		if c.CycleDetect() && !isSpecialPointerType &&
			destReflectType.Kind() == reflect.Ptr &&
			srcReflectVal.IsValid() && srcReflectVal.Kind() == reflect.Ptr && !srcReflectVal.IsNil() &&
			srcReflectVal.Type() == destReflectType {
			logger.Debugf("CopyWith[ptr T, non-special]: Priming cycle Detector for top-level pointer object: %p (%s) -> %p (%s)",
				unsafe.Pointer(srcReflectVal.Pointer()), srcReflectVal.Type(),
				unsafe.Pointer(destVal.UnsafeAddr()), destVal.Type()) // Use UnsafeAddr if destVal is a pointer
			ctx.Detector.Track(srcReflectVal.Pointer(), destReflectType.Elem(), destVal)
		}

		err := c.CopyRecursive(destVal, srcReflectVal, ctx)
		finalErr := c.FinalizeResult(err, ctx)
		if finalErr != nil {
			return zero, finalErr
		}
		finalDest, ok := destVal.Interface().(T)
		if !ok {
			return zero, fmt.Errorf("internal error: cannot assert copy result (non-struct) type %s to target type %s", destVal.Type(), destReflectType)
		}
		return finalDest, nil
	}
}

// MustCopy is like Copy but panics if an error occurs. Uses the default Copier.
func MustCopy[T any](src interface{}) T {
	// Make a local copy to set panic mode without affecting defaultCopier
	tempPanicCopier := *defaultCopier
	tempPanicCopier.WithPanicOnError(true)
	tempPanicCopier.WithCollectErrors(false) // Panic immediately

	val, err := CopyWith[T](&tempPanicCopier, src)
	if err != nil {
		// This path should ideally not be reached if CopyWith correctly panics.
		panic(fmt.Sprintf("mm.MustCopy failed: %v", err))
	}
	return val
}

// Deep creates a deep copy of the input value, returning a new independent value.
// Uses the default copier.
func Deep(v interface{}) (interface{}, error) {
	return defaultCopier.Deep(v)
}

// MustDeep is like Deep but panics if an error occurs. Uses the default copier.
func MustDeep(v interface{}) interface{} {
	return defaultCopier.MustDeep(v)
}

// CopyCat copies data from the first source using standard Copy logic,
// then merges data from subsequent sources using Merge logic (overwrite=false).
func CopyCat(dest interface{}, sources ...interface{}) error {
	return defaultCopier.CopyCat(dest, sources...)
}

func Merge(dest, src interface{}) error {
	return defaultCopier.Merge(dest, src)
}
