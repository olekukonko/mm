// mm.go
package mm

import (
	"fmt"
	"github.com/olekukonko/ll"
	"reflect"
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
		// If srcVal became invalid (e.g., src was a nil interface containing a nil pointer which is then dereferenced by Elem())
		if !srcVal.IsValid() {
			if c.skipNilSrc {
				c.logger.Debug("CopyWith[interface T]: Source became invalid after Elem() and skipNilSrc=true, returning zero T.")
				return zero, nil
			}
			c.logger.Debug("CopyWith[interface T]: Source became invalid after Elem() and skipNilSrc=false, returning ErrSourceIsNil.")
			errToRet := ErrSourceIsNil // Or a more specific error like "source interface holds invalid value"
			return zero, c.finalizeResult(errToRet, &SysContext{errors: make([]error, 0)})
		}

		// Create a new instance of the source's concrete type for the destination
		concreteDestVal := reflect.New(srcVal.Type()).Elem()
		ctx := &SysContext{depth: 0, visited: make(map[visitKey]reflect.Value), errors: make([]error, 0), detector: newCycleDetector()}

		if c.enableCycleDetect && srcVal.Kind() == reflect.Ptr && !srcVal.IsNil() && srcVal.Type() == concreteDestVal.Addr().Type() {
			c.logger.Debug("CopyWith[interface T]: Priming cycle detector for top-level ptr: %p (%s) -> %p (%s)",
				unsafe.Pointer(srcVal.Pointer()), srcVal.Type(),
				unsafe.Pointer(concreteDestVal.Addr().Pointer()), concreteDestVal.Addr().Type())
			ctx.detector.Track(srcVal.Pointer(), srcVal.Type().Elem(), concreteDestVal.Addr())
		}

		err := c.copyRecursive(concreteDestVal, reflect.ValueOf(src), ctx) // Use original src as passed to CopyWith
		finalErr := c.finalizeResult(err, ctx)
		if finalErr != nil {
			return zero, finalErr
		}
		finalDest, ok := concreteDestVal.Interface().(T)
		if !ok {
			// Try to get the string representation of T for a better error message
			// This might be tricky if T is an unexported interface type or complex.
			var typeNameT string
			// For an interface type T, reflect.TypeOf(zeroT) is nil.
			// We cannot directly get its string name easily if it's a generic interface parameter.
			// A placeholder or a more generic message might be best.
			typeNameT = "<target interface type T>" // Fallback if T's type cannot be easily stringified
			return zero, fmt.Errorf("internal error: cannot assert copy result type %s to target interface type %s", concreteDestVal.Type(), typeNameT)
		}
		return finalDest, nil
	}

	// T is a non-interface type
	// src is guaranteed non-nil here.
	srcReflectVal := reflect.ValueOf(src)

	if destReflectType.Kind() == reflect.Struct {
		destPtr := reflect.New(destReflectType) // *T
		destElem := destPtr.Elem()              // T

		ctx := &SysContext{depth: 0, visited: make(map[visitKey]reflect.Value), errors: make([]error, 0), detector: newCycleDetector()}

		if c.enableCycleDetect && srcReflectVal.IsValid() && srcReflectVal.Kind() == reflect.Ptr && !srcReflectVal.IsNil() {
			if srcReflectVal.Type() == destPtr.Type() {
				c.logger.Debug("CopyWith[struct T]: Priming cycle detector for top-level object: %p (%s) -> %p (%s)",
					unsafe.Pointer(srcReflectVal.Pointer()), srcReflectVal.Type(),
					unsafe.Pointer(destPtr.Pointer()), destPtr.Type())
				// Track source pointer, source element type, to destination pointer
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
	} else {
		destVal := reflect.New(destReflectType).Elem()
		ctx := &SysContext{depth: 0, visited: make(map[visitKey]reflect.Value), errors: make([]error, 0), detector: newCycleDetector()}

		if c.enableCycleDetect && destReflectType.Kind() == reflect.Ptr &&
			srcReflectVal.IsValid() && srcReflectVal.Kind() == reflect.Ptr && !srcReflectVal.IsNil() &&
			srcReflectVal.Type() == destReflectType {
			c.logger.Debug("CopyWith[ptr T]: Priming cycle detector for top-level pointer object: %p (%s) -> %p (%s)",
				unsafe.Pointer(srcReflectVal.Pointer()), srcReflectVal.Type(),
				unsafe.Pointer(destVal.Pointer()), destVal.Type())
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
		logger:          logger.Namespace("copier"), // Default to parent's namespace
		maxDepth:        -1,                         // Initialize all fields that NewCopier does
	}
	// Apply defaults from NewCopier more explicitly or ensure NewCopier is the base.
	// For now, apply options on top of a NewCopier() base.
	baseCopier := NewCopier()
	c.logger = baseCopier.logger // Ensure logger is set if not overridden by options
	c.fallbackTagKeys = baseCopier.fallbackTagKeys
	c.typeInfoCache = baseCopier.typeInfoCache
	c.bufferPool = baseCopier.bufferPool
	c.enableCycleDetect = baseCopier.enableCycleDetect
	c.useUnsafe = baseCopier.useUnsafe
	// ... copy other relevant defaults from NewCopier if they are not covered by options

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
		overwrite:         true,  // Default to overwrite
		useUnsafe:         true,  // Default to use unsafe (often faster)
		copyUnexported:    false, // Default to NOT copy unexported (safer)
		enableCycleDetect: true,  // Default to enable cycle detection
		collectErrors:     false, // Default to stop on first error
		panicOnError:      false, // Default to return errors
		silentErrors:      false, // Default to report errors
		skipNilSrc:        false, // Default to process nil source (usually zeroing dest)
		skipZeroValue:     false, // Default to copy zero values
		skipEmptyValue:    false, // Default to copy empty values
		debugMode:         false,
		tagResolution:     true,                                                                          // Added from New's defaults
		bufferPool:        sync.Pool{New: func() interface{} { buf := make([]byte, 1024); return &buf }}, // Keep pool, might be useful later
		logger:            logger.Namespace("copier"),
	}
}

// Option is a functional option for configuring a Copier.

// AddFallbackTagKeys appends to the list of fallback tag keys.
func (c *Copier) AddFallbackTagKeys(keys ...string) *Copier {
	c.fallbackTagKeys = append(c.fallbackTagKeys, keys...)
	c.typeInfoCache = sync.Map{} // Clear cache
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
		// When src is nil and skipNilSrc is false, copyRecursive will handle zeroing the destElem or returning an error.
		// Unlike CopyWith, Copier.Copy's contract implies modifying 'dest', so directly returning ErrSourceIsNil
		// might be unexpected if the user wants 'dest' to be zeroed.
		// The current flow where copyRecursive handles this (by zeroing 'destElem' if 'src' is nil/invalid) is fine.
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

	// Prime cycle detector for Copy(&destStruct, &srcStruct) case
	srcReflectVal := reflect.ValueOf(src)
	if c.enableCycleDetect && srcReflectVal.IsValid() && srcReflectVal.Kind() == reflect.Ptr && !srcReflectVal.IsNil() {
		// If dest is *T and src is *T (same T)
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
		// This panic will occur if Copy returns an error and panicOnError is true
		// No need to explicitly panic here if finalizeResult handles it based on panicOnError.
		// However, if Copy itself could return an error before finalizeResult (e.g. upfront checks),
		// this explicit panic is a safeguard.
		panic(fmt.Sprintf("mm.MustCopy failed: %v", err))
	}
}

// Merge copies data from src into dest, but only if the destination field/element is a zero/empty value.
func (c *Copier) Merge(dest, src interface{}) error {
	tempCopier := *c
	tempCopier.overwrite = false
	tempCopier.merging = true // Explicitly set merging flag
	tempCopier.debugMode = c.debugMode

	c.logger.Debug("Copier.Merge called. Effective copier config for merge -> Overwrite: %v, Debug: %v", tempCopier.overwrite, tempCopier.debugMode)
	return tempCopier.Copy(dest, src)
}

// CopyCat copies data from the first source using standard Copy logic,
// then merges data from subsequent sources using Merge logic (overwrite=false).
func (c *Copier) CopyCat(dest interface{}, sources ...interface{}) error {
	if len(sources) == 0 {
		return nil // Nothing to copy
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
	firstCopier := *c             // Copier for the first source respects original 'c.overwrite'
	mergeCopier := *c             // Copier for subsequent sources
	mergeCopier.overwrite = false // Subsequent sources always merge
	mergeCopier.merging = true
	mergeCopier.debugMode = c.debugMode

	var overallErr error

	c.logger.Debug("CopyCat: Processing first source...")
	// Prime cycle detector for the first source if applicable
	firstSrcReflectVal := reflect.ValueOf(sources[0])
	if c.enableCycleDetect && firstSrcReflectVal.IsValid() && firstSrcReflectVal.Kind() == reflect.Ptr && !firstSrcReflectVal.IsNil() {
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
				return c.finalizeResult(overallErr, ctx) // Use main copier 'c' for finalizeResult settings
			}
		}
	}

	if len(sources) > 1 {
		c.logger.Debug("CopyCat: Processing subsequent sources for merge...")
		for i := 1; i < len(sources); i++ {
			c.logger.Debug("CopyCat: Merging source %d...", i+1)
			// For subsequent sources, we might need a fresh cycle detector context
			// if the sources are completely independent and could have their own cycles
			// that might conflict with the first source's cycle map.
			// However, if they are merging into the SAME dest, the dest pointers tracked are the primary concern.
			// For simplicity, reuse ctx, assuming cycles are primarily about not re-processing parts of 'dest'.
			currentSrcVal := reflect.ValueOf(sources[i])
			err := mergeCopier.copyRecursive(destElem, currentSrcVal, ctx)
			if err != nil {
				if mergeCopier.handlePotentialError(err, ctx) {
					c.logger.Debug("CopyCat: Error collected from source %d, continuing...", i+1)
				} else {
					if overallErr == nil {
						overallErr = err
					} // Keep first non-collected error
					if !mergeCopier.collectErrors {
						c.logger.Debug("CopyCat: Error from source %d, stopping (collectErrors=false).", i+1)
						break // Stop on first error if not collecting
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
		if c.skipNilSrc { // If skipNilSrc is true, Deep(nil) should return nil, nil
			return nil, nil
		}
		// If skipNilSrc is false, Deep(nil) could arguably be an error or return (nil, nil).
		// Let's align with CopyWith: if src is nil and not skipping, it's an error.
		errToRet := ErrSourceIsNil
		return nil, c.finalizeResult(errToRet, &SysContext{errors: make([]error, 0)})
	}
	srcVal := reflect.ValueOf(v)
	// Note: srcVal will be Invalid if v was `interface{}(nil)` but `v == nil` check handles that.
	// If v was e.g. `(*int)(nil)`, srcVal is Ptr and IsNil, but Valid.
	if !srcVal.IsValid() { // Should not be reached if v != nil check is exhaustive
		err := fmt.Errorf("cannot deep copy an invalid source value (was not nil, but reflect.Value is invalid)")
		return nil, c.finalizeResult(err, &SysContext{errors: make([]error, 0)})
	}

	destType := srcVal.Type()
	destVal := reflect.New(destType).Elem()

	ctx := &SysContext{depth: 0, visited: make(map[visitKey]reflect.Value), errors: make([]error, 0), detector: newCycleDetector()}

	// Prime cycle detector if top-level source is a pointer
	if c.enableCycleDetect && srcVal.Kind() == reflect.Ptr && !srcVal.IsNil() {
		// Track src pointer, its element type, to the new destination pointer (which is destVal itself if destType is a Ptr)
		ctx.detector.Track(srcVal.Pointer(), srcVal.Type().Elem(), destVal)
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
	tempCopier.silentErrors = false // Ensure errors are not silenced if panic is expected

	val, err := tempCopier.Deep(v)
	if err != nil {
		panic(fmt.Sprintf("mm.MustDeep failed: %v", err))
	}
	return val
}
