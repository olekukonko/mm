// cy.go
package mm

import (
	"github.com/olekukonko/ll"
	"reflect"
	"unsafe"
	// No longer needs to import a parent logger or the Copier struct for logging
)

type visitKey struct {
	ptr uintptr
	typ reflect.Type
}

type cycleDetector struct {
	visited map[visitKey]reflect.Value
	logger  *ll.Logger // This will be its own namespaced logger
}

// newCycleDetector now creates its own logger internally.
func newCycleDetector() *cycleDetector {
	// Create a namespaced logger specifically for the cycleDetector.
	// It will use the global DefaultLogger's settings for enabled status and level,
	// unless specifically configured otherwise after creation if needed.
	detectorLogger := logger.Namespace("cycle") // Uses your package-level LogWithNamespace

	return &cycleDetector{
		visited: make(map[visitKey]reflect.Value),
		logger:  detectorLogger,
	}
}

// Check checks if a source pointer has been visited.
// If so, it returns the already created destination reflect.Value and true.
func (d *cycleDetector) Check(srcPtr uintptr, srcElemType reflect.Type) (reflect.Value, bool) {
	vk := visitKey{ptr: srcPtr, typ: srcElemType}
	existingDest, ok := d.visited[vk]
	if ok {
		var existingDestPtr uintptr
		if existingDest.IsValid() {
			if (existingDest.Kind() == reflect.Ptr || existingDest.Kind() == reflect.UnsafePointer) && !existingDest.IsNil() {
				existingDestPtr = existingDest.Pointer()
			} else if existingDest.CanAddr() {
				existingDestPtr = existingDest.UnsafeAddr()
			}
		}
		// Use d.logger directly
		d.logger.Debug("Check: FOUND srcPtr %p (ElemType %s) -> existingDest %s (addr/ptr: %p)",
			unsafe.Pointer(srcPtr), srcElemType.String(), existingDest.Type().String(), unsafe.Pointer(existingDestPtr))
	}
	return existingDest, ok
}

// Track records that a source pointer now corresponds to a specific destination value/pointer.
func (d *cycleDetector) Track(srcPtr uintptr, srcElemType reflect.Type, destVal reflect.Value) {
	vk := visitKey{ptr: srcPtr, typ: srcElemType}
	if _, exists := d.visited[vk]; !exists {
		if !destVal.IsValid() {
			d.logger.Warn("Track: Attempted to track an invalid destVal for srcPtr %p (ElemType %s). Skipping track.", unsafe.Pointer(srcPtr), srcElemType.String())
			return
		}
		d.visited[vk] = destVal
		var destValAddr uintptr
		if destVal.IsValid() { // Re-check after ensuring it's not stored if invalid
			if (destVal.Kind() == reflect.Ptr || destVal.Kind() == reflect.UnsafePointer) && !destVal.IsNil() {
				destValAddr = destVal.Pointer()
			} else if destVal.CanAddr() {
				destValAddr = destVal.UnsafeAddr()
			}
		}
		d.logger.Debug("Track: STORED srcPtr %p (ElemType %s) -> destVal %s (addr/ptr: %p)",
			unsafe.Pointer(srcPtr), srcElemType.String(), destVal.Type().String(), unsafe.Pointer(destValAddr))
	} else {
		// Optionally log if trying to overwrite, but for now, first entry wins.
		d.logger.Debug("Track: srcPtr %p (ElemType %s) already tracked. Current destVal %s ignored.",
			unsafe.Pointer(srcPtr), srcElemType.String(), destVal.Type().String())
	}
}
