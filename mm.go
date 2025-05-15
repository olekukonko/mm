// mm.go
package mm

import (
	"bytes"
	"fmt"
	"github.com/olekukonko/ll"
	"reflect"
	"strings"
	"sync"
	"unsafe"
)

// Core Structs & Types
type Copier struct {
	tagName           string
	fallbackTagKeys   []string
	fieldMappings     map[string]string
	skipFields        map[string]struct{}
	formatters        map[string]func(interface{}) interface{}
	typeConverters    map[reflect.Type]map[reflect.Type]func(reflect.Value) (reflect.Value, error)
	maxDepth          int
	overwrite         bool
	skipZeroValue     bool
	skipEmptyValue    bool
	skipSensitive     bool
	skipNilSrc        bool
	useUnsafe         bool
	enableCycleDetect bool
	copyUnexported    bool
	collectErrors     bool
	panicOnError      bool
	silentErrors      bool
	typeInfoCache     sync.Map
	bufferPool        sync.Pool
	debugMode         bool
	tagResolution     bool
	merging           bool
	logger            *ll.Logger
}

func init() {
	logger.Disable()
}

// Copy creates a new value of type T and copies data from src into it, using the default Copier.
func Copy[T any](src interface{}) (T, error) {
	return CopyWith[T](defaultCopier, src)
}

// MustCopy is like Copy but panics if an error occurs. Uses the default Copier.
func MustCopy[T any](src interface{}) T {
	// Make a local copy to set panic mode without affecting defaultCopier
	tempPanicCopier := *defaultCopier
	tempPanicCopier.panicOnError = true
	tempPanicCopier.collectErrors = false // Panic immediately

	val, err := CopyWith[T](&tempPanicCopier, src)
	if err != nil {
		// This path should ideally not be reached if CopyWith correctly panics.
		panic(fmt.Sprintf("mm.MustCopy failed: %v", err))
	}
	return val
}

// CopyWith creates a new value of type T and copies data from src into it, using the provided Copier.
func CopyWith[T any](c *Copier, src interface{}) (T, error) {
	var zero T
	destReflectType := reflect.TypeOf(zero) // Get type of T

	if src == nil {
		if c.skipNilSrc {
			c.logger.Debug("CopyWith: Source is nil and skipNilSrc=true, returning zero value for T.")
			return zero, nil
		}
		c.logger.Debug("CopyWith: Source is nil and skipNilSrc=false, returning ErrSourceIsNil.")
		errToRet := ErrSourceIsNil
		// Use finalizeResult to handle panicOnError consistently even for this upfront check
		return zero, c.finalizeResult(errToRet, &SysContext{errors: make([]error, 0)})
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
			if c.skipNilSrc {
				c.logger.Debug("CopyWith[interface T]: Source became invalid after Elem() and skipNilSrc=true, returning zero T.")
				return zero, nil
			}
			c.logger.Debug("CopyWith[interface T]: Source became invalid after Elem() and skipNilSrc=false, returning ErrSourceIsNil.")
			errToRet := ErrSourceIsNil
			return zero, c.finalizeResult(errToRet, &SysContext{errors: make([]error, 0)})
		}

		concreteDestVal := reflect.New(srcVal.Type()).Elem()
		ctx := &SysContext{depth: 0, visited: make(map[visitKey]reflect.Value), errors: make([]error, 0), detector: newCycleDetector()}

		// For interface T, if srcVal is a pointer, we track it to the new concreteDestVal's address
		// This helps if the interface T is expected to hold a pointer that might be part of a cycle.
		if c.enableCycleDetect && srcVal.Kind() == reflect.Ptr && !srcVal.IsNil() {
			// Ensure concreteDestVal is addressable before taking Addr()
			if concreteDestVal.CanAddr() {
				c.logger.Debug("CopyWith[interface T]: Priming cycle detector for top-level ptr source: %p (%s) -> %p (%s)",
					unsafe.Pointer(srcVal.Pointer()), srcVal.Type(),
					unsafe.Pointer(concreteDestVal.Addr().Pointer()), concreteDestVal.Addr().Type())
				ctx.detector.Track(srcVal.Pointer(), srcVal.Type().Elem(), concreteDestVal.Addr())
			} else if concreteDestVal.Kind() == reflect.Ptr { // If concreteDestVal itself is a pointer type (e.g. T is *SomeType)
				c.logger.Debug("CopyWith[interface T]: Priming cycle detector for top-level ptr source (dest is ptr): %p (%s) -> destVal itself %s",
					unsafe.Pointer(srcVal.Pointer()), srcVal.Type(), concreteDestVal.Type())
				ctx.detector.Track(srcVal.Pointer(), srcVal.Type().Elem(), concreteDestVal) // Track to the pointer value directly
			}
		}

		err := c.copyRecursive(concreteDestVal, reflect.ValueOf(src), ctx)
		finalErr := c.finalizeResult(err, ctx)
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

		ctx := &SysContext{depth: 0, visited: make(map[visitKey]reflect.Value), errors: make([]error, 0), detector: newCycleDetector()}

		if c.enableCycleDetect && srcReflectVal.IsValid() && srcReflectVal.Kind() == reflect.Ptr && !srcReflectVal.IsNil() {
			if srcReflectVal.Type() == destPtr.Type() {
				c.logger.Debug("CopyWith[struct T]: Priming cycle detector for top-level object: %p (%s) -> %p (%s)",
					unsafe.Pointer(srcReflectVal.Pointer()), srcReflectVal.Type(),
					unsafe.Pointer(destPtr.Pointer()), destPtr.Type())
				ctx.detector.Track(srcReflectVal.Pointer(), srcReflectVal.Type().Elem(), destPtr)
			}
		}

		err := c.copyRecursive(destElem, srcReflectVal, ctx)
		finalErr := c.finalizeResult(err, ctx)
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
		ctx := &SysContext{depth: 0, visited: make(map[visitKey]reflect.Value), errors: make([]error, 0), detector: newCycleDetector()}

		// Prime cycle detector for top-level pointers, but NOT for special pointer types like *bytes.Buffer
		// which have their own explicit handling that shouldn't be short-circuited by cycle detection at the top level.
		isSpecialPointerType := false
		if destReflectType.Kind() == reflect.Ptr {
			elemType := destReflectType.Elem()
			if elemType == reflect.TypeOf(bytes.Buffer{}) || elemType == reflect.TypeOf(strings.Builder{}) {
				isSpecialPointerType = true
			}
		}

		if c.enableCycleDetect && !isSpecialPointerType &&
			destReflectType.Kind() == reflect.Ptr &&
			srcReflectVal.IsValid() && srcReflectVal.Kind() == reflect.Ptr && !srcReflectVal.IsNil() &&
			srcReflectVal.Type() == destReflectType {
			c.logger.Debug("CopyWith[ptr T, non-special]: Priming cycle detector for top-level pointer object: %p (%s) -> %p (%s)",
				unsafe.Pointer(srcReflectVal.Pointer()), srcReflectVal.Type(),
				unsafe.Pointer(destVal.UnsafeAddr()), destVal.Type()) // Use UnsafeAddr if destVal is a pointer
			ctx.detector.Track(srcReflectVal.Pointer(), destReflectType.Elem(), destVal)
		}

		err := c.copyRecursive(destVal, srcReflectVal, ctx)
		finalErr := c.finalizeResult(err, ctx)
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

// CopyCat copies data from the first source using standard Copy logic,
// then merges data from subsequent sources using Merge logic (overwrite=false).
func CopyCat(dest interface{}, sources ...interface{}) error {
	return defaultCopier.CopyCat(dest, sources...)
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

// New creates a new Copier with the given options.
func New(options ...Option) *Copier {
	c := &Copier{
		overwrite:       true,
		tagName:         "mm",
		fallbackTagKeys: []string{"json"},
		tagResolution:   true,
		fieldMappings:   make(map[string]string),
		formatters:      make(map[string]func(interface{}) interface{}),
		skipFields:      make(map[string]struct{}),
		typeConverters:  make(map[reflect.Type]map[reflect.Type]func(reflect.Value) (reflect.Value, error)),
		logger:          logger.Namespace("copier"),
		maxDepth:        -1,
	}

	baseCopier := NewCopier()
	c.logger = baseCopier.logger
	c.fallbackTagKeys = baseCopier.fallbackTagKeys
	c.typeInfoCache = baseCopier.typeInfoCache
	c.bufferPool = baseCopier.bufferPool
	c.enableCycleDetect = baseCopier.enableCycleDetect
	c.useUnsafe = baseCopier.useUnsafe

	for _, opt := range options {
		opt(c)
	}
	return c
}

// Initialization
func NewCopier() *Copier {
	return &Copier{
		tagName:           "mm",
		fallbackTagKeys:   []string{"json", "msgpack", "hcl", "xml", "yaml", "db"},
		fieldMappings:     make(map[string]string),
		skipFields:        make(map[string]struct{}),
		formatters:        make(map[string]func(interface{}) interface{}),
		typeConverters:    make(map[reflect.Type]map[reflect.Type]func(reflect.Value) (reflect.Value, error)),
		maxDepth:          -1,
		overwrite:         true,
		useUnsafe:         true,
		copyUnexported:    false,
		enableCycleDetect: true,
		collectErrors:     false,
		panicOnError:      false,
		silentErrors:      false,
		skipNilSrc:        false,
		skipZeroValue:     false,
		skipEmptyValue:    false,
		debugMode:         false,
		tagResolution:     true,
		bufferPool:        sync.Pool{New: func() interface{} { buf := make([]byte, 1024); return &buf }},
		logger:            logger.Namespace("copier"),
	}
}

// AddFallbackTagKeys appends to the list of fallback tag keys.
func (c *Copier) AddFallbackTagKeys(keys ...string) *Copier {
	c.fallbackTagKeys = append(c.fallbackTagKeys, keys...)
	c.typeInfoCache = sync.Map{}
	return c
}

// Copy copies data from src into dest. dest must be a non-nil pointer.
func (c *Copier) Copy(dest interface{}, src interface{}) error {
	c.logger.Debug("Copier.Copy started. Overwrite: %v, Debug: %v", c.overwrite, c.debugMode)
	if dest == nil {
		return c.finalizeResult(ErrDestinationIsNil, &SysContext{errors: make([]error, 0)})
	}
	destVal := reflect.ValueOf(dest)

	if destVal.Kind() != reflect.Ptr || destVal.IsNil() {
		return c.finalizeResult(ErrDestNotPointer, &SysContext{errors: make([]error, 0)})
	}

	if src == nil {
		if c.skipNilSrc {
			c.logger.Debug("Copy: Source is nil, skipping (skipNilSrc=true).")
			return nil
		}
		c.logger.Debug("Copy: Source is nil (skipNilSrc=false, overwrite=%v). Proceeding to copyRecursive (will zero dest or error).", c.overwrite)
	} else {
		c.logger.Debug("Copy: Source is non-nil. Type: %T", src)
	}

	destElem := destVal.Elem()
	if !destElem.CanSet() {
		canPotentiallySet := c.useUnsafe && destElem.CanAddr()
		if !canPotentiallySet {
			err := fmt.Errorf("destination element (type %s) is not settable and unsafe cannot help", destElem.Type())
			return c.finalizeResult(err, &SysContext{errors: make([]error, 0)})
		}
		c.logger.Debug("Destination element %s not settable via standard means, relying on unsafe.", destElem.Type())
	}

	ctx := &SysContext{depth: 0, visited: make(map[visitKey]reflect.Value), errors: make([]error, 0), detector: newCycleDetector()}

	srcReflectVal := reflect.ValueOf(src)

	isSpecialPointerType := false
	if destVal.IsValid() && destVal.Kind() == reflect.Ptr && !destVal.IsNil() {
		elemType := destVal.Type().Elem()
		if elemType == reflect.TypeOf(bytes.Buffer{}) || elemType == reflect.TypeOf(strings.Builder{}) {
			isSpecialPointerType = true
		}
	}

	if c.enableCycleDetect && !isSpecialPointerType &&
		srcReflectVal.IsValid() && srcReflectVal.Kind() == reflect.Ptr && !srcReflectVal.IsNil() {
		if destVal.Type() == srcReflectVal.Type() && destVal.Type().Elem().Kind() == reflect.Struct {
			c.logger.Debug("Copier.Copy: Priming cycle detector for top-level object: %p (%s) -> %p (%s)",
				unsafe.Pointer(srcReflectVal.Pointer()), srcReflectVal.Type(),
				unsafe.Pointer(destVal.Pointer()), destVal.Type())
			ctx.detector.Track(srcReflectVal.Pointer(), srcReflectVal.Type().Elem(), destVal)
		}
	}

	err := c.copyRecursive(destElem, srcReflectVal, ctx)

	finalErr := c.finalizeResult(err, ctx)
	if finalErr != nil && c.silentErrors {
		c.logger.Debug("Copy: Error occurred but suppressed due to silentErrors=true: %v", finalErr)
		return nil
	}
	return finalErr
}

// MustCopy is like Copy but panics if an error occurs.
func (c *Copier) MustCopy(dest, src interface{}) {
	tempCopier := *c
	tempCopier.panicOnError = true
	tempCopier.collectErrors = false

	if err := tempCopier.Copy(dest, src); err != nil {
		panic(fmt.Sprintf("mm.MustCopy failed: %v", err))
	}
}

// Merge copies data from src into dest, but only if the destination field/element is a zero/empty value.
func (c *Copier) Merge(dest, src interface{}) error {
	tempCopier := *c
	tempCopier.overwrite = false
	tempCopier.merging = true
	tempCopier.debugMode = c.debugMode

	c.logger.Debug("Copier.Merge called. Effective copier config for merge -> Overwrite: %v, Debug: %v", tempCopier.overwrite, tempCopier.debugMode)
	return tempCopier.Copy(dest, src)
}

// CopyCat copies data from the first source using standard Copy logic,
// then merges data from subsequent sources using Merge logic (overwrite=false).
func (c *Copier) CopyCat(dest interface{}, sources ...interface{}) error {
	if len(sources) == 0 {
		return nil
	}

	if dest == nil {
		return c.finalizeResult(ErrDestinationIsNil, &SysContext{errors: make([]error, 0)})
	}
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.IsNil() {
		return c.finalizeResult(ErrDestNotPointer, &SysContext{errors: make([]error, 0)})
	}
	destElem := destVal.Elem()
	if !destElem.CanSet() {
		canPotentiallySet := c.useUnsafe && destElem.CanAddr()
		if !canPotentiallySet {
			err := fmt.Errorf("destination element (type %s) is not settable and unsafe cannot help", destElem.Type())
			return c.finalizeResult(err, &SysContext{errors: make([]error, 0)})
		}
	}

	ctx := &SysContext{depth: 0, visited: make(map[visitKey]reflect.Value), errors: make([]error, 0), detector: newCycleDetector()}
	firstCopier := *c
	mergeCopier := *c
	mergeCopier.overwrite = false
	mergeCopier.merging = true
	mergeCopier.debugMode = c.debugMode

	var overallErr error

	c.logger.Debug("CopyCat: Processing first source...")
	firstSrcReflectVal := reflect.ValueOf(sources[0])

	isSpecialPointerType := false
	if destVal.IsValid() && destVal.Kind() == reflect.Ptr && !destVal.IsNil() {
		elemType := destVal.Type().Elem()
		if elemType == reflect.TypeOf(bytes.Buffer{}) || elemType == reflect.TypeOf(strings.Builder{}) {
			isSpecialPointerType = true
		}
	}

	if c.enableCycleDetect && !isSpecialPointerType &&
		firstSrcReflectVal.IsValid() && firstSrcReflectVal.Kind() == reflect.Ptr && !firstSrcReflectVal.IsNil() {
		if destVal.Type() == firstSrcReflectVal.Type() && destVal.Type().Elem().Kind() == reflect.Struct {
			ctx.detector.Track(firstSrcReflectVal.Pointer(), firstSrcReflectVal.Type().Elem(), destVal)
		}
	}

	err := firstCopier.copyRecursive(destElem, firstSrcReflectVal, ctx)
	if err != nil {
		if firstCopier.handlePotentialError(err, ctx) {
			c.logger.Debug("CopyCat: Error collected from first source, continuing...")
		} else {
			overallErr = err
			if !firstCopier.collectErrors {
				c.logger.Debug("CopyCat: Error from first source, stopping (collectErrors=false).")
				return c.finalizeResult(overallErr, ctx)
			}
		}
	}

	if len(sources) > 1 {
		c.logger.Debug("CopyCat: Processing subsequent sources for merge...")
		for i := 1; i < len(sources); i++ {
			c.logger.Debug("CopyCat: Merging source %d...", i+1)
			currentSrcVal := reflect.ValueOf(sources[i])
			err := mergeCopier.copyRecursive(destElem, currentSrcVal, ctx)
			if err != nil {
				if mergeCopier.handlePotentialError(err, ctx) {
					c.logger.Debug("CopyCat: Error collected from source %d, continuing...", i+1)
				} else {
					if overallErr == nil {
						overallErr = err
					}
					if !mergeCopier.collectErrors {
						c.logger.Debug("CopyCat: Error from source %d, stopping (collectErrors=false).", i+1)
						break
					}
				}
			}
		}
	}

	finalErr := c.finalizeResult(overallErr, ctx)
	if finalErr != nil && c.silentErrors {
		c.logger.Debug("CopyCat: Error occurred but suppressed due to silentErrors=true: %v", finalErr)
		return nil
	}
	return finalErr
}

// Deep creates a deep copy of the input value v.
// It returns a new value independent of the original.
func (c *Copier) Deep(v interface{}) (interface{}, error) {
	c.logger.Debug("Copier.Deep called for type %T", v)
	if v == nil {
		if c.skipNilSrc {
			return nil, nil
		}
		errToRet := ErrSourceIsNil
		return nil, c.finalizeResult(errToRet, &SysContext{errors: make([]error, 0)})
	}
	srcVal := reflect.ValueOf(v)
	if !srcVal.IsValid() {
		err := fmt.Errorf("cannot deep copy an invalid source value (was not nil, but reflect.Value is invalid)")
		return nil, c.finalizeResult(err, &SysContext{errors: make([]error, 0)})
	}

	destType := srcVal.Type()
	destVal := reflect.New(destType).Elem()

	ctx := &SysContext{depth: 0, visited: make(map[visitKey]reflect.Value), errors: make([]error, 0), detector: newCycleDetector()}

	// For Deep(), avoid pre-tracking the top-level source pointer if it's a special type
	// like *bytes.Buffer, as these have dedicated handlers in copyRecursive that
	// will perform the correct allocation and setting. Pre-tracking can cause the
	// cycle detector to short-circuit to the initially nil destVal.
	isSpecialPointerType := false
	if c.enableCycleDetect && srcVal.Kind() == reflect.Ptr && !srcVal.IsNil() {
		elemType := srcVal.Type().Elem()
		if elemType == reflect.TypeOf(bytes.Buffer{}) || elemType == reflect.TypeOf(strings.Builder{}) {
			isSpecialPointerType = true
		}
		if !isSpecialPointerType {
			// Track src pointer, its element type, to the new destination pointer
			// (which is destVal itself if destType is a Ptr)
			ctx.detector.Track(srcVal.Pointer(), elemType, destVal)
		}
	}

	err := c.copyRecursive(destVal, srcVal, ctx)

	finalErr := c.finalizeResult(err, ctx)
	if finalErr != nil {
		if c.silentErrors {
			c.logger.Debug("Deep: Error occurred but suppressed due to silentErrors=true: %v", finalErr)
			return reflect.Zero(destType).Interface(), nil
		}
		return nil, finalErr
	}
	return destVal.Interface(), nil
}

// MustDeep is like Deep but panics if an error occurs.
func (c *Copier) MustDeep(v interface{}) interface{} {
	tempCopier := *c
	tempCopier.panicOnError = true
	tempCopier.collectErrors = false
	tempCopier.silentErrors = false

	val, err := tempCopier.Deep(v)
	if err != nil {
		panic(fmt.Sprintf("mm.MustDeep failed: %v", err))
	}
	return val
}
