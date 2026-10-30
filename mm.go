package mm

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"unsafe"

	"github.com/olekukonko/ll"
	"github.com/olekukonko/mm/copier"
	"github.com/olekukonko/mm/cycle"
)

// Default Copier and Package-Level Variables
var logger = ll.New("mm").Suspend()
var defaultCopier = copier.NewCopier(copier.WithLogger(logger))

// newCycleDetector builds a pooled cycle detector. The copier logger is only
// attached in debug mode: wiring a logger makes every Check/Track call evaluate
// its (formatted) debug arguments even when logging is disabled.
func newCycleDetector(c *copier.Copier) *cycle.Detector {
	opts := []cycle.Option{cycle.WithPool(true), cycle.WithMetrics(false)}
	if c.DebugMode() {
		opts = append(opts, cycle.WithLogger(c.Logger()))
	}
	return cycle.New(opts...)
}

// Copy creates a new value of type T and copies data from src into it using the default copier.
// Returns the copied value or an error if the copy operation fails.
func Copy[T any](src interface{}) (T, error) {
	return CopyWith[T](defaultCopier, src)
}

// CopyWith creates a new value of type T and copies data from src into it using the provided copier.
// Handles nil sources based on SkipNilSrc and supports cycle detection for pointers.
// Returns the copied value or an error if the copy operation fails.
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
		return zero, c.FinalizeResult(errToRet, &copier.SysContext{Errors: make([]error, 0)})
	}

	srcReflectVal := reflect.ValueOf(src) // Get reflect.Value of src early

	ctx := &copier.SysContext{
		Detector: newCycleDetector(c),
	}

	// Handle T being an interface type (e.g., any)
	if destReflectType == nil { // T is an interface type
		// srcReflectVal is guaranteed non-nil and valid here
		concreteSrcVal := srcReflectVal
		if srcReflectVal.Kind() == reflect.Interface && !srcReflectVal.IsNil() {
			concreteSrcVal = srcReflectVal.Elem()
		}

		if !concreteSrcVal.IsValid() { // Should not happen if src was non-nil and valid
			if c.SkipNilSrc() {
				return zero, nil
			}
			return zero, c.FinalizeResult(copier.ErrSourceIsNil, ctx)
		}

		// Create a new instance of the source's concrete type
		destInstanceForInterface := reflect.New(concreteSrcVal.Type()).Elem()

		// Prime cycle detector if source is a pointer
		if c.CycleDetect() && concreteSrcVal.Kind() == reflect.Ptr && !concreteSrcVal.IsNil() {
			// The destination for an interface T is the concrete value that will be assigned to T.
			// If this concrete value is a struct, we track its address. If it's already a pointer, we track it directly.
			destToTrack := destInstanceForInterface
			if destInstanceForInterface.Kind() != reflect.Ptr && destInstanceForInterface.CanAddr() {
				destToTrack = destInstanceForInterface.Addr()
			}
			logger.Debugf("CopyWith[interface T]: Priming detector: src %p (%s) -> destTracked %s (%s)",
				unsafe.Pointer(concreteSrcVal.Pointer()), concreteSrcVal.Type(), destToTrack.Type(), destToTrack.Kind())
			ctx.Detector.Track(concreteSrcVal.Pointer(), concreteSrcVal.Type().Elem(), destToTrack)
		}

		err := c.CopyRecursive(destInstanceForInterface, srcReflectVal, ctx) // Pass original srcReflectVal
		finalErr := c.FinalizeResult(err, ctx)
		if finalErr != nil {
			return zero, finalErr
		}
		finalDest, ok := destInstanceForInterface.Interface().(T)
		if !ok {
			var typeNameT = "<target interface type T>"
			return zero, fmt.Errorf("internal error: cannot assert copy result type %s to target interface type %s", destInstanceForInterface.Type(), typeNameT)
		}
		return finalDest, nil
	}

	// T is a non-interface type
	var destForCopyRecursive reflect.Value
	var destPointerForTracking reflect.Value // This is the *pointer* to the top-level dest object

	if destReflectType.Kind() == reflect.Struct {
		destPtr := reflect.New(destReflectType) // *T
		destForCopyRecursive = destPtr.Elem()   // T
		destPointerForTracking = destPtr        // *T
	} else if destReflectType.Kind() == reflect.Ptr {
		// T is *S. We need to create *S.
		// destReflectType.Elem() is S. reflect.New(S) gives *S.
		destForCopyRecursive = reflect.New(destReflectType.Elem()) // *S (this is the value of T)
		destPointerForTracking = destForCopyRecursive              // *S
	} else {
		// T is slice, map, basic type, etc.
		destForCopyRecursive = reflect.New(destReflectType).Elem() // T
		if destForCopyRecursive.CanAddr() {                        // If T is addressable, we can track its address
			destPointerForTracking = destForCopyRecursive.Addr() // *T
		} else {
			// For non-addressable T (e.g. if T was an unexported struct type from another package, though generics make this less likely for T itself)
			// We can't effectively prime tracking for it with a pointer.
			// However, this path is for slice/map/basic, where top-level cycles involving *them* are less common than struct cycles.
			destPointerForTracking = reflect.Value{} // Cannot track
		}
	}

	// Prime cycle detector if source is a pointer and dest is trackable
	if c.CycleDetect() && srcReflectVal.IsValid() && srcReflectVal.Kind() == reflect.Ptr && !srcReflectVal.IsNil() && destPointerForTracking.IsValid() {
		// Ensure we are not tracking for special pointer types that have direct handlers
		isSpecialPointerType := false
		if srcReflectVal.Type().Elem() == reflect.TypeOf(bytes.Buffer{}) ||
			srcReflectVal.Type().Elem() == reflect.TypeOf(strings.Builder{}) {
			isSpecialPointerType = true
		}

		if !isSpecialPointerType {
			// Check if types are compatible for tracking (src's element type vs dest's element type if both are pointers)
			// Or src's element type vs dest type if dest is value (struct) pointed to by destPointerForTracking
			canTrack := false
			if destPointerForTracking.Type().Elem() == srcReflectVal.Type().Elem() { // Both point to same underlying struct type for instance
				canTrack = true
			}

			if canTrack {
				logger.Debugf("CopyWith[T=%s]: Priming detector: src %p (%s) -> destPtrForTracking %p (%s)",
					destReflectType.String(), unsafe.Pointer(srcReflectVal.Pointer()), srcReflectVal.Type(),
					unsafe.Pointer(destPointerForTracking.Pointer()), destPointerForTracking.Type())
				ctx.Detector.Track(srcReflectVal.Pointer(), srcReflectVal.Type().Elem(), destPointerForTracking)
			} else {
				logger.Debugf("CopyWith[T=%s]: Skipping prime detector due to type mismatch for tracking: src.ElemType %s, destPtrForTracking.ElemType %s",
					destReflectType.String(), srcReflectVal.Type().Elem(), destPointerForTracking.Type().Elem())
			}
		} else {
			logger.Debugf("CopyWith[T=%s]: Skipping prime detector for special pointer type %s", destReflectType.String(), srcReflectVal.Type())
		}
	}

	err := c.CopyRecursive(destForCopyRecursive, srcReflectVal, ctx)
	finalErr := c.FinalizeResult(err, ctx)
	if finalErr != nil {
		return zero, finalErr
	}

	finalDest, ok := destForCopyRecursive.Interface().(T)
	if !ok {
		return zero, fmt.Errorf("internal error: cannot assert copy result type %s to target type %s", destForCopyRecursive.Type(), destReflectType)
	}
	return finalDest, nil
}

// MustCopy creates a new value of type T and copies data from src using the default copier, panicking on error.
// Returns the copied value.
func MustCopy[T any](src interface{}) T {
	tempPanicCopier := defaultCopier.Clone()
	tempPanicCopier.WithPanicOnError(true)
	tempPanicCopier.WithCollectErrors(false) // Panic immediately

	val, err := CopyWith[T](tempPanicCopier, src)
	if err != nil {
		// This path should ideally not be reached if CopyWith correctly panics.
		panic(fmt.Sprintf("mm.MustCopy failed: %v", err))
	}
	return val
}

// Deep creates a deep copy of the input value using the default copier.
// Returns the copied value or an error if the copy operation fails.
func Deep(v interface{}) (interface{}, error) {
	return defaultCopier.Deep(v)
}

// MustDeep creates a deep copy of the input value using the default copier, panicking on error.
// Returns the copied value.
func MustDeep(v interface{}) interface{} {
	return defaultCopier.MustDeep(v)
}

// CopyCat copies data from the first source using Copy logic, then merges subsequent sources using Merge logic (overwrite=false).
// Returns an error if the operation fails.
func CopyCat(dest interface{}, sources ...interface{}) error {
	return defaultCopier.CopyCat(dest, sources...)
}

// Merge merges data from src into dest using the default copier.
// Returns an error if the merge operation fails.
func Merge(dest, src interface{}) error {
	return defaultCopier.Merge(dest, src)
}
