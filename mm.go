package mm

import (
	"errors"
	"fmt"
	"log"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"unsafe"
)

// --- Error Definitions ---

var (
	ErrSourceIsNil         = errors.New("source cannot be nil")
	ErrDestinationIsNil    = errors.New("destination cannot be nil")
	ErrDestNotPointer      = errors.New("destination must be a pointer")
	ErrTypeMismatch        = errors.New("type mismatch")
	ErrUnexportedField     = errors.New("cannot set unexported field")
	ErrNoConverter         = errors.New("no suitable type converter found")
	ErrCycleDetected       = errors.New("cycle detected but cannot assign destination")
	ErrUnmappedFieldType   = errors.New("field tagged with 'unmapped' must be a map[string]interface{} or map[string]any")
	ErrMapKeyNotString     = errors.New("source/destination map key type must be string when copying between map and struct")
	ErrDestNotMap          = errors.New("destination type must be a map when copying from struct")
	ErrMapValueNotSettable = errors.New("destination map value type not settable")
	ErrReadUnexportedSrc   = errors.New("reading unexported source field is not supported reliably") // Keep this error, although we'll avoid reading them for now
)

// --- Core Structs & Types ---

type SmartCopier struct {
	tagName           string
	fieldMappings     map[string]string
	skipFields        map[string]struct{}
	formatters        map[string]func(interface{}) interface{}
	typeConverters    map[reflect.Type]map[reflect.Type]func(reflect.Value) (reflect.Value, error)
	maxDepth          int
	overwrite         bool // If true, destination fields are always overwritten by source fields (unless skip options apply)
	skipZeroValue     bool // If true, skip copying source fields that are zero value (0, "", false, nil, etc.)
	skipEmptyValue    bool // If true, skip copying source fields that are empty (zero, "", false, nil, empty slice/map)
	skipNilSrc        bool // If true, skip copying if the source *value itself* is nil (top-level or field value)
	useUnsafe         bool
	enableCycleDetect bool
	copyUnexported    bool // If true (and useUnsafe is true), allows copying unexported fields
	collectErrors     bool
	panicOnError      bool

	typeInfoCache sync.Map
	bufferPool    sync.Pool
	debugMode     bool
}

type visitKey struct {
	ptr uintptr
	typ reflect.Type
}

type copyContext struct {
	depth   int
	visited map[visitKey]reflect.Value // Stores *address* of destination value created for a source pointer/address
	errors  []error
}

type typeInfo struct {
	fields        map[string]fieldInfo
	unmappedField *fieldInfo
	size          uintptr
	canMemCopy    bool // Indicates if the type *structure* is suitable for memcopy (no pointers, etc.)
	alignment     uintptr
	containsPtrs  bool
	isStruct      bool
	isSlice       bool
	isMap         bool
	isPtr         bool
	isInterface   bool
}

type fieldInfo struct {
	name        string
	structField reflect.StructField
	index       []int
	offset      uintptr
	typ         reflect.Type
	canSet      bool // If the field is exported or unsafe+unexported enabled
	tagOpts     map[string]string
}

// --- Tag Parsing & Helpers ---

func parseTagOptions(tag string) map[string]string {
	opts := make(map[string]string)
	parts := strings.Split(tag, ",")
	// Handle the name part first (can be empty)
	if len(parts) > 0 {
		trimmedName := strings.TrimSpace(parts[0])
		if trimmedName == "-" {
			opts["-"] = "" // Mark as ignored
		} else if trimmedName != "" {
			opts["name"] = trimmedName
		}
		// Process options after the first comma
		for _, opt := range parts[1:] {
			trimmedOpt := strings.TrimSpace(opt)
			if trimmedOpt != "" {
				// Handle potential key=value options later if needed
				opts[trimmedOpt] = ""
			}
		}
	}
	// Ensure '-' overrides 'name' if both somehow appear (e.g., `mm:"-,omitempty"`)
	if _, ok := opts["-"]; ok {
		delete(opts, "name")
	}
	return opts
}

func isEmptyValue(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr, reflect.Chan, reflect.Func:
		return v.IsNil()
	}
	// Consider struct zero value? reflect.Value.IsZero() handles this.
	return v.IsZero() // Use IsZero for struct and other cases
}

// --- Initialization ---

func New() *SmartCopier {
	return &SmartCopier{
		tagName:           "mm",
		fieldMappings:     make(map[string]string),
		skipFields:        make(map[string]struct{}),
		formatters:        make(map[string]func(interface{}) interface{}),
		typeConverters:    make(map[reflect.Type]map[reflect.Type]func(reflect.Value) (reflect.Value, error)),
		maxDepth:          -1,
		overwrite:         true, // Default: overwrite destination fields
		useUnsafe:         true, // Default: use unsafe where possible/configured
		enableCycleDetect: true,
		copyUnexported:    false, // Default: do not copy unexported
		collectErrors:     false,
		panicOnError:      false,
		skipNilSrc:        false, // Default: nil source zeros destination if overwrite=true
		skipZeroValue:     false, // Default: copy zero values
		skipEmptyValue:    false, // Default: copy empty values
		bufferPool: sync.Pool{
			New: func() interface{} {
				// Buffer size for potential memcopy, adjust as needed
				return make([]byte, 1024) // Reduced default size
			},
		},
	}
}

// --- Configuration Methods ---

func (sc *SmartCopier) WithTag(tag string) *SmartCopier {
	sc.tagName = tag
	sc.typeInfoCache = sync.Map{} // Clear cache as tags affect type info
	return sc
}

func (sc *SmartCopier) WithFieldMapping(src, dst string) *SmartCopier {
	sc.fieldMappings[src] = dst
	return sc
}

func (sc *SmartCopier) WithSkip(fields ...string) *SmartCopier {
	for _, f := range fields {
		sc.skipFields[f] = struct{}{}
	}
	return sc
}

func (sc *SmartCopier) WithFormatter(field string, fn func(interface{}) interface{}) *SmartCopier {
	sc.formatters[field] = fn
	return sc
}

func (sc *SmartCopier) WithMaxDepth(depth int) *SmartCopier {
	sc.maxDepth = depth
	return sc
}

func (sc *SmartCopier) WithOverwrite(overwrite bool) *SmartCopier {
	sc.overwrite = overwrite
	return sc
}

func (sc *SmartCopier) WithSkipZero(skip bool) *SmartCopier {
	sc.skipZeroValue = skip
	return sc
}

func (sc *SmartCopier) WithSkipEmpty(skip bool) *SmartCopier {
	sc.skipEmptyValue = skip
	return sc
}

func (sc *SmartCopier) WithSkipNil(skip bool) *SmartCopier {
	sc.skipNilSrc = skip
	return sc
}

func (sc *SmartCopier) WithUnsafe(enable bool) *SmartCopier {
	sc.useUnsafe = enable
	sc.typeInfoCache = sync.Map{} // Clear cache as unsafe affects field access
	return sc
}

func (sc *SmartCopier) WithCycleDetection(enable bool) *SmartCopier {
	sc.enableCycleDetect = enable
	return sc
}

func (sc *SmartCopier) WithUnexported(enable bool) *SmartCopier {
	sc.copyUnexported = enable
	sc.typeInfoCache = sync.Map{} // Clear cache as unexported affects field access
	return sc
}

func (sc *SmartCopier) WithTypeConverter(src, dst reflect.Type, converter func(reflect.Value) (reflect.Value, error)) *SmartCopier {
	if sc.typeConverters[src] == nil {
		sc.typeConverters[src] = make(map[reflect.Type]func(reflect.Value) (reflect.Value, error))
	}
	sc.typeConverters[src][dst] = converter
	return sc
}

func (sc *SmartCopier) WithErrors(collect bool) *SmartCopier {
	sc.collectErrors = collect
	return sc
}

func (sc *SmartCopier) WithPanic(panicOnError bool) *SmartCopier {
	sc.panicOnError = panicOnError
	return sc
}

func (sc *SmartCopier) EnableDebug(enable bool) *SmartCopier {
	sc.debugMode = enable
	return sc
}

// --- Public Copy Methods ---

func (sc *SmartCopier) handlePotentialError(err error, ctx *copyContext) bool {
	if err == nil {
		return false
	}
	if sc.collectErrors {
		sc.debug("Collecting error: %v", err)
		ctx.errors = append(ctx.errors, err)
		return true // Indicate error was handled (collected)
	}
	// If not collecting, the error will be returned or panic
	return false // Indicate error needs propagation
}

func (sc *SmartCopier) finalizeResult(returnedErr error, ctx *copyContext) error {
	if sc.collectErrors && len(ctx.errors) > 0 {
		finalErr := errors.Join(ctx.errors...)
		if sc.panicOnError {
			panic(fmt.Sprintf("mm copy failed with %d error(s): %v", len(ctx.errors), finalErr))
		}
		return finalErr
	}
	// Handle non-collected error
	if returnedErr != nil {
		if sc.panicOnError {
			panic(fmt.Sprintf("mm copy failed: %v", returnedErr))
		}
		return returnedErr
	}
	// No errors
	return nil
}

func Copy[T any](sc *SmartCopier, src interface{}) (T, error) {
	var dest T // Zero value of T
	var zero T // Explicit zero value for return on error
	destType := reflect.TypeOf(dest)

	// Handle src == nil directly
	if src == nil {
		if sc.skipNilSrc {
			sc.debug("Copy[T]: Source is nil, skipNilSrc=true, returning zero T")
			return zero, nil // Return zero T, nil error
		}
		sc.debug("Copy[T]: Source is nil, skipNilSrc=false, returning zero T and ErrSourceIsNil")
		err := ErrSourceIsNil
		if sc.panicOnError {
			panic(fmt.Sprintf("mm copy failed: %v", err))
		}
		return zero, err // Return zero T and the error
	}

	destVal := reflect.New(destType).Elem() // Create a new instance of T
	srcVal := reflect.ValueOf(src)

	// Handle src being a nil pointer explicitly (different from src == nil)
	if srcVal.Kind() == reflect.Ptr {
		if srcVal.IsNil() {
			if sc.skipNilSrc {
				sc.debug("Copy[T]: Source is nil pointer, skipNilSrc=true, returning zero T")
				return zero, nil // Return zero T, nil error
			}
			// If not skipping nil, a nil pointer source results in a zero value destination
			sc.debug("Copy[T]: Source is nil pointer, skipNilSrc=false, returning zero T")
			return zero, nil // Return zero T, nil error (as per TestGenericCopy expectation)
		}
		srcVal = srcVal.Elem() // Dereference non-nil pointer
	}

	ctx := &copyContext{
		depth:   0,
		visited: make(map[visitKey]reflect.Value),
		errors:  make([]error, 0),
	}

	err := sc.copyRecursive(destVal, srcVal, ctx)
	finalErr := sc.finalizeResult(err, ctx)
	if finalErr != nil {
		return zero, finalErr // Return zero T on error
	}

	// Ensure the final result is addressable if T is not a pointer type itself
	finalResult := destVal
	if destVal.CanAddr() && destType.Kind() != reflect.Ptr {
		// If T is not a pointer, return the value.
		// Interface() handles this correctly.
	} else if !destVal.CanAddr() && destType.Kind() == reflect.Ptr {
		// If T is a pointer, but destVal is not addressable (shouldn't happen with New().Elem())
		// Fallback or error might be needed, but assume Interface() works
	}

	// Check if the result can be converted to T
	if !finalResult.IsValid() {
		// Should not happen if copyRecursive worked, but handle defensively
		return zero, fmt.Errorf("internal error: copy resulted in invalid value for type %s", destType)
	}
	if !finalResult.Type().AssignableTo(destType) {
		// Try conversion if direct assignment fails
		if finalResult.Type().ConvertibleTo(destType) {
			finalResult = finalResult.Convert(destType)
		} else {
			return zero, fmt.Errorf("internal error: copy result type %s cannot be assigned or converted to target type %s", finalResult.Type(), destType)
		}
	}

	return finalResult.Interface().(T), nil
}

func (sc *SmartCopier) CopyTo(dest, src interface{}) error {
	sc.debug("CopyTo called with dest=%T, src=%T", dest, src)
	if dest == nil {
		sc.debug("Destination is nil")
		err := ErrDestinationIsNil
		if sc.panicOnError {
			panic(fmt.Sprintf("mm.CopyTo failed: %v", err))
		}
		return err
	}

	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.IsNil() {
		sc.debug("Destination is not a non-nil pointer: %v Kind=%s IsNil=%t", destVal, destVal.Kind(), destVal.IsValid() && destVal.IsNil())
		err := ErrDestNotPointer
		if sc.panicOnError {
			panic(fmt.Sprintf("mm.CopyTo failed: %v", err))
		}
		return err
	}

	destElem := destVal.Elem() // The value the pointer points to
	if !destElem.CanSet() {
		err := fmt.Errorf("destination element (type %s) is not settable", destElem.Type())
		if sc.panicOnError {
			panic(fmt.Sprintf("mm.CopyTo failed: %v", err))
		}
		return err
	}

	ctx := &copyContext{
		depth:   0,
		visited: make(map[visitKey]reflect.Value),
		errors:  make([]error, 0),
	}

	// Pass destElem (the value) and src to internal copy
	err := sc.copyToInternal(destElem, src, ctx)
	return sc.finalizeResult(err, ctx)
}

// Internal helper that works with the destination *value*
func (sc *SmartCopier) copyToInternal(destElem reflect.Value, src interface{}, ctx *copyContext) error {
	sc.debug("copyToInternal called with destElem=%s, src=%T", destElem.Type(), src)

	// Handle nil source interface
	if src == nil {
		if sc.skipNilSrc {
			sc.debug("Source is nil, skipNilSrc=true, skipping.")
			return nil
		}
		// Source is nil, zero the destination if overwrite=true
		if sc.overwrite && destElem.IsValid() && destElem.CanSet() {
			sc.debug("Source is nil, skipNilSrc=false, overwrite=true. Zeroing destination %s.", destElem.Type())
			destElem.Set(reflect.Zero(destElem.Type()))
		} else {
			sc.debug("Source is nil, skipNilSrc=false, overwrite=false. Destination %s remains unchanged.", destElem.Type())
		}
		return nil // No error, just potentially zeroed dest
	}

	srcVal := reflect.ValueOf(src)

	// Handle source being a nil pointer
	if srcVal.Kind() == reflect.Ptr {
		if srcVal.IsNil() {
			if sc.skipNilSrc {
				sc.debug("Source is nil pointer, skipNilSrc=true, skipping.")
				return nil
			}
			// Source is nil pointer, zero the destination if overwrite=true
			if sc.overwrite && destElem.IsValid() && destElem.CanSet() {
				sc.debug("Source is nil pointer, skipNilSrc=false, overwrite=true. Zeroing destination %s.", destElem.Type())
				destElem.Set(reflect.Zero(destElem.Type()))
			} else {
				sc.debug("Source is nil pointer, skipNilSrc=false, overwrite=false. Destination %s remains unchanged.", destElem.Type())
			}
			return nil // No error
		}
		srcVal = srcVal.Elem() // Dereference non-nil pointer
	}

	if !destElem.IsValid() {
		err := fmt.Errorf("destination element became invalid")
		if sc.handlePotentialError(err, ctx) {
			return nil
		} // Should not happen if initial check passed
		return err
	}
	if !srcVal.IsValid() {
		// This might happen if src was e.g., an interface containing a nil pointer that got dereferenced
		if sc.skipNilSrc {
			return nil
		}
		if sc.overwrite && destElem.CanSet() {
			destElem.Set(reflect.Zero(destElem.Type()))
		}
		return nil
	}

	sc.debug("Starting recursive copy from %s to %s", srcVal.Type(), destElem.Type())
	return sc.copyRecursive(destElem, srcVal, ctx)
}

func (sc *SmartCopier) MustCopyTo(dest, src interface{}) {
	originalPanic := sc.panicOnError
	sc.panicOnError = true
	defer func() { sc.panicOnError = originalPanic }() // Ensure panic mode is reset

	if err := sc.CopyTo(dest, src); err != nil {
		// CopyTo already panics if panicOnError is true and error occurs
		// If it returns an error here, it means panicOnError was false initially,
		// but we still need to panic now.
		panic(fmt.Sprintf("mm.MustCopyTo failed: %v", err))
	}
}

func (sc *SmartCopier) MergeTo(dest, src interface{}) error {
	// Merge implies overwrite=false (don't overwrite non-empty dest fields)
	// AND skipEmptyValue=true (don't copy empty source values over dest)
	originalOverwrite := sc.overwrite
	originalSkipEmpty := sc.skipEmptyValue
	sc.overwrite = false
	sc.skipEmptyValue = true // Crucial for merge behavior
	defer func() {
		sc.overwrite = originalOverwrite
		sc.skipEmptyValue = originalSkipEmpty
	}()
	sc.debug("MergeTo called (overwrite=false, skipEmptyValue=true)")
	return sc.CopyTo(dest, src)
}

// CopyToMultiple copies the first source, then merges subsequent sources.
func (sc *SmartCopier) CopyToMultiple(dest interface{}, sources ...interface{}) error {
	if len(sources) == 0 {
		return nil // Nothing to do
	}
	if dest == nil {
		return ErrDestinationIsNil
	}
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.IsNil() {
		return ErrDestNotPointer
	}
	destElem := destVal.Elem()
	if !destElem.CanSet() {
		return fmt.Errorf("destination element (type %s) is not settable", destElem.Type())
	}

	ctx := &copyContext{
		depth:   0,
		visited: make(map[visitKey]reflect.Value),
		errors:  make([]error, 0),
	}

	var firstErr error
	// Copy first source normally (respecting current copier settings)
	sc.debug("CopyToMultiple: Copying first source")
	if err := sc.copyToInternal(destElem, sources[0], ctx); err != nil {
		if sc.handlePotentialError(err, ctx) {
			// Error collected, continue if possible
		} else {
			firstErr = err // Store first non-collected error
		}
	}
	// If we are not collecting errors and got one, stop early
	if !sc.collectErrors && firstErr != nil {
		return sc.finalizeResult(firstErr, ctx)
	}

	// Merge subsequent sources
	if len(sources) > 1 {
		originalOverwrite := sc.overwrite
		originalSkipEmpty := sc.skipEmptyValue
		sc.overwrite = false     // Ensure merge behavior
		sc.skipEmptyValue = true // Ensure merge behavior
		defer func() {           // Restore original settings
			sc.overwrite = originalOverwrite
			sc.skipEmptyValue = originalSkipEmpty
		}()
		sc.debug("CopyToMultiple: Merging subsequent sources (overwrite=false, skipEmptyValue=true)")
		for i := 1; i < len(sources); i++ {
			if err := sc.copyToInternal(destElem, sources[i], ctx); err != nil {
				if sc.handlePotentialError(err, ctx) {
					// Error collected, continue if possible
				} else if firstErr == nil { // Store first non-collected error
					firstErr = err
				}
			}
			// If not collecting errors and got one, stop early
			if !sc.collectErrors && firstErr != nil {
				break // Stop processing further sources
			}
		}
	}

	return sc.finalizeResult(firstErr, ctx)
}

// --- Internal Implementation ---

func (sc *SmartCopier) copyRecursive(dest, src reflect.Value, ctx *copyContext) (err error) {
	// Handle maximum depth
	if sc.maxDepth >= 0 && ctx.depth > sc.maxDepth {
		sc.debug("Depth %d: Max depth (%d) reached, stopping recursion.", ctx.depth, sc.maxDepth)
		return nil
	}

	// --- Source Validity and Nil/Zero/Empty Checks ---
	if !src.IsValid() {
		// This can happen with nil interface elements, etc.
		if sc.skipNilSrc {
			sc.debug("Depth %d: Source is invalid, skipNilSrc=true, skipping.", ctx.depth)
			return nil
		}
		if sc.overwrite && dest.IsValid() && dest.CanSet() {
			sc.debug("Depth %d: Source is invalid, overwrite=true, zeroing destination %s.", ctx.depth, dest.Type())
			dest.Set(reflect.Zero(dest.Type()))
		} else {
			sc.debug("Depth %d: Source is invalid, overwrite=false or dest not settable.", ctx.depth)
		}
		return nil
	}

	srcType := src.Type()
	destType := dest.Type()
	sc.debug("Depth %d: Enter copyRecursive: Dest: %s(%v) <- Src: %s(%v)", ctx.depth, destType, dest.Kind(), srcType, src.Kind())

	// Dereference source pointer *after* initial IsValid check
	// Store original src kind for cycle check before potential deref
	srcKindOriginal := src.Kind()
	srcPtrOriginal := src
	if srcKindOriginal == reflect.Ptr {
		if src.IsNil() {
			if sc.skipNilSrc {
				sc.debug("Depth %d: Source pointer is nil, skipNilSrc=true, skipping.", ctx.depth)
				return nil
			}
			if sc.overwrite && dest.IsValid() && dest.CanSet() {
				sc.debug("Depth %d: Source pointer is nil, overwrite=true, zeroing destination %s.", ctx.depth, destType)
				dest.Set(reflect.Zero(destType))
			} else {
				sc.debug("Depth %d: Source pointer is nil, overwrite=false or dest not settable.", ctx.depth)
			}
			return nil
		}
		// Dereference non-nil pointer for further checks/copying
		src = src.Elem()
		// Check validity again after dereferencing
		if !src.IsValid() {
			if sc.skipNilSrc {
				return nil
			} // Should be redundant?
			if sc.overwrite && dest.IsValid() && dest.CanSet() {
				dest.Set(reflect.Zero(destType))
			}
			return nil
		}
		srcType = src.Type() // Update srcType after deref
		sc.debug("Depth %d: Dereferenced source pointer. New Src: %s(%v)", ctx.depth, srcType, src.Kind())
	}

	// Apply skipNilSrc, skipZeroValue, skipEmptyValue based on the actual source value (potentially dereferenced)
	if sc.skipNilSrc {
		switch src.Kind() {
		case reflect.Map, reflect.Slice, reflect.Interface, reflect.Chan, reflect.Func:
			if src.IsNil() {
				sc.debug("Depth %d: Skipping nil source (%v) due to skipNilSrc", ctx.depth, src.Kind())
				return nil
			}
		}
	}
	if sc.skipZeroValue && src.IsZero() {
		sc.debug("Depth %d: Skipping zero-value source (%v) due to skipZeroValue", ctx.depth, src.Interface())
		return nil
	}
	if sc.skipEmptyValue && isEmptyValue(src) {
		sc.debug("Depth %d: Skipping empty-value source (%v) due to skipEmptyValue", ctx.depth, src.Interface())
		return nil
	}

	// --- Destination Validity Check ---
	if !dest.IsValid() {
		err = fmt.Errorf("internal error: destination %s became invalid before copy", destType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}
	if !dest.CanSet() {
		// This check is crucial. If dest isn't settable here, we can't proceed.
		// This might happen if passing e.g. an unexported struct field value directly.
		sc.debug("Depth %d: Destination %s (%v) is not settable, skipping.", ctx.depth, destType, dest.Kind())
		// Don't return an error, just can't perform the copy on this path.
		// Field-level copies handle settability via getSettableFieldValue.
		// This guards direct calls like map[int]MyStruct where MyStruct isn't settable.
		return nil
	}

	// --- Cycle Detection ---
	canTrackCycle := false
	var key visitKey
	if sc.enableCycleDetect {
		// Determine the source reference (pointer or addressable value) to track
		sourceToTrack := srcPtrOriginal // Use original source before potential pointer deref
		sourceKindToTrack := srcKindOriginal
		if sourceKindToTrack == reflect.Ptr && sourceToTrack.IsNil() {
			// Don't track nil pointers
		} else if sourceKindToTrack == reflect.Interface && sourceToTrack.IsNil() {
			// Don't track nil interfaces
		} else {
			// Track pointers, maps, slices, and addressable structs
			switch sourceKindToTrack {
			case reflect.Ptr, reflect.Map, reflect.Slice:
				if !sourceToTrack.IsNil() { // Extra nil check for map/slice
					key = visitKey{ptr: sourceToTrack.Pointer(), typ: sourceToTrack.Type()}
					canTrackCycle = true
				}
			case reflect.Struct:
				if sourceToTrack.CanAddr() {
					key = visitKey{ptr: sourceToTrack.Addr().Pointer(), typ: sourceToTrack.Addr().Type()}
					canTrackCycle = true
				}
			case reflect.Interface:
				// Track the concrete value within the interface if possible
				elem := sourceToTrack.Elem()
				if elem.IsValid() {
					elemKind := elem.Kind()
					if elemKind == reflect.Ptr || elemKind == reflect.Map || elemKind == reflect.Slice {
						if !elem.IsNil() {
							key = visitKey{ptr: elem.Pointer(), typ: elem.Type()}
							canTrackCycle = true
						}
					} else if elemKind == reflect.Struct && elem.CanAddr() {
						key = visitKey{ptr: elem.Addr().Pointer(), typ: elem.Addr().Type()}
						canTrackCycle = true
					}
				}
			}
		}

		if canTrackCycle {
			if existingDestAddr, visited := ctx.visited[key]; visited {
				sc.debug("Depth %d: Cycle detected for src ptr %x (%s)", ctx.depth, key.ptr, key.typ)
				if dest.CanSet() && existingDestAddr.IsValid() {
					existingDest := existingDestAddr // Assume stored value is address/pointer
					if !existingDest.IsValid() {     // Defensive check
						sc.debug("Depth %d: Cycle detected but stored destination is invalid.", ctx.depth)
						return nil // Or return error?
					}

					// Try assigning/converting the stored destination (which should be an address/pointer)
					// or its element to the current destination.
					if existingDest.Type().AssignableTo(destType) {
						dest.Set(existingDest)
						sc.debug("Depth %d: Cycle assignment: Dest %s = Existing %s (assignable Addr)", ctx.depth, destType, existingDest.Type())
						return nil
					}
					if existingDest.Kind() == reflect.Ptr {
						existingElem := existingDest.Elem()
						if existingElem.IsValid() && existingElem.Type().AssignableTo(destType) {
							dest.Set(existingElem)
							sc.debug("Depth %d: Cycle assignment: Dest %s = Existing %s (assignable Elem)", ctx.depth, destType, existingElem.Type())
							return nil
						}
						// Handle case where dest is a pointer but existing is pointer to different type
						if dest.Kind() == reflect.Ptr && existingElem.IsValid() && existingElem.Type().AssignableTo(dest.Type().Elem()) {
							if dest.IsNil() {
								dest.Set(reflect.New(dest.Type().Elem()))
							}
							dest.Elem().Set(existingElem)
							sc.debug("Depth %d: Cycle assignment: Dest Elem %s = Existing Elem %s (assignable)", ctx.depth, dest.Elem().Type(), existingElem.Type())
							return nil
						}
					}

					// Try conversion (less common for cycles, but possible)
					converted, convErr := sc.tryConvert(existingDest, destType)
					if convErr == nil && converted.IsValid() {
						dest.Set(converted)
						sc.debug("Depth %d: Cycle assignment: Dest %s = Existing %s (converted Addr)", ctx.depth, destType, existingDest.Type())
						return nil
					}
					if existingDest.Kind() == reflect.Ptr {
						existingElem := existingDest.Elem()
						if existingElem.IsValid() {
							convertedElem, convElemErr := sc.tryConvert(existingElem, destType)
							if convElemErr == nil && convertedElem.IsValid() {
								dest.Set(convertedElem)
								sc.debug("Depth %d: Cycle assignment: Dest %s = Existing %s (converted Elem)", ctx.depth, destType, existingElem.Type())
								return nil
							}
						}
					}

					// If no assignment/conversion works
					errMsg := fmt.Sprintf("detected cycle ptr %x type %s, but existing dest addr type %s / elem type %s not assignable or convertible to %s",
						key.ptr, key.typ, existingDest.Type(), existingDest.Elem().Type(), destType)
					err = fmt.Errorf("%w: %s", ErrCycleDetected, errMsg)
					if sc.handlePotentialError(err, ctx) {
						return nil
					}
					return err
				}
			} else {
				// Store the *address* of the destination if possible, otherwise the value itself.
				// This helps when the cycle needs to assign a pointer back.
				valueToStore := dest
				if dest.CanAddr() {
					valueToStore = dest.Addr()
					sc.debug("Depth %d: Adding cycle track: src ptr %x (%s) -> dest addr %v (%s)", ctx.depth, key.ptr, key.typ, valueToStore, valueToStore.Type())
				} else {
					// Less ideal, might cause issues if cycle needs to assign a pointer later
					sc.debug("Depth %d: Adding cycle track: src ptr %x (%s) -> dest value %v (%s) (not addressable)", ctx.depth, key.ptr, key.typ, valueToStore, valueToStore.Type())
				}
				if valueToStore.IsValid() {
					ctx.visited[key] = valueToStore
				}
			}
		}
	}

	// --- Fast Paths (Assignment, Conversion, Unsafe MemCopy) ---

	// 1. Direct Assignment (if types match and dest is settable)
	if srcType.AssignableTo(destType) {
		if dest.CanSet() { // Should already be true from check above
			sc.debug("Depth %d: Setting assignable type %s to %s", ctx.depth, srcType, destType)
			dest.Set(src)
			return nil
		}
	}

	// 2. Unsafe Memory Copy (if enabled, suitable, and configured)
	destInfo := sc.getTypeInfo(destType)
	if sc.useUnsafe && destType == srcType && destInfo.canMemCopy {
		// Check if unexported fields prevent memcopy unless allowed
		containsUnexported := hasUnexportedFields(destType) // Check destination type
		if !containsUnexported || (sc.copyUnexported && sc.useUnsafe) {
			if dest.CanAddr() && src.CanAddr() {
				copyErr := sc.unsafeMemCopy(dest, src, destInfo)
				if copyErr == nil {
					sc.debug("Depth %d: Unsafe memory copy successful for %s", ctx.depth, destType)
					return nil
				}
				sc.debug("Depth %d: Unsafe memory copy failed: %v. Falling back.", ctx.depth, copyErr)
				// Fall through to reflection copy if memcopy failed unexpectedly
			} else {
				sc.debug("Depth %d: Skipping unsafe memory copy for %s (dest or src not addressable)", ctx.depth, destType)
			}
		} else {
			sc.debug("Depth %d: Skipping unsafe memory copy for %s due to unexported fields and config", ctx.depth, destType)
		}
	}

	// 3. Type Conversion (Custom or Go built-in)
	// Try conversion only if direct assignment didn't work
	if srcType != destType {
		converted, convErr := sc.tryConvert(src, destType)
		if convErr == nil {
			if dest.CanSet() { // Check again just in case
				sc.debug("Depth %d: Setting converted type %s to %s", ctx.depth, converted.Type(), destType)
				dest.Set(converted)
				return nil
			}
		} else if !errors.Is(convErr, ErrNoConverter) {
			// If conversion failed for a reason other than no converter found
			err = fmt.Errorf("type conversion failed from %s to %s: %w", srcType, destType, convErr)
			if sc.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}
		// If no converter and types differ, fall through to kind-specific logic
		sc.debug("Depth %d: No direct assignment or conversion for %s -> %s. Trying kind-specific copy.", ctx.depth, srcType, destType)
	}

	// --- Kind-Specific Copy Logic ---
	ctx.depth++ // Increment depth for recursive calls
	sc.debug("Depth %d: Entering kind-specific copy for Dest: %s(%v) <- Src: %s(%v)", ctx.depth, destType, dest.Kind(), srcType, src.Kind())

	var copyErr error
	switch destKind := dest.Kind(); destKind {
	case reflect.Struct:
		srcKind := src.Kind()
		if srcKind == reflect.Struct {
			copyErr = sc.copyStruct(dest, src, ctx, destInfo)
		} else if srcKind == reflect.Map {
			copyErr = sc.copyMapToStruct(dest, src, ctx, destInfo)
		} else {
			copyErr = fmt.Errorf("%w: cannot copy %s to struct %s", ErrTypeMismatch, srcKind, destKind)
		}
	case reflect.Slice:
		if src.Kind() == reflect.Slice {
			copyErr = sc.copySlice(dest, src, ctx)
		} else {
			copyErr = fmt.Errorf("%w: cannot copy %s to slice %s", ErrTypeMismatch, src.Kind(), destKind)
		}
	case reflect.Map:
		srcKind := src.Kind()
		if srcKind == reflect.Map {
			copyErr = sc.copyMap(dest, src, ctx)
		} else if srcKind == reflect.Struct {
			copyErr = sc.copyStructToMap(dest, src, ctx)
		} else {
			copyErr = fmt.Errorf("%w: cannot copy %s to map %s", ErrTypeMismatch, srcKind, destKind)
		}
	case reflect.Ptr:
		// Source was already dereferenced if it was a pointer.
		// We are copying the underlying value *into* the destination pointer's element.
		copyErr = sc.copyPtr(dest, src, ctx)
	case reflect.Interface:
		copyErr = sc.copyInterface(dest, src, ctx)
	case reflect.Array:
		if src.Kind() == reflect.Array {
			copyErr = sc.copyArray(dest, src, ctx)
		} else {
			copyErr = fmt.Errorf("%w: cannot copy %s to array %s", ErrTypeMismatch, src.Kind(), destKind)
		}
	default: // Handle basic types (int, string, bool, etc.)
		// We should have already handled assignable/convertible cases.
		// This path might be hit if types are fundamentally different but share a base kind.
		// Or if CanSet() was false earlier but somehow became true (unlikely).
		if dest.CanSet() {
			// Re-attempt conversion as a last resort for basic kinds
			converted, convErr := sc.tryConvert(src, destType)
			if convErr == nil && converted.IsValid() {
				sc.debug("Depth %d: Default copy: Setting converted type %s to %s (via tryConvert)", ctx.depth-1, converted.Type(), destType)
				dest.Set(converted)
			} else {
				copyErr = fmt.Errorf("%w: unhandled default kind copy from %s (%s) to %s (%s), convErr=%v", ErrTypeMismatch, srcType, src.Kind(), destType, destKind, convErr)
			}
		} else {
			// If somehow dest became non-settable again
			copyErr = fmt.Errorf("cannot set basic kind %s (value %v) from source type %s", destKind, dest, srcType)
		}
	}
	ctx.depth-- // Decrement depth after returning from recursive call

	// Handle errors from kind-specific logic
	if copyErr != nil {
		if sc.handlePotentialError(copyErr, ctx) {
			return nil // Error was collected
		}
		return copyErr // Propagate error
	}

	sc.debug("Depth %d: Exit copyRecursive: Dest: %s(%v) <- Src: %s(%v)", ctx.depth, destType, dest.Kind(), srcType, src.Kind())
	return nil // Success
}

// --- Helper Functions ---

func hasUnexportedFields(typ reflect.Type) bool {
	if typ.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < typ.NumField(); i++ {
		// PkgPath is empty for exported fields, non-empty for unexported.
		if typ.Field(i).PkgPath != "" {
			return true
		}
	}
	return false
}

// --- Unsafe & Type Info ---

func (sc *SmartCopier) unsafeMemCopy(dest, src reflect.Value, info *typeInfo) error {
	// Ensure addresses are valid
	if !dest.CanAddr() || !src.CanAddr() {
		return fmt.Errorf("cannot perform memcopy on unaddressable values")
	}
	destPtr := unsafe.Pointer(dest.UnsafeAddr())
	srcPtr := unsafe.Pointer(src.UnsafeAddr())
	size := info.size
	if size == 0 {
		return nil // Nothing to copy
	}

	// Use a buffer from the pool? Might be overkill for small structs. Direct memmove is usually fine.
	// buf := sc.bufferPool.Get().([]byte)
	// if uintptr(len(buf)) < size {
	//  sc.bufferPool.Put(buf) // Put back small buffer
	//  buf = make([]byte, size)
	// } else {
	//  buf = buf[:size]
	// }
	// // Copy src to buffer, then buffer to dest? Or direct memmove?
	// // Direct memmove is simpler and likely faster unless dealing with overlapping memory regions (not typical here).
	// sc.bufferPool.Put(buf) // Ensure buffer is returned

	memmove(destPtr, srcPtr, size)
	// Keep alive might not be strictly necessary if pointers don't escape,
	// but safer esp. with unsafe operations.
	runtime.KeepAlive(dest.Interface())
	runtime.KeepAlive(src.Interface())
	return nil
}

func (sc *SmartCopier) getTypeInfo(typ reflect.Type) *typeInfo {
	if cached, ok := sc.typeInfoCache.Load(typ); ok {
		return cached.(*typeInfo)
	}

	info := &typeInfo{
		size:         typ.Size(),
		alignment:    uintptr(typ.Align()),
		containsPtrs: checkContainsPointers(typ),
		isStruct:     typ.Kind() == reflect.Struct,
		isSlice:      typ.Kind() == reflect.Slice,
		isMap:        typ.Kind() == reflect.Map,
		isPtr:        typ.Kind() == reflect.Ptr,
		isInterface:  typ.Kind() == reflect.Interface,
	}
	// Determine if memory copy is suitable based on type structure (no pointers)
	info.canMemCopy = isMemCopySuitable(typ)

	if info.isStruct {
		info.fields = make(map[string]fieldInfo)
		numFields := typ.NumField()
		for i := 0; i < numFields; i++ {
			field := typ.Field(i)
			fieldName := field.Name
			tagVal := field.Tag.Get(sc.tagName)
			tagOpts := parseTagOptions(tagVal) // Use refined tag parsing
			sc.debug("Parsed tag '%s' for field %s: %v", tagVal, fieldName, tagOpts)

			isExported := field.PkgPath == ""
			// Field is settable if exported OR if unexported copy is enabled via unsafe
			canSet := isExported || (sc.copyUnexported && sc.useUnsafe)

			fInfo := fieldInfo{
				name:        fieldName,
				structField: field,
				index:       field.Index,
				offset:      field.Offset,
				typ:         field.Type,
				canSet:      canSet,
				tagOpts:     tagOpts,
			}
			info.fields[fieldName] = fInfo

			// Check for the unmapped field tag
			if _, isUnmapped := tagOpts["unmapped"]; isUnmapped {
				fieldType := field.Type
				// Field must be map[string]interface{} or map[string]any
				if fieldType.Kind() == reflect.Map &&
					fieldType.Key().Kind() == reflect.String &&
					(fieldType.Elem().Kind() == reflect.Interface || fieldType.Elem().Kind() == reflect.Invalid) { // Allow any/interface{}
					if info.unmappedField == nil {
						info.unmappedField = &fInfo
						sc.debug("Found unmapped field '%s' in type %s", fieldName, typ)
					} else {
						// Log warning about multiple unmapped fields, only the first found is used
						log.Printf("[mm copier warning] Multiple fields tagged 'unmapped' in type %s. Using '%s', ignoring '%s'.", typ, info.unmappedField.name, fieldName)
					}
				} else {
					// Log warning about invalid type for unmapped field
					log.Printf("[mm copier warning] Field '%s' in type %s tagged 'unmapped' has invalid type %s. It must be map[string]interface{} or map[string]any. Ignoring tag.", fieldName, typ, fieldType)
				}
			}
		}
	}

	actual, _ := sc.typeInfoCache.LoadOrStore(typ, info)
	return actual.(*typeInfo)
}

// isMemCopySuitable checks if the raw memory layout is safe to copy directly.
// Generally true for primitive types and arrays/structs containing only such types.
func isMemCopySuitable(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return true
	case reflect.Array:
		// Array is suitable if its element type is suitable
		return isMemCopySuitable(typ.Elem())
	case reflect.Struct:
		// Struct is suitable only if it contains NO pointers anywhere
		return !checkContainsPointers(typ)
	default:
		// Pointers, slices, maps, channels, funcs, interfaces, strings are NOT suitable
		return false
	}
}

// checkContainsPointers recursively checks if a type contains any pointer types,
// including slice, map, chan, func, interface, string headers, or actual pointers.
func checkContainsPointers(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Ptr, reflect.UnsafePointer,
		reflect.Map, reflect.Slice, // These contain internal pointers
		reflect.Chan, reflect.Func, // These are pointers
		reflect.Interface, // Contains type and data pointers
		reflect.String:    // Contains data pointer
		return true
	case reflect.Array:
		// Array contains pointers if its element type does
		return checkContainsPointers(typ.Elem())
	case reflect.Struct:
		// Struct contains pointers if any of its fields do
		for i := 0; i < typ.NumField(); i++ {
			if checkContainsPointers(typ.Field(i).Type) {
				return true
			}
		}
		return false
	default:
		// Basic types (int, float, bool, complex) do not contain pointers
		return false
	}
}

// --- Kind-Specific Copy Logic ---

// getSettableFieldValue returns a settable reflect.Value for a struct field.
// It handles exported fields directly and attempts unsafe access for unexported
// fields if sc.copyUnexported and sc.useUnsafe are enabled.
// Returns an invalid reflect.Value if the field cannot be made settable.
func (sc *SmartCopier) getSettableFieldValue(structVal reflect.Value, fInfo *fieldInfo) reflect.Value {
	fieldVal := structVal.FieldByIndex(fInfo.index)

	// If exported and settable via reflection, return it
	if fieldVal.CanSet() {
		return fieldVal
	}

	// If unexported, attempt unsafe access ONLY if configured
	isUnexported := fInfo.structField.PkgPath != ""
	if isUnexported && sc.copyUnexported && sc.useUnsafe {
		if structVal.CanAddr() {
			fieldPtr := unsafe.Pointer(structVal.UnsafeAddr() + fInfo.offset)
			// Use reflect.NewAt to create a settable Value pointing to the field's memory
			// Elem() gets the actual value wrapper from the pointer wrapper NewAt creates.
			unsafeFieldVal := reflect.NewAt(fInfo.typ, fieldPtr).Elem()

			// Double-check if the unsafely obtained value is indeed settable
			if unsafeFieldVal.CanSet() {
				sc.debug("Field '%s': unsafe access successful, field is settable.", fInfo.name)
				return unsafeFieldVal
			}
			sc.debug("Field '%s': unsafe access attempted but resulting value is not settable.", fInfo.name)
		} else {
			sc.debug("Field '%s': unsafe access skipped because struct is not addressable.", fInfo.name)
		}
	}

	// If we reach here, the field is not settable (either unexported without permission, or reflection failed)
	sc.debug("Field '%s': cannot get settable value (Exported: %t, CanSet: %t, Unsafe/Unexported: %t)",
		fInfo.name, fInfo.structField.PkgPath == "", fieldVal.CanSet(), sc.copyUnexported && sc.useUnsafe)
	return reflect.Value{} // Return invalid value
}

// copyPtr handles copying *into* a destination pointer.
// The `src` value is the concrete value to be copied (already dereferenced if it was a pointer).
func (sc *SmartCopier) copyPtr(dest, src reflect.Value, ctx *copyContext) error {
	destType := dest.Type()         // This is the pointer type (e.g., *SimpleDest)
	destElemType := destType.Elem() // This is the element type (e.g., SimpleDest)
	sc.debug("Depth %d: copyPtr: Dest: %s <- Src: %s", ctx.depth, destType, src.Type())

	if !dest.CanSet() {
		err := fmt.Errorf("cannot set destination pointer %s", destType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	// If source value is invalid (e.g. from nil interface elem), potentially zero dest pointer
	if !src.IsValid() {
		if sc.skipNilSrc {
			return nil
		} // Already checked earlier but good defense
		if sc.overwrite {
			sc.debug("Depth %d: Source value is invalid, zeroing destination pointer %s", ctx.depth, destType)
			dest.Set(reflect.Zero(destType)) // Set pointer to nil
		}
		return nil
	}

	// Allocate destination pointer if it's nil
	if dest.IsNil() {
		sc.debug("Depth %d: Allocating new pointer for destination %s", ctx.depth, destType)
		newDestPtr := reflect.New(destElemType) // Create new *SimpleDest
		dest.Set(newDestPtr)                    // Assign the new pointer to the destination field/variable
	} else if !sc.overwrite {
		// If dest pointer exists and we are not overwriting, do nothing more.
		// The value it points to remains unchanged.
		sc.debug("Depth %d: Skipping pointer element copy as !overwrite and destination pointer %s is not nil", ctx.depth, destType)
		return nil
	}
	// If dest is not nil AND overwrite is true, we proceed to copy into dest.Elem()

	// Get the element the destination pointer points to
	destElem := dest.Elem()
	if !destElem.IsValid() {
		// Should not happen if dest.Set(reflect.New(...)) worked or if dest was non-nil initially
		err := fmt.Errorf("internal error: dest pointer elem invalid for %s after allocation/check", destType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}
	if !destElem.CanSet() {
		// This usually means the pointer itself was unexported or part of an unexported chain.
		err := fmt.Errorf("destination pointer element %s is not settable", destElem.Type())
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	sc.debug("Depth %d: Recursively copying element for pointer Dest Elem: %s <- Src: %s", ctx.depth, destElem.Type(), src.Type())
	// Recursively copy the source value into the destination element
	return sc.copyRecursive(destElem, src, ctx)
}

func (sc *SmartCopier) copyStruct(dest, src reflect.Value, ctx *copyContext, destInfo *typeInfo) error {
	sc.debug("Depth %d: copyStruct: Dest: %s <- Src: %s", ctx.depth, dest.Type(), src.Type())
	srcInfo := sc.getTypeInfo(src.Type())
	usedSrcFields := make(map[string]struct{}) // Track source fields copied to handle unmapped ones

	// --- Handle Unmapped Field Initialization ---
	var unmappedMap reflect.Value
	var unmappedValueType reflect.Type
	var unmappedFieldName string = "<none>"
	if destInfo.unmappedField != nil {
		fieldInfo := destInfo.unmappedField
		unmappedFieldName = fieldInfo.name
		unmappedMap = sc.getSettableFieldValue(dest, fieldInfo) // Get the map field value
		if unmappedMap.IsValid() {
			if !unmappedMap.CanSet() {
				// Should not happen if getSettableFieldValue worked, but check defensively
				sc.debug("-> Warning: Unmapped field '%s' retrieved but cannot be set. Ignoring.", unmappedFieldName)
				unmappedMap = reflect.Value{} // Invalidate it
			} else if unmappedMap.IsNil() {
				// Initialize the map if it's nil and we can set it
				sc.debug("-> Initializing unmapped field map '%s' (%s)", unmappedFieldName, fieldInfo.typ)
				unmappedMap.Set(reflect.MakeMap(fieldInfo.typ))
			}
			if unmappedMap.IsValid() {
				unmappedValueType = fieldInfo.typ.Elem() // Get map value type (e.g., interface{})
			}
		} else {
			sc.debug("-> Cannot get settable value for unmapped field '%s'. Unmapped source fields will be ignored.", unmappedFieldName)
			// Keep unmappedMap invalid
		}
	}

	// --- Build Source Tag Mapping ---
	// Map source field names to the destination field name specified in the source tag `mm:"DestName"`
	srcTagToDestName := make(map[string]string)
	for srcFName, sfInfo := range srcInfo.fields {
		if tagName, ok := sfInfo.tagOpts["name"]; ok { // Name is explicitly provided in source tag
			srcTagToDestName[srcFName] = tagName
		}
	}

	// --- Iterate Over Destination Fields ---
	for destFieldName, dfInfo := range destInfo.fields {
		sc.debug("-> Processing Dest Field: '%s'", destFieldName)

		// Skip the unmapped field itself
		if unmappedMap.IsValid() && destFieldName == unmappedFieldName {
			sc.debug("-> Skipping unmapped field '%s' itself.", destFieldName)
			continue
		}

		// --- Check Skip Conditions (Global, Tags) ---
		if _, shouldSkip := sc.skipFields[destFieldName]; shouldSkip {
			sc.debug("-> Skip: Global config for dest field '%s'", destFieldName)
			continue
		}
		// Check '-' tag on destination field
		if _, isMinus := dfInfo.tagOpts["-"]; isMinus {
			sc.debug("-> Skip: Dest field '%s' has '-' tag.", destFieldName)
			continue
		}

		// --- Determine Source Field Name ---
		sourceFieldName := destFieldName // Default: Match by name
		// 1. Check destination tag `mm:"SrcName"`
		if srcNameFromDestTag, ok := dfInfo.tagOpts["name"]; ok {
			sourceFieldName = srcNameFromDestTag
			sc.debug("-> Source name mapped via dest tag: '%s' -> '%s'", sourceFieldName, destFieldName)
		}
		// 2. Check source tag `mm:"DestName"` (overrides dest tag if conflicting)
		for srcFName, targetDestName := range srcTagToDestName {
			if targetDestName == destFieldName {
				sourceFieldName = srcFName
				sc.debug("-> Source name mapped via source tag: '%s' -> '%s'", sourceFieldName, destFieldName)
				break // Found mapping from source tag
			}
		}
		// 3. Check global config mapping (overrides tags)
		for mapSrc, mapDst := range sc.fieldMappings {
			if mapDst == destFieldName {
				sourceFieldName = mapSrc
				sc.debug("-> Source name mapped via config: '%s' -> '%s'", sourceFieldName, destFieldName)
				break
			}
		}

		// --- Get Source Field Value ---
		sfInfo, srcFieldExists := srcInfo.fields[sourceFieldName]
		if !srcFieldExists {
			sc.debug("-> Skip: No source field named '%s' found for dest field '%s'", sourceFieldName, destFieldName)
			continue
		}

		// Check if source field is unexported and if we can/should access it
		isSrcUnexported := sfInfo.structField.PkgPath != ""
		if isSrcUnexported && !(sc.copyUnexported && sc.useUnsafe) {
			sc.debug("-> Skip: Source field '%s' is unexported and copyUnexported/unsafe config prevents access.", sourceFieldName)
			continue // Skip this field
		}

		// Get the source field value (using reflection - might fail for unexported without unsafe)
		srcField := src.FieldByIndex(sfInfo.index)
		if !srcField.IsValid() {
			// This might happen if FieldByIndex fails (e.g., unexported field without unsafe)
			// We currently don't attempt unsafe *reads* reliably.
			sc.debug("-> Skip: Could not get valid source field value for '%s' (possibly unexported without unsafe read).", sourceFieldName)
			continue
		}

		finalSrcValue := srcField // Start with the retrieved source value

		// --- Apply Source-Based Skip Conditions ('omitempty', skipZero, skipEmpty) ---
		// These depend on the *source* value.
		if _, omit := sfInfo.tagOpts["omitempty"]; omit && isEmptyValue(finalSrcValue) {
			sc.debug("-> Skip: Source value for '%s' is empty and 'omitempty' tag set on source field.", sourceFieldName)
			usedSrcFields[sourceFieldName] = struct{}{} // Mark used even if skipped by omitempty
			continue
		}
		if sc.skipZeroValue && finalSrcValue.IsZero() {
			sc.debug("-> Skip: Source field '%s' is zero-valued and skipZeroValue=true.", sourceFieldName)
			usedSrcFields[sourceFieldName] = struct{}{}
			continue
		}
		if sc.skipEmptyValue && isEmptyValue(finalSrcValue) {
			sc.debug("-> Skip: Source field '%s' is empty and skipEmptyValue=true.", sourceFieldName)
			usedSrcFields[sourceFieldName] = struct{}{}
			continue
		}

		// --- Get Destination Field and Check Overwrite ---
		destField := sc.getSettableFieldValue(dest, &dfInfo)
		if !destField.IsValid() {
			sc.debug("-> Skip: Cannot get settable destination field '%s' value.", destFieldName)
			continue // Cannot proceed if destination isn't settable
		}

		// Check overwrite condition based on *destination* state and *config*
		// Use isEmptyValue for the check, as MergeTo sets overwrite=false, skipEmpty=true
		// and expects non-empty dest fields to be kept.
		if !sc.overwrite && destField.IsValid() && !isEmptyValue(destField) {
			sc.debug("-> Skip: Destination field '%s' is non-empty and overwrite=false.", destFieldName)
			continue
		}

		// --- Apply Formatter ---
		if formatter, ok := sc.formatters[destFieldName]; ok {
			if finalSrcValue.CanInterface() {
				sc.debug("-> Applying formatter to source value for field '%s'", destFieldName)
				formattedInterface := formatter(finalSrcValue.Interface())
				// Update finalSrcValue with the result of the formatter
				if formattedInterface == nil {
					// Formatter returned nil, treat as zero value for the destination type
					finalSrcValue = reflect.Zero(dfInfo.typ) // Use dest type's zero
				} else {
					finalSrcValue = reflect.ValueOf(formattedInterface)
				}
				// Check if formatter returned a compatible type, convert if needed
				if finalSrcValue.IsValid() && finalSrcValue.Type() != dfInfo.typ {
					if finalSrcValue.Type().ConvertibleTo(dfInfo.typ) {
						finalSrcValue = finalSrcValue.Convert(dfInfo.typ)
					} else {
						sc.debug("-> Warning: Formatter for '%s' returned incompatible type %s (expected %s). Skipping field.", destFieldName, finalSrcValue.Type(), dfInfo.typ)
						continue // Skip if formatter result is wrong type
					}
				}
			} else {
				sc.debug("-> Warning: Cannot call Interface() on source field '%s' for formatter. Skipping formatter.", sourceFieldName)
			}
		}
		// Re-check validity after formatter
		if !finalSrcValue.IsValid() {
			sc.debug("-> Skip: Source value became invalid after formatting for field '%s'.", destFieldName)
			continue
		}

		// --- Recursive Copy ---
		sc.debug("-> Recursing copy: Dest: %s (%s) <- Src: %s (%s)", destFieldName, destField.Type(), sourceFieldName, finalSrcValue.Type())
		usedSrcFields[sourceFieldName] = struct{}{} // Mark source field as used before recursing
		if err := sc.copyRecursive(destField, finalSrcValue, ctx); err != nil {
			fieldErr := fmt.Errorf("copying field '%s' <- '%s': %w", destFieldName, sourceFieldName, err)
			if sc.handlePotentialError(fieldErr, ctx) {
				continue // Continue with next field if error collected
			}
			return fieldErr // Propagate error if not collected
		}
	}

	// --- Handle Unmapped Source Fields ---
	if unmappedMap.IsValid() { // Check if unmapped field/map exists and is settable
		sc.debug("-> Processing unmapped source fields for destination map '%s'", unmappedFieldName)
		for srcFieldName, sfInfo := range srcInfo.fields {
			if _, used := usedSrcFields[srcFieldName]; !used {
				sc.debug(" -> Found unmapped source field: '%s'", srcFieldName)

				// Check source field tags like '-', 'omitempty' for unmapped fields too
				srcTagOpts := sfInfo.tagOpts
				if _, isMinus := srcTagOpts["-"]; isMinus {
					continue
				}
				if _, omit := srcTagOpts["omitempty"]; omit {
					srcFieldValCheck := src.FieldByIndex(sfInfo.index)
					if !srcFieldValCheck.IsValid() || isEmptyValue(srcFieldValCheck) {
						sc.debug(" -> Skipping unmapped source field '%s' due to 'omitempty' tag and empty value.", srcFieldName)
						continue
					}
				}

				// Check if source is unexported and shouldn't be copied
				isSrcUnexported := sfInfo.structField.PkgPath != ""
				if isSrcUnexported && !(sc.copyUnexported && sc.useUnsafe) {
					sc.debug(" -> Skip unmapped: Source field '%s' is unexported.", srcFieldName)
					continue
				}

				// Get source field value
				srcFieldVal := src.FieldByIndex(sfInfo.index)
				if !srcFieldVal.IsValid() {
					// Cannot read unexported without unsafe, skip.
					sc.debug(" -> Skip unmapped: Cannot get valid value for source field '%s'.", srcFieldName)
					continue
				}

				// Skip based on global settings if configured
				if sc.skipZeroValue && srcFieldVal.IsZero() {
					continue
				}
				if sc.skipEmptyValue && isEmptyValue(srcFieldVal) {
					continue
				}

				// Copy the value into the unmapped map
				mapKey := reflect.ValueOf(srcFieldName)
				mapValue := reflect.New(unmappedValueType).Elem() // Create holder for map value (e.g., interface{})

				sc.debug("  -> Recursing copy for unmapped: map['%s'] (%s) <- %s (%s)", srcFieldName, mapValue.Type(), srcFieldName, srcFieldVal.Type())
				if err := sc.copyRecursive(mapValue, srcFieldVal, ctx); err == nil {
					// Set the value in the map
					unmappedMap.SetMapIndex(mapKey, mapValue)
				} else {
					unmappedErr := fmt.Errorf("copying unmapped source field '%s' to map '%s': %w", srcFieldName, unmappedFieldName, err)
					if sc.handlePotentialError(unmappedErr, ctx) {
						continue // Collect and continue
					}
					return unmappedErr // Propagate
				}
			}
		}
	}

	return nil
}

func (sc *SmartCopier) copyMapToStruct(dest reflect.Value, src reflect.Value, ctx *copyContext, destInfo *typeInfo) error {
	sc.debug("Depth %d: copyMapToStruct: Dest: %s <- Src: %s", ctx.depth, dest.Type(), src.Type())
	if src.Kind() != reflect.Map { // Defensive check
		return fmt.Errorf("%w: source is %s, not map", ErrTypeMismatch, src.Kind())
	}
	srcType := src.Type()
	if srcType.Key().Kind() != reflect.String {
		err := ErrMapKeyNotString
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}
	if src.IsNil() {
		sc.debug("-> Source map is nil, nothing to copy.")
		return nil // Nothing to copy from nil map
	}

	// --- Handle Unmapped Field Initialization ---
	var unmappedMap reflect.Value
	var unmappedValueType reflect.Type
	var unmappedFieldName string = "<none>"
	if destInfo.unmappedField != nil {
		fieldInfo := destInfo.unmappedField
		unmappedFieldName = fieldInfo.name
		unmappedMap = sc.getSettableFieldValue(dest, fieldInfo)
		if unmappedMap.IsValid() {
			if !unmappedMap.CanSet() {
				sc.debug("-> Warning: Unmapped field '%s' retrieved but cannot be set. Ignoring.", unmappedFieldName)
				unmappedMap = reflect.Value{}
			} else if unmappedMap.IsNil() {
				sc.debug("-> Initializing unmapped field map '%s' (%s)", unmappedFieldName, fieldInfo.typ)
				unmappedMap.Set(reflect.MakeMap(fieldInfo.typ))
			}
			if unmappedMap.IsValid() {
				unmappedValueType = fieldInfo.typ.Elem()
			}
		} else {
			sc.debug("-> Cannot get settable value for unmapped field '%s'. Unused map keys will be ignored.", unmappedFieldName)
		}
	}

	usedMapKeys := make(map[string]struct{}) // Track map keys copied to handle unmapped ones

	// --- Iterate Over Destination Struct Fields ---
	for destFieldName, dfInfo := range destInfo.fields {
		sc.debug("-> Processing Dest Field: '%s'", destFieldName)

		// Skip the unmapped field itself
		if unmappedMap.IsValid() && destFieldName == unmappedFieldName {
			sc.debug("-> Skipping unmapped field '%s' itself.", destFieldName)
			continue
		}

		// --- Check Skip Conditions (Global, Tags) ---
		if _, shouldSkip := sc.skipFields[destFieldName]; shouldSkip {
			sc.debug("-> Skip: Global config for dest field '%s'", destFieldName)
			continue
		}
		if _, isMinus := dfInfo.tagOpts["-"]; isMinus {
			sc.debug("-> Skip: Dest field '%s' has '-' tag.", destFieldName)
			continue
		}

		// --- Determine Source Map Key ---
		sourceMapKey := destFieldName // Default: Struct field name is map key
		// Check destination tag `mm:"MapKeyName"`
		if mapKeyName, ok := dfInfo.tagOpts["name"]; ok {
			sourceMapKey = mapKeyName
			sc.debug("-> Source map key mapped via dest tag: '%s'", sourceMapKey)
		}
		// Check global config mapping (overrides tag)
		for mapSrc, mapDst := range sc.fieldMappings {
			if mapDst == destFieldName {
				sourceMapKey = mapSrc
				sc.debug("-> Source map key mapped via config: '%s'", sourceMapKey)
				break
			}
		}

		// --- Get Source Map Value ---
		srcMapValue := src.MapIndex(reflect.ValueOf(sourceMapKey))
		if !srcMapValue.IsValid() {
			sc.debug("-> Skip: No key '%s' in source map.", sourceMapKey)
			continue // Key doesn't exist in map
		}

		// Handle map value being interface{}
		concreteSrcValue := srcMapValue
		if srcMapValue.Kind() == reflect.Interface {
			concreteSrcValue = srcMapValue.Elem()
			if !concreteSrcValue.IsValid() {
				sc.debug("-> Skip: Source map value for key '%s' is nil/invalid interface.", sourceMapKey)
				usedMapKeys[sourceMapKey] = struct{}{} // Mark used even if skipped
				continue
			}
		}

		finalSrcValue := concreteSrcValue // Start with concrete source value

		// --- Apply Source-Based Skip Conditions ('omitempty' on dest tag, skipZero, skipEmpty) ---
		// These depend on the source value from the map.
		// Note: 'omitempty' tag on the *destination* struct field controls skipping based on source value.
		if _, omit := dfInfo.tagOpts["omitempty"]; omit && isEmptyValue(finalSrcValue) {
			sc.debug("-> Skip: Source value for key '%s' is empty and 'omitempty' tag set on dest field '%s'.", sourceMapKey, destFieldName)
			usedMapKeys[sourceMapKey] = struct{}{}
			continue
		}
		if sc.skipZeroValue && finalSrcValue.IsZero() {
			sc.debug("-> Skip: Source value for key '%s' is zero-valued and skipZeroValue=true.", sourceMapKey)
			usedMapKeys[sourceMapKey] = struct{}{}
			continue
		}
		if sc.skipEmptyValue && isEmptyValue(finalSrcValue) {
			sc.debug("-> Skip: Source value for key '%s' is empty and skipEmptyValue=true.", sourceMapKey)
			usedMapKeys[sourceMapKey] = struct{}{}
			continue
		}

		// --- Get Destination Field and Check Overwrite ---
		destField := sc.getSettableFieldValue(dest, &dfInfo)
		if !destField.IsValid() {
			sc.debug("-> Skip: Cannot get settable destination field '%s' value.", destFieldName)
			continue
		}

		// Check overwrite condition based on destination state
		if !sc.overwrite && destField.IsValid() && !isEmptyValue(destField) {
			sc.debug("-> Skip: Destination field '%s' is non-empty and overwrite=false.", destFieldName)
			continue
		}

		// --- Apply Formatter ---
		if formatter, ok := sc.formatters[destFieldName]; ok {
			if finalSrcValue.CanInterface() {
				// ... apply formatter, update finalSrcValue, check type compatibility ... (similar to copyStruct)
				sc.debug("-> Applying formatter to source map value for key '%s'", sourceMapKey)
				formattedInterface := formatter(finalSrcValue.Interface())
				if formattedInterface == nil {
					finalSrcValue = reflect.Zero(dfInfo.typ)
				} else {
					finalSrcValue = reflect.ValueOf(formattedInterface)
				}
				if finalSrcValue.IsValid() && finalSrcValue.Type() != dfInfo.typ {
					if finalSrcValue.Type().ConvertibleTo(dfInfo.typ) {
						finalSrcValue = finalSrcValue.Convert(dfInfo.typ)
					} else {
						sc.debug("-> Warning: Formatter for '%s' returned incompatible type %s (expected %s). Skipping field.", destFieldName, finalSrcValue.Type(), dfInfo.typ)
						continue
					}
				}
			} else {
				sc.debug("-> Warning: Cannot call Interface() on map value for key '%s'. Skipping formatter.", sourceMapKey)
			}
		}
		if !finalSrcValue.IsValid() {
			sc.debug("-> Skip: Source value became invalid after formatting for map key '%s'.", sourceMapKey)
			continue
		}

		// --- Recursive Copy ---
		sc.debug("-> Recursing copy: Dest Field: %s (%s) <- Map Key: '%s' (%s)", destFieldName, destField.Type(), sourceMapKey, finalSrcValue.Type())
		usedMapKeys[sourceMapKey] = struct{}{} // Mark key as used before recursing
		if err := sc.copyRecursive(destField, finalSrcValue, ctx); err != nil {
			fieldErr := fmt.Errorf("copying field '%s' <- map['%s']: %w", destFieldName, sourceMapKey, err)
			if sc.handlePotentialError(fieldErr, ctx) {
				continue
			}
			return fieldErr
		}
	}

	// --- Handle Unmapped Map Keys ---
	if unmappedMap.IsValid() {
		sc.debug("-> Processing unmapped map keys for struct field '%s'", unmappedFieldName)
		iter := src.MapRange()
		for iter.Next() {
			mapKey := iter.Key()
			mapKeyStr := mapKey.String() // Assumes string key
			if _, used := usedMapKeys[mapKeyStr]; !used {
				sc.debug(" -> Found unmapped map key: '%s'", mapKeyStr)
				srcMapVal := iter.Value()

				// Handle interface values from map
				concreteSrcVal := srcMapVal
				if srcMapVal.Kind() == reflect.Interface {
					concreteSrcVal = srcMapVal.Elem()
				}
				// Skip if nil/invalid interface element
				if !concreteSrcVal.IsValid() {
					sc.debug(" -> Skipping unmapped map key '%s', value is nil/invalid interface.", mapKeyStr)
					continue
				}

				// Skip based on global settings if configured
				if sc.skipZeroValue && concreteSrcVal.IsZero() {
					continue
				}
				if sc.skipEmptyValue && isEmptyValue(concreteSrcVal) {
					continue
				}

				// Copy the value into the unmapped map field
				destMapKey := reflect.ValueOf(mapKeyStr)
				destMapValue := reflect.New(unmappedValueType).Elem() // Holder for map value (e.g., interface{})

				sc.debug("  -> Recursing copy for unmapped: map['%s'] (%s) <- map['%s'] (%s)", mapKeyStr, destMapValue.Type(), mapKeyStr, concreteSrcVal.Type())
				if err := sc.copyRecursive(destMapValue, concreteSrcVal, ctx); err == nil {
					unmappedMap.SetMapIndex(destMapKey, destMapValue)
				} else {
					unmappedErr := fmt.Errorf("copying unmapped map key '%s' to map field '%s': %w", mapKeyStr, unmappedFieldName, err)
					if sc.handlePotentialError(unmappedErr, ctx) {
						continue
					}
					return unmappedErr
				}
			}
		}
	}

	return nil
}

func (sc *SmartCopier) copyStructToMap(dest reflect.Value, src reflect.Value, ctx *copyContext) error {
	sc.debug("Depth %d: copyStructToMap: Dest: %s <- Src: %s", ctx.depth, dest.Type(), src.Type())
	if dest.Kind() != reflect.Map {
		err := ErrDestNotMap
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}
	destType := dest.Type()
	if destType.Key().Kind() != reflect.String {
		err := fmt.Errorf("%w: destination map key type must be string, got %s", ErrMapKeyNotString, destType.Key().Kind())
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}
	if !dest.CanSet() {
		err := fmt.Errorf("cannot set destination map %s", destType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	// Initialize map if nil, or clear if overwrite=true
	if dest.IsNil() {
		sc.debug("-> Initializing destination map %s", destType)
		dest.Set(reflect.MakeMap(destType)) // Use MakeMap, size hint isn't easy here
	} else if sc.overwrite {
		// Clear existing map entries if overwriting
		sc.debug("-> Clearing destination map %s as overwrite=true", destType)
		// Iterate and delete keys - using MapRange is safer
		iter := dest.MapRange()
		keysToDelete := []reflect.Value{}
		for iter.Next() {
			keysToDelete = append(keysToDelete, iter.Key())
		}
		for _, k := range keysToDelete {
			dest.SetMapIndex(k, reflect.Value{}) // Set to zero value to delete
		}
		// Alternative: dest.Set(reflect.MakeMap(destType)) might be simpler if allowed
	}
	// If !overwrite and map exists, we add/update keys

	destValueType := destType.Elem()
	srcInfo := sc.getTypeInfo(src.Type())

	// --- Iterate Over Source Struct Fields ---
	for srcFieldName, sfInfo := range srcInfo.fields {
		sc.debug("-> Processing Source Field: '%s'", srcFieldName)

		// --- Check Export Status ---
		isSrcUnexported := sfInfo.structField.PkgPath != ""
		if isSrcUnexported { // Always skip unexported source fields when copying TO map
			sc.debug("-> Skip: Source field '%s' is unexported.", srcFieldName)
			continue
		}

		// --- Get Source Value ---
		srcField := src.FieldByIndex(sfInfo.index)
		if !srcField.IsValid() {
			sc.debug("-> Skip: Source field '%s' is invalid.", srcFieldName)
			continue
		}

		// --- Check Source Tags ('-', 'omitempty') and Skip Options ---
		tagOpts := sfInfo.tagOpts
		if _, isMinus := tagOpts["-"]; isMinus {
			sc.debug("-> Skip: Tag '-' on source field '%s'.", srcFieldName)
			continue
		}
		if _, omit := tagOpts["omitempty"]; omit && isEmptyValue(srcField) {
			sc.debug("-> Skip: Source value is empty and 'omitempty' tag set on source field '%s'.", srcFieldName)
			continue
		}
		if sc.skipZeroValue && srcField.IsZero() {
			sc.debug("-> Skip: Source field '%s' is zero-valued and skipZeroValue=true.", srcFieldName)
			continue
		}
		if sc.skipEmptyValue && isEmptyValue(srcField) {
			sc.debug("-> Skip: Source field '%s' is empty and skipEmptyValue=true.", srcFieldName)
			continue
		}

		// --- Determine Destination Map Key ---
		destMapKeyName := srcFieldName // Default: use source field name
		// Check source tag `mm:"DestMapKey"`
		if tagName, ok := tagOpts["name"]; ok {
			destMapKeyName = tagName
			sc.debug("-> Dest key mapped via source tag: '%s'", destMapKeyName)
		}
		// Check global config (overrides tag)
		if mappedDst, ok := sc.fieldMappings[srcFieldName]; ok {
			destMapKeyName = mappedDst
			sc.debug("-> Dest key mapped via config: '%s'", destMapKeyName)
		}
		destMapKey := reflect.ValueOf(destMapKeyName)

		// --- Check Overwrite Condition for Map Key ---
		// If not overwriting, check if the key already exists in the destination map
		if !sc.overwrite {
			existingVal := dest.MapIndex(destMapKey)
			if existingVal.IsValid() && !isEmptyValue(existingVal) { // Check if existing value is non-empty
				sc.debug("-> Skip: Destination map key '%s' exists and is non-empty, and overwrite=false.", destMapKeyName)
				continue
			}
		}

		// --- Recursive Copy ---
		destMapValue := reflect.New(destValueType).Elem() // Create holder for map value
		sc.debug("-> Recursing copy: map['%s'] (%s) <- %s (%s)", destMapKeyName, destValueType, srcFieldName, srcField.Type())
		if err := sc.copyRecursive(destMapValue, srcField, ctx); err == nil {
			// Set value in map only if recursive copy succeeded
			dest.SetMapIndex(destMapKey, destMapValue)
		} else {
			fieldErr := fmt.Errorf("copying to map key '%s' <- field '%s': %w", destMapKeyName, srcFieldName, err)
			if sc.handlePotentialError(fieldErr, ctx) {
				continue // Collect and continue
			}
			return fieldErr // Propagate
		}
	}

	return nil
}

func (sc *SmartCopier) copySlice(dest, src reflect.Value, ctx *copyContext) error {
	destType := dest.Type()
	srcType := src.Type()
	srcLen := src.Len()
	sc.debug("Depth %d: copySlice: Dest: %s <- Src: %s (len %d)", ctx.depth, destType, srcType, srcLen)

	if !dest.CanSet() {
		err := fmt.Errorf("cannot set destination slice %s", destType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	// Handle destination slice allocation/resizing based on overwrite flag
	if dest.IsNil() {
		// If nil, always allocate regardless of overwrite flag
		sc.debug("-> Initializing destination slice %s with len/cap %d", destType, srcLen)
		newSlice := reflect.MakeSlice(destType, srcLen, srcLen)
		dest.Set(newSlice)
	} else if sc.overwrite {
		// If overwriting and dest exists, ensure capacity and length match source
		if dest.Cap() < srcLen {
			sc.debug("-> Reallocating destination slice %s to len/cap %d (overwrite=true)", destType, srcLen)
			newSlice := reflect.MakeSlice(destType, srcLen, srcLen)
			dest.Set(newSlice) // Replace entirely
		} else if dest.Len() != srcLen {
			sc.debug("-> Resizing destination slice %s length to %d (overwrite=true)", destType, srcLen)
			dest.SetLen(srcLen) // Adjust length if capacity is sufficient
		}
		// If cap >= srcLen and len == srcLen, do nothing to the slice header itself
	} else {
		// If !overwrite and dest is not nil, do nothing (keep existing slice)
		sc.debug("-> Skipping slice copy as !overwrite and destination slice %s exists", destType)
		return nil
	}

	// --- Perform Element Copy ---
	destElemType := destType.Elem()
	srcElemType := srcType.Elem()

	// Optimization: Use reflect.Copy for primitive/simple types if assignable
	canReflectCopy := false
	if srcElemType.AssignableTo(destElemType) {
		// Check if element type is suitable for block copy
		if isMemCopySuitable(destElemType) {
			canReflectCopy = true
		}
		// Alternative: check specific kinds safe for reflect.Copy
		// switch destElemType.Kind() {
		// case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		// 	reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		// 	reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		// 	canReflectCopy = true
		// }
	}

	if canReflectCopy {
		sc.debug("-> Using reflect.Copy for slice %s", destType)
		reflect.Copy(dest, src) // Fast path for simple slices
		return nil
	}

	// Fallback to element-wise recursive copy
	sc.debug("-> Performing element-wise recursive copy for slice %s", destType)
	// Iterate up to the length of the *destination* slice (which was set to srcLen if needed)
	finalLen := dest.Len()
	for i := 0; i < finalLen; i++ {
		if i >= src.Len() {
			break
		} // Safety break if src is somehow shorter

		destElem := dest.Index(i)
		srcElem := src.Index(i)

		if !destElem.CanSet() {
			// Should not happen if slice was created correctly, but check defensively
			err := fmt.Errorf("cannot set destination slice element at index %d for %s", i, destType)
			if sc.handlePotentialError(err, ctx) {
				continue
			}
			return err
		}

		sc.debug(" --> Recursing copy slice index %d: Dest Elem: %s <- Src Elem: %s", i, destElem.Type(), srcElem.Type())
		if err := sc.copyRecursive(destElem, srcElem, ctx); err != nil {
			elemErr := fmt.Errorf("copying slice index %d: %w", i, err)
			if sc.handlePotentialError(elemErr, ctx) {
				continue
			} // Collect and continue
			return elemErr // Propagate
		}
	}
	return nil
}

func (sc *SmartCopier) copyMap(dest, src reflect.Value, ctx *copyContext) error {
	destType := dest.Type()
	srcType := src.Type()
	srcLen := src.Len()
	sc.debug("Depth %d: copyMap: Dest: %s <- Src: %s (len %d)", ctx.depth, destType, srcType, srcLen)

	if !dest.CanSet() {
		err := fmt.Errorf("cannot set destination map %s", destType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	// Handle destination map allocation/clearing
	if dest.IsNil() {
		sc.debug("-> Initializing destination map %s with size hint %d", destType, srcLen)
		dest.Set(reflect.MakeMapWithSize(destType, srcLen))
	} else if sc.overwrite {
		sc.debug("-> Clearing destination map %s as overwrite=true", destType)
		// Clear existing entries efficiently
		iter := dest.MapRange()
		keysToDelete := []reflect.Value{}
		for iter.Next() {
			keysToDelete = append(keysToDelete, iter.Key())
		}
		for _, k := range keysToDelete {
			dest.SetMapIndex(k, reflect.Value{}) // Zero value deletes the key
		}
	}
	// If !overwrite and dest exists, add/update keys below

	iter := src.MapRange()
	destKeyType := destType.Key()
	destElemType := destType.Elem()

	for iter.Next() {
		srcKey := iter.Key()
		srcValue := iter.Value()

		// Create settable holders for destination key and value
		newDestKey := reflect.New(destKeyType).Elem()
		newDestValue := reflect.New(destElemType).Elem()

		// Copy source key to destination key holder
		sc.debug("-> Copying map key: Dest Key Holder: %s <- Src Key: %s", newDestKey.Type(), srcKey.Type())
		if err := sc.copyRecursive(newDestKey, srcKey, ctx); err != nil {
			keyErr := fmt.Errorf("copying map key (%+v): %w", srcKey.Interface(), err)
			if sc.handlePotentialError(keyErr, ctx) {
				continue
			} // Skip this entry if key copy fails
			return keyErr // Propagate if not collecting
		}
		// Check if key is valid after copy
		if !newDestKey.IsValid() {
			sc.debug("-> Skipping map entry, key became invalid after copy: %+v", srcKey.Interface())
			continue
		}

		// Check overwrite condition for the specific key if !overwrite
		if !sc.overwrite {
			existingVal := dest.MapIndex(newDestKey)
			if existingVal.IsValid() && !isEmptyValue(existingVal) {
				sc.debug("-> Skipping map key '%v' as it exists and is non-empty, and overwrite=false.", newDestKey.Interface())
				continue
			}
		}

		// Copy source value to destination value holder
		sc.debug("-> Copying map value for key [%+v]: Dest Val Holder: %s <- Src Val: %s", newDestKey.Interface(), newDestValue.Type(), srcValue.Type())
		err := sc.copyRecursive(newDestValue, srcValue, ctx)
		if err != nil {
			valErr := fmt.Errorf("copying map value for key [%+v]: %w", newDestKey.Interface(), err)
			if sc.handlePotentialError(valErr, ctx) {
				continue
			} // Skip this entry if value copy fails
			return valErr // Propagate if not collecting
		}

		// Set the copied key and value in the destination map
		sc.debug("-> Setting map index: Key: %v (%s), Value: %v (%s)", newDestKey.Interface(), newDestKey.Type(), newDestValue.Interface(), newDestValue.Type())
		dest.SetMapIndex(newDestKey, newDestValue)
	}
	return nil
}

func (sc *SmartCopier) copyInterface(dest, src reflect.Value, ctx *copyContext) error {
	destType := dest.Type() // The interface type itself
	sc.debug("Depth %d: copyInterface: Dest: %s <- Src: %s (%s)", ctx.depth, destType, src.Type(), src.Kind())

	if !dest.CanSet() {
		err := fmt.Errorf("cannot set destination interface %s", destType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	// Handle source being nil interface or holding nil pointer
	if src.Kind() == reflect.Interface && src.IsNil() {
		sc.debug("-> Source is nil interface.")
		if sc.overwrite && !dest.IsNil() { // Only zero dest if overwriting and dest currently holds something
			sc.debug("-> Zeroing destination interface %s.", destType)
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	// Get the concrete value from the source interface/value
	srcConcrete := src
	if src.Kind() == reflect.Interface {
		srcConcrete = src.Elem()
	}

	// Handle invalid concrete value (e.g., interface holding typed nil pointer)
	if !srcConcrete.IsValid() {
		sc.debug("-> Source concrete value is invalid (e.g., typed nil).")
		if sc.overwrite && !dest.IsNil() {
			sc.debug("-> Zeroing destination interface %s.", destType)
			dest.Set(reflect.Zero(destType))
		}
		return nil
	}

	srcConcreteType := srcConcrete.Type()
	sc.debug("-> Source concrete type: %s", srcConcreteType)

	// Create a new value holder of the *source* concrete type.
	// The recursive call below MUST handle the type transformation if necessary
	// (e.g., SimpleSrc -> SimpleDest) based on the destination *field* type if known,
	// or potentially based on registered converters.
	newDestConcreteHolder := reflect.New(srcConcreteType).Elem()

	sc.debug("-> Recursively copying concrete source value %s into holder %s", srcConcreteType, newDestConcreteHolder.Type())
	// Crucially, we pass the *holder* as the destination to copyRecursive.
	// If the source was SimpleSrc and the destination context implies SimpleDest,
	// copyRecursive (specifically copyStruct) should handle the field mapping.
	err := sc.copyRecursive(newDestConcreteHolder, srcConcrete, ctx)
	if err != nil {
		concErr := fmt.Errorf("copying concrete value (%s) for interface %s: %w", srcConcreteType, destType, err)
		if sc.handlePotentialError(concErr, ctx) {
			return nil
		}
		return concErr
	}

	// Now, assign the (potentially transformed) value from the holder to the destination interface.
	finalValueToAssign := newDestConcreteHolder
	if !finalValueToAssign.IsValid() {
		// If recursive copy resulted in invalid value (e.g., nil struct), assign zero interface value
		sc.debug("-> Recursive copy resulted in invalid value, assigning zero value to interface %s", destType)
		dest.Set(reflect.Zero(destType))
		return nil
	}

	// Check if the final value (or a pointer to it) is assignable to the destination interface type.
	if finalValueToAssign.Type().AssignableTo(destType) {
		sc.debug("-> Assigning concrete value of type %s to interface %s", finalValueToAssign.Type(), destType)
		dest.Set(finalValueToAssign)
	} else if finalValueToAssign.CanAddr() && finalValueToAssign.Addr().Type().AssignableTo(destType) {
		// Check if a pointer to the result is assignable (very common for interfaces)
		sc.debug("-> Assigning pointer to concrete value (*%s) to interface %s", finalValueToAssign.Type(), destType)
		dest.Set(finalValueToAssign.Addr())
	} else {
		// Conversion as a last resort? Usually assignment works for interfaces.
		converted, convErr := sc.tryConvert(finalValueToAssign, destType)
		if convErr == nil && converted.IsValid() {
			sc.debug("-> Assigning converted value of type %s to interface %s", converted.Type(), destType)
			dest.Set(converted)
		} else {
			// If still not assignable/convertible, it's an error.
			errMsg := fmt.Sprintf("copied concrete type %s (or pointer) not assignable/convertible to destination interface %s", finalValueToAssign.Type(), destType)
			err := fmt.Errorf("%w: %s", ErrTypeMismatch, errMsg)
			if sc.handlePotentialError(err, ctx) {
				return nil
			}
			return err
		}
	}

	return nil
}

func (sc *SmartCopier) copyArray(dest, src reflect.Value, ctx *copyContext) error {
	srcLen, destLen := src.Len(), dest.Len()
	destType := dest.Type()
	srcType := src.Type()
	sc.debug("Depth %d: copyArray: Dest: %s[%d] <- Src: %s[%d]", ctx.depth, destType, destLen, srcType, srcLen)

	if destLen != srcLen {
		err := fmt.Errorf("%w: array length mismatch (dest len %d != src len %d) for types %s and %s", ErrTypeMismatch, destLen, srcLen, destType, srcType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}
	if !dest.CanSet() {
		err := fmt.Errorf("cannot set destination array %s", destType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	// Array copy respects overwrite flag - if false and dest has content, skip.
	// Use IsZero() to check if the array is uninitialized/zeroed.
	if !sc.overwrite && dest.IsValid() && !dest.IsZero() {
		sc.debug("-> Skipping array copy as !overwrite and destination array %s is non-zero", destType)
		return nil
	}

	// --- Perform Element Copy ---
	destElemType := destType.Elem()
	srcElemType := srcType.Elem()

	// Optimization: Use reflect.Copy if elements are simple and assignable
	canReflectCopy := false
	if srcElemType.AssignableTo(destElemType) && isMemCopySuitable(destElemType) {
		canReflectCopy = true
	}

	if canReflectCopy {
		sc.debug("-> Using reflect.Copy for array %s", destType)
		reflect.Copy(dest, src) // Fast path for simple arrays
		return nil
	}

	// Fallback to element-wise recursive copy
	sc.debug("-> Performing element-wise recursive copy for array %s", destType)
	for i := 0; i < srcLen; i++ { // Iterate up to shared length
		destElem := dest.Index(i)
		srcElem := src.Index(i)

		if !destElem.CanSet() {
			err := fmt.Errorf("cannot set destination array element at index %d for %s", i, destType)
			if sc.handlePotentialError(err, ctx) {
				continue
			}
			return err
		}

		sc.debug(" --> Recursing copy array index %d: Dest Elem: %s <- Src Elem: %s", i, destElem.Type(), srcElem.Type())
		if err := sc.copyRecursive(destElem, srcElem, ctx); err != nil {
			elemErr := fmt.Errorf("copying array index %d: %w", i, err)
			if sc.handlePotentialError(elemErr, ctx) {
				continue
			} // Collect and continue
			return elemErr // Propagate
		}
	}
	return nil
}

func (sc *SmartCopier) tryConvert(src reflect.Value, destType reflect.Type) (reflect.Value, error) {
	srcType := src.Type()

	// 1. Check Custom Converters
	if destConvMap, ok := sc.typeConverters[srcType]; ok {
		if converter, ok := destConvMap[destType]; ok {
			sc.debug("-> Using custom converter: %s -> %s", srcType, destType)
			converted, err := converter(src)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("custom converter %s -> %s failed: %w", srcType, destType, err)
			}
			// Handle nil result from converter carefully
			if !converted.IsValid() {
				// If dest is a nillable type, return its zero value (nil)
				k := destType.Kind()
				if k == reflect.Ptr || k == reflect.Map || k == reflect.Slice || k == reflect.Interface || k == reflect.Chan || k == reflect.Func {
					return reflect.Zero(destType), nil
				}
				// Otherwise, nil is invalid for non-nillable types
				return reflect.Value{}, fmt.Errorf("custom converter %s -> %s returned nil for non-nillable type %s", srcType, destType, destType)
			}
			// Check if converter returned the exact type or assignable/convertible
			if converted.Type() == destType {
				return converted, nil
			}
			if converted.Type().AssignableTo(destType) {
				sc.debug(" -> Custom converter returned assignable type %s, using for %s", converted.Type(), destType)
				return converted, nil
			}
			if converted.Type().ConvertibleTo(destType) {
				sc.debug(" -> Custom converter returned convertible type %s, converting to %s", converted.Type(), destType)
				return converted.Convert(destType), nil
			}
			return reflect.Value{}, fmt.Errorf("custom converter %s -> %s returned incompatible type %s", srcType, destType, converted.Type())
		}
	}

	// 2. Check Go's Built-in Conversion
	if srcType.ConvertibleTo(destType) {
		sc.debug("-> Using Go's built-in conversion: %s -> %s", srcType, destType)
		return src.Convert(destType), nil
	}

	// 3. Handle Interface Source
	// If source is interface, try converting its concrete element
	if src.Kind() == reflect.Interface && !src.IsNil() {
		srcElem := src.Elem()
		if srcElem.IsValid() {
			sc.debug("-> Retrying conversion from concrete type %s (within interface) to %s", srcElem.Type(), destType)
			// Recursive call to tryConvert with the concrete element
			return sc.tryConvert(srcElem, destType)
		}
	}

	// 4. Handle Pointer Source to Pointer Destination
	// If src is *T1 and dest is *T2, try converting T1 to T2 and wrap in *T2
	if src.Kind() == reflect.Ptr && !src.IsNil() && destType.Kind() == reflect.Ptr {
		srcElem := src.Elem()
		if srcElem.IsValid() {
			destElemType := destType.Elem()
			sc.debug("-> Attempting pointer element conversion: %s -> %s", srcElem.Type(), destElemType)
			convertedElem, err := sc.tryConvert(srcElem, destElemType) // Recursive call for element
			if err == nil && convertedElem.IsValid() {
				// Create new pointer of destination type and set its element
				newDestPtr := reflect.New(destElemType)
				newDestPtr.Elem().Set(convertedElem)
				sc.debug(" -> Pointer element conversion successful, returning new %s", newDestPtr.Type())
				return newDestPtr, nil
			}
			sc.debug(" -> Pointer element conversion failed: %v", err)
		}
	}

	// 5. No suitable conversion found
	sc.debug("-> No converter found for %s -> %s", srcType, destType)
	return reflect.Value{}, fmt.Errorf("%w: from %s to %s", ErrNoConverter, srcType, destType)
}

func (sc *SmartCopier) debug(format string, args ...interface{}) {
	if sc.debugMode {
		// Add indentation based on context depth for readability
		indent := strings.Repeat("  ", ctxDepth())
		log.Printf("[mm debug] "+indent+format, args...)
	}
}

// Helper to get current depth from context (if available)
// This is a placeholder - context needs to be accessible or passed differently for this to work.
// For simplicity, we won't use dynamic indentation in this version.
func ctxDepth() int {
	// In a real implementation, get depth from context passed around.
	// For now, return 0.
	return 0
}
