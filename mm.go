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
	ErrUnexportedField     = errors.New("cannot set unexported field") // Note: This might be less relevant if copyUnexported+unsafe is used.
	ErrNoConverter         = errors.New("no suitable type converter found")
	ErrCycleDetected       = errors.New("cycle detected but cannot assign destination")
	ErrUnmappedFieldType   = errors.New("field tagged with 'unmapped' must be a map[string]interface{} or map[string]any")
	ErrMapKeyNotString     = errors.New("source/destination map key type must be string when copying between map and struct")
	ErrDestNotMap          = errors.New("destination type must be a map when copying from struct")
	ErrMapValueNotSettable = errors.New("destination map value type not settable")
	ErrReadUnexportedSrc   = errors.New("reading unexported source field is not supported reliably")
)

// --- Core Structs & Types ---

type SmartCopier struct {
	tagName           string
	fieldMappings     map[string]string
	skipFields        map[string]struct{}
	formatters        map[string]func(interface{}) interface{}
	typeConverters    map[reflect.Type]map[reflect.Type]func(reflect.Value) (reflect.Value, error)
	maxDepth          int
	overwrite         bool
	skipZeroValue     bool
	skipEmptyValue    bool
	skipNilSrc        bool
	useUnsafe         bool
	enableCycleDetect bool
	copyUnexported    bool // Requires useUnsafe=true
	collectErrors     bool
	panicOnError      bool
	typeInfoCache     sync.Map
	bufferPool        sync.Pool // TODO: Use buffer pool for memcopy if deemed beneficial. Currently unused.
	debugMode         bool
}

// visitKey is used to track visited pointers during recursive copying
// to detect cycles.
type visitKey struct {
	ptr uintptr
	typ reflect.Type
}

// mmContext holds the state for a single copy operation. Renamed from copyContext.
type mmContext struct {
	depth   int
	visited map[visitKey]reflect.Value // Stores the destination value corresponding to a source pointer
	errors  []error
}

// typeInfo caches reflection information about a type.
type typeInfo struct {
	fields        map[string]fieldInfo // Field name -> info
	nameToField   map[string]fieldInfo
	unmappedField *fieldInfo // Info for the field tagged 'unmapped', if any
	size          uintptr    // reflect.Type.Size()
	canMemCopy    bool       // Can we potentially use memcopy for this type?
	alignment     uintptr    // reflect.Type.Align()
	containsPtrs  bool       // Does this type (or its elements/fields) contain pointers?
	isStruct      bool
	isSlice       bool
	isMap         bool
	isPtr         bool
	isInterface   bool
}

// fieldInfo caches reflection information about a struct field.
type fieldInfo struct {
	name        string
	structField reflect.StructField
	index       []int   // For FieldByIndex
	offset      uintptr // For unsafe access
	typ         reflect.Type
	canSet      bool              // Based on export status and copyUnexported/useUnsafe flags
	tagOpts     map[string]string // Parsed options from the struct tag
}

// --- Tag Parsing & Helpers ---

// parseTagOptions splits a struct tag string into options.
// Example: "targetName,omitempty,-" -> map["name":"targetName", "omitempty":"", "-":""].
func parseTagOptions(tag string) map[string]string {
	opts := make(map[string]string)
	parts := strings.Split(tag, ",")
	if len(parts) > 0 {
		trimmedName := strings.TrimSpace(parts[0])
		if trimmedName == "-" {
			opts["-"] = "" // Mark as skipped
		} else if trimmedName != "" {
			opts["name"] = trimmedName // Explicit name mapping
		}
		// Handle other options like omitempty, unmapped etc.
		for _, opt := range parts[1:] {
			trimmedOpt := strings.TrimSpace(opt)
			if trimmedOpt != "" {
				opts[trimmedOpt] = "" // Presence of the option is enough
			}
		}
	}
	// If skipped, remove name mapping if present
	if _, ok := opts["-"]; ok {
		delete(opts, "name")
	}
	return opts
}

// isEmptyValue checks if a reflect.Value represents an empty value (zero, nil, empty collection).
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
	case reflect.Complex64, reflect.Complex128:
		return v.Complex() == 0
	case reflect.Interface, reflect.Ptr, reflect.Chan, reflect.Func:
		return v.IsNil()
	}
	// For other types like struct, check the IsZero method
	return v.IsZero()
}

// --- Initialization ---

func New() *SmartCopier {
	return &SmartCopier{
		tagName:           "mm", // Default struct tag name
		fieldMappings:     make(map[string]string),
		skipFields:        make(map[string]struct{}),
		formatters:        make(map[string]func(interface{}) interface{}),
		typeConverters:    make(map[reflect.Type]map[reflect.Type]func(reflect.Value) (reflect.Value, error)),
		maxDepth:          -1, // No depth limit by default
		overwrite:         true,
		useUnsafe:         true, // Allow unsafe operations by default (for unexported fields)
		enableCycleDetect: true,
		copyUnexported:    false, // Don't copy unexported fields by default
		collectErrors:     false,
		panicOnError:      false,
		skipNilSrc:        false,
		skipZeroValue:     false,
		skipEmptyValue:    false,
		debugMode:         false, // Disable debug logging by default
		bufferPool: sync.Pool{
			New: func() interface{} {
				// Allocate a reasonably sized buffer for potential memcopy operations
				buf := make([]byte, 1024)
				return &buf
			},
		},
	}
}

// --- Configuration Methods ---

func (sc *SmartCopier) WithTag(tag string) *SmartCopier {
	sc.tagName = tag
	sc.typeInfoCache = sync.Map{} // Clear cache as tags affect field info
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

// WithOverwrite configures whether existing non-empty/non-zero values
// in the destination should be overwritten. Default is true.
// Setting to false enables merge-like behavior (see also MergeTo).
func (sc *SmartCopier) WithOverwrite(overwrite bool) *SmartCopier {
	sc.overwrite = overwrite
	return sc
}

// WithSkipZero configures whether source fields with zero values
// (according to reflect.Value.IsZero) should be skipped. Default is false.
func (sc *SmartCopier) WithSkipZero(skip bool) *SmartCopier {
	sc.skipZeroValue = skip
	return sc
}

// WithSkipEmpty configures whether source fields with empty values
// (zero, nil, or empty collections/strings) should be skipped. Default is false.
// See isEmptyValue for the definition of empty.
func (sc *SmartCopier) WithSkipEmpty(skip bool) *SmartCopier {
	sc.skipEmptyValue = skip
	return sc
}

// WithSkipNil configures whether a nil source value (or nil pointer/interface)
// should be skipped entirely (leaving the destination untouched). Default is false.
// If false, a nil source might zero the destination field if overwrite is true.
func (sc *SmartCopier) WithSkipNil(skip bool) *SmartCopier {
	sc.skipNilSrc = skip
	return sc
}

// WithUnsafe enables or disables the use of the unsafe package.
// Required for copying unexported fields. Default is true.
func (sc *SmartCopier) WithUnsafe(enable bool) *SmartCopier {
	sc.useUnsafe = enable
	sc.typeInfoCache = sync.Map{} // Clear cache as unsafe affects field accessibility
	return sc
}

// WithCycleDetection enables or disables cycle detection during recursive copying.
// Enabled by default. Disabling might improve performance slightly for known
// acyclic structures but risks infinite loops with cyclic data.
func (sc *SmartCopier) WithCycleDetection(enable bool) *SmartCopier {
	sc.enableCycleDetect = enable
	return sc
}

// WithUnexported enables copying of unexported fields.
// Requires WithUnsafe(true). Default is false.
// Note: Reading unexported fields from the source might still be problematic
// and isn't guaranteed to work reliably across Go versions or architectures.
// Setting unexported fields in the destination is generally more reliable with unsafe.
func (sc *SmartCopier) WithUnexported(enable bool) *SmartCopier {
	if enable && !sc.useUnsafe {
		log.Println("[mm copier warning] WithUnexported(true) requires WithUnsafe(true). Unexported fields will not be copied.")
		sc.copyUnexported = false
	} else {
		sc.copyUnexported = enable
	}
	sc.typeInfoCache = sync.Map{} // Clear cache as this affects field info
	return sc
}

// WithTypeConverter registers a custom function to convert values from src type to dst type.
func (sc *SmartCopier) WithTypeConverter(src, dst reflect.Type, converter func(reflect.Value) (reflect.Value, error)) *SmartCopier {
	if sc.typeConverters[src] == nil {
		sc.typeConverters[src] = make(map[reflect.Type]func(reflect.Value) (reflect.Value, error))
	}
	sc.typeConverters[src][dst] = converter
	return sc
}

// WithErrors enables collecting all errors encountered during a copy operation
// instead of stopping at the first one. The collected errors are joined
// into a single error returned at the end. Default is false.
func (sc *SmartCopier) WithErrors(collect bool) *SmartCopier {
	sc.collectErrors = collect
	return sc
}

// WithPanic enables panicking immediately when any error occurs during copying.
// Default is false. If true, overrides WithErrors(true).
func (sc *SmartCopier) WithPanic(panicOnError bool) *SmartCopier {
	sc.panicOnError = panicOnError
	return sc
}

// EnableDebug enables or disables verbose debug logging. Default is false.
func (sc *SmartCopier) EnableDebug(enable bool) *SmartCopier {
	sc.debugMode = enable
	return sc
}

// --- Error Handling Helpers ---

// handlePotentialError decides how to handle an error based on copier settings.
// Returns true if the error was handled (collected) and copying should continue,
// false if the error should be returned immediately (or panicked on).
func (sc *SmartCopier) handlePotentialError(err error, ctx *mmContext) bool {
	if err == nil {
		return false // No error to handle
	}

	// If panic mode is on, always panic immediately
	if sc.panicOnError {
		panic(fmt.Sprintf("mm copy panic: %v", err))
		// Note: panic() doesn't return, but Go compiler requires a return path.
		// In practice, the panic prevents this return.
		return false
	}

	// If collect errors mode is on, add to context and continue
	if sc.collectErrors {
		sc.debug("Collecting error: %v", err)
		ctx.errors = append(ctx.errors, err)
		return true // Error handled by collecting, continue copying
	}

	// Default: return the error immediately
	return false // Error not handled here, should be returned by caller
}

// finalizeResult processes the result of a copy operation, considering collected errors.
func (sc *SmartCopier) finalizeResult(returnedErr error, ctx *mmContext) error {
	// If errors were collected, join them.
	if sc.collectErrors && len(ctx.errors) > 0 {
		// Even if returnedErr is not nil, collected errors take precedence
		finalErr := errors.Join(ctx.errors...)
		// Panic here if configured and errors were collected
		if sc.panicOnError {
			panic(fmt.Sprintf("mm copy failed with %d error(s): %v", len(ctx.errors), finalErr))
		}
		return finalErr
	}

	// If no errors were collected, handle the potentially returned single error.
	if returnedErr != nil {
		// Panic here if configured and a single error occurred without collection.
		if sc.panicOnError {
			panic(fmt.Sprintf("mm copy failed: %v", returnedErr))
		}
		return returnedErr
	}

	// No errors encountered or collected.
	return nil
}

// --- Public Copy Methods ---

// Copy creates a new destination value of type T and copies data from src into it.
// T must be the desired concrete type for the destination.
// Returns the newly created destination value and any error encountered.
func Copy[T any](sc *SmartCopier, src interface{}) (T, error) {
	var dest T
	var zero T // Zero value of T to return on error or skip

	destType := reflect.TypeOf(dest)
	if destType == nil {
		// This can happen if T is an interface type like `any` or `interface{}`
		// We need a concrete type to create a new value.
		// Trying to get type from src if possible.
		if src != nil {
			srcVal := reflect.ValueOf(src)
			if srcVal.Kind() == reflect.Ptr && !srcVal.IsNil() {
				srcVal = srcVal.Elem()
			}
			if srcVal.IsValid() && srcVal.Type() != nil {
				destType = srcVal.Type() // Use source type as fallback? Risky.
				sc.debug("Warning: Copy[T] called with interface type T. Attempting to use source type %s for destination.", destType)
				// We still can't instantiate `dest` directly with this type easily.
				// This scenario might need rethinking or clearer constraints on T.
				// For now, let's return an error if the initial destType is nil.
				return zero, fmt.Errorf("Copy[T] requires a non-interface type for T, got %T", dest)

			}
		}
		// If src is also nil or has no type, we cannot proceed.
		return zero, fmt.Errorf("Copy[T] requires a non-interface type for T, cannot determine type")
	}

	// Handle nil source based on skipNilSrc flag *before* returning ErrSourceIsNil
	isSrcNil := false
	if src == nil {
		isSrcNil = true
	} else {
		srcValCheck := reflect.ValueOf(src)
		if srcValCheck.Kind() == reflect.Ptr || srcValCheck.Kind() == reflect.Interface || srcValCheck.Kind() == reflect.Map || srcValCheck.Kind() == reflect.Slice || srcValCheck.Kind() == reflect.Chan || srcValCheck.Kind() == reflect.Func {
			if srcValCheck.IsNil() {
				isSrcNil = true
			}
		}
	}

	if isSrcNil {
		if sc.skipNilSrc {
			sc.debug("Copy: Source is nil and skipNilSrc=true, returning zero value for %T", dest)
			return zero, nil
		}
		sc.debug("Copy: Source is nil and skipNilSrc=false, returning error.")
		// Note: finalizerResult handles panicOnError if needed
		return zero, sc.finalizeResult(ErrSourceIsNil, &mmContext{})
	}

	// Create a new settable value of the destination type T
	// Use reflect.New to get a pointer, then Elem() to get the settable value.
	destVal := reflect.New(destType).Elem()
	srcVal := reflect.ValueOf(src)

	// Prepare context for recursive copy
	ctx := &mmContext{
		depth:   0,
		visited: make(map[visitKey]reflect.Value),
		errors:  make([]error, 0),
	}

	sc.debug("Copy: Starting copy from %T (%s) to new %s", src, srcVal.Type(), destVal.Type())
	err := sc.copyRecursive(destVal, srcVal, ctx)

	// Finalize result (handle collected errors / panic)
	finalErr := sc.finalizeResult(err, ctx)
	if finalErr != nil {
		return zero, finalErr
	}

	// Ensure the result is valid and assignable back to T
	if !destVal.IsValid() {
		// Should not happen if copyRecursive succeeded without error, but check defensively.
		return zero, fmt.Errorf("internal error: copy result is invalid for type %s", destType)
	}

	// The destVal is guaranteed to be of type T because we created it with reflect.New(destType).Elem()
	finalDest, ok := destVal.Interface().(T)
	if !ok {
		// This should be impossible if destType was derived from T correctly.
		return zero, fmt.Errorf("internal error: cannot assert copy result type %s to target type %T", destVal.Type(), dest)
	}

	return finalDest, nil
}

// CopyTo copies data from src to the value pointed to by dest.
// dest must be a non-nil pointer to the destination value.
func (sc *SmartCopier) CopyTo(dest, src interface{}) error {
	sc.debug("CopyTo called with dest=%T, src=%T", dest, src)
	if dest == nil {
		sc.debug("Destination is nil")
		err := ErrDestinationIsNil
		// finalizeResult will handle panicOnError if needed
		return sc.finalizeResult(err, &mmContext{})
	}

	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr {
		sc.debug("Destination is not a pointer: %v Kind=%s", destVal, destVal.Kind())
		err := ErrDestNotPointer
		return sc.finalizeResult(err, &mmContext{})
	}
	if destVal.IsNil() {
		sc.debug("Destination is nil pointer")
		// A nil pointer destination is not settable.
		err := ErrDestNotPointer // Or perhaps a more specific error like "destination pointer is nil"
		return sc.finalizeResult(err, &mmContext{})
	}

	destElem := destVal.Elem()
	if !destElem.CanSet() {
		// This usually happens if dest points to an unexported field in another package,
		// or if it's obtained in a way that makes it read-only.
		err := fmt.Errorf("destination element (type %s) is not settable", destElem.Type())
		return sc.finalizeResult(err, &mmContext{})
	}

	// Prepare context for recursive copy
	ctx := &mmContext{
		depth:   0,
		visited: make(map[visitKey]reflect.Value),
		errors:  make([]error, 0),
	}

	err := sc.copyToInternal(destElem, src, ctx)
	return sc.finalizeResult(err, ctx)
}

// copyToInternal is the internal implementation for CopyTo, operating on the settable destination element.
func (sc *SmartCopier) copyToInternal(destElem reflect.Value, src interface{}, ctx *mmContext) error {
	sc.debug("copyToInternal called with destElem=%s (%s), src=%T", destElem.Type(), destElem.Kind(), src)

	// Handle nil source value
	if src == nil {
		if sc.skipNilSrc {
			sc.debug("Source is nil, skipNilSrc=true, skipping.")
			return nil
		}
		// Source is nil, skipNilSrc is false. Behavior depends on overwrite.
		if sc.overwrite && destElem.IsValid() && destElem.CanSet() {
			sc.debug("Source is nil, skipNilSrc=false, overwrite=true. Zeroing destination %s.", destElem.Type())
			destElem.Set(reflect.Zero(destElem.Type()))
		} else {
			sc.debug("Source is nil, skipNilSrc=false, overwrite=false. Destination %s remains unchanged.", destElem.Type())
		}
		return nil // Successfully handled nil source (by skipping or zeroing)
	}

	srcVal := reflect.ValueOf(src)

	// Handle nil pointer/interface source
	if srcVal.Kind() == reflect.Ptr || srcVal.Kind() == reflect.Interface || srcVal.Kind() == reflect.Map || srcVal.Kind() == reflect.Slice || srcVal.Kind() == reflect.Chan || srcVal.Kind() == reflect.Func {
		if srcVal.IsNil() {
			if sc.skipNilSrc {
				sc.debug("Source is nil pointer/interface/slice/map, skipNilSrc=true, skipping.")
				return nil
			}
			if sc.overwrite && destElem.IsValid() && destElem.CanSet() {
				sc.debug("Source is nil pointer/interface/slice/map, skipNilSrc=false, overwrite=true. Zeroing destination %s.", destElem.Type())
				destElem.Set(reflect.Zero(destElem.Type()))
			} else {
				sc.debug("Source is nil pointer/interface/slice/map, skipNilSrc=false, overwrite=false. Destination %s remains unchanged.", destElem.Type())
			}
			return nil // Successfully handled nil source
		}
	}

	// Source is not nil, proceed with recursive copy
	sc.debug("Starting recursive copy from %s to %s", srcVal.Type(), destElem.Type())
	return sc.copyRecursive(destElem, srcVal, ctx)
}

// MustCopyTo is like CopyTo but panics if any error occurs.
func (sc *SmartCopier) MustCopyTo(dest, src interface{}) {
	// Temporarily enable panicOnError, ensuring it's restored afterward
	originalPanic := sc.panicOnError
	sc.panicOnError = true
	defer func() { sc.panicOnError = originalPanic }()

	// Call CopyTo. finalizeResult inside CopyTo will handle the panic.
	if err := sc.CopyTo(dest, src); err != nil {
		// This part should technically not be reached if panicOnError works correctly
		// inside finalizeResult. However, adding a fallback panic just in case.
		panic(fmt.Sprintf("mm.MustCopyTo failed: %v", err))
	}
}

// MergeTo copies data from src to dest, but only for fields that are
// considered empty/zero in the destination. It's equivalent to configuring
// the copier with WithOverwrite(false) and WithSkipEmpty(true) temporarily.
func (sc *SmartCopier) MergeTo(dest, src interface{}) error {
	// Save original settings
	originalOverwrite := sc.overwrite
	originalSkipEmpty := sc.skipEmptyValue // MergeTo implies skipping empty source values shouldn't happen
	originalSkipZero := sc.skipZeroValue   // so we only change overwrite.

	// Apply temporary settings for merge behavior
	sc.overwrite = false
	// sc.skipEmptyValue = true // Merge doesn't skip empty source, it skips overwriting non-empty dest
	// sc.skipZeroValue = true // Let's keep skipZero/Empty as configured by user, merge primarily means no overwrite

	// Ensure settings are restored
	defer func() {
		sc.overwrite = originalOverwrite
		sc.skipEmptyValue = originalSkipEmpty
		sc.skipZeroValue = originalSkipZero
	}()

	sc.debug("MergeTo called (temporarily setting overwrite=false)")
	// Call CopyTo with the modified settings
	return sc.CopyTo(dest, src)
}

// CopyToMultiple copies data from the first source to dest using the standard
// configuration, then merges data from subsequent sources into dest.
// Merge behavior uses WithOverwrite(false) and WithSkipEmpty(true) temporarily.
func (sc *SmartCopier) CopyToMultiple(dest interface{}, sources ...interface{}) error {
	if len(sources) == 0 {
		sc.debug("CopyToMultiple called with no sources.")
		return nil // Nothing to copy
	}

	// Validate destination once
	if dest == nil {
		return sc.finalizeResult(ErrDestinationIsNil, &mmContext{})
	}
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.IsNil() {
		return sc.finalizeResult(ErrDestNotPointer, &mmContext{})
	}
	destElem := destVal.Elem()
	if !destElem.CanSet() {
		err := fmt.Errorf("destination element (type %s) is not settable", destElem.Type())
		return sc.finalizeResult(err, &mmContext{})
	}

	// Prepare context for the entire multi-copy operation
	ctx := &mmContext{
		depth:   0,
		visited: make(map[visitKey]reflect.Value), // Reset visited for each full operation
		errors:  make([]error, 0),
	}

	var overallErr error // Tracks the first error if not collecting

	// 1. Copy the first source using current settings
	sc.debug("CopyToMultiple: Copying first source (%T)", sources[0])
	firstSrc := sources[0]
	if err := sc.copyToInternal(destElem, firstSrc, ctx); err != nil {
		if sc.handlePotentialError(err, ctx) {
			// Error collected, continue if possible
		} else {
			overallErr = err // Store first error if not collecting
			// If not collecting, stop after the first error
			if !sc.collectErrors {
				return sc.finalizeResult(overallErr, ctx)
			}
		}
	}

	// 2. Merge subsequent sources
	if len(sources) > 1 {
		// Save original settings relevant to merging
		originalOverwrite := sc.overwrite
		originalSkipEmpty := sc.skipEmptyValue // Using skipEmpty during merge
		originalSkipZero := sc.skipZeroValue   // Using skipZero during merge

		// Apply temporary settings for merge behavior
		sc.overwrite = false
		sc.skipEmptyValue = true // Skip empty values from subsequent sources during merge
		sc.skipZeroValue = true  // Skip zero values from subsequent sources during merge

		// Ensure settings are restored
		defer func() {
			sc.overwrite = originalOverwrite
			sc.skipEmptyValue = originalSkipEmpty
			sc.skipZeroValue = originalSkipZero
		}()

		sc.debug("CopyToMultiple: Merging subsequent sources (overwrite=false, skipEmpty/Zero=true)")
		for i := 1; i < len(sources); i++ {
			// Reset cycle detection for each merge source? No, keep context across merges.
			currentSrc := sources[i]
			sc.debug("CopyToMultiple: Merging source %d (%T)", i+1, currentSrc)
			if err := sc.copyToInternal(destElem, currentSrc, ctx); err != nil {
				if sc.handlePotentialError(err, ctx) {
					// Error collected, continue if possible
				} else if overallErr == nil {
					overallErr = err // Store first error if not collecting
				}
				// If not collecting, stop after the first error
				if !sc.collectErrors && overallErr != nil {
					break // Stop processing more sources
				}
			}
		}
	}

	// Finalize the result, considering any collected errors or the first error
	return sc.finalizeResult(overallErr, ctx)
}

// copyRecursive is the heart of the copier, handling different types recursively.
func (sc *SmartCopier) copyRecursive(dest, src reflect.Value, ctx *mmContext) error {
	// --- Pre-checks ---
	if sc.maxDepth >= 0 && ctx.depth > sc.maxDepth {
		sc.debug("Depth %d: Max depth (%d) reached, stopping recursion.", ctx.depth, sc.maxDepth)
		return nil // Stop recursion gracefully
	}

	// Check source validity
	if !src.IsValid() {
		if sc.skipNilSrc { // Treat invalid like nil for skipping purposes
			sc.debug("Depth %d: Source is invalid, skipNilSrc=true, skipping.", ctx.depth)
			return nil
		}
		if sc.overwrite && dest.CanSet() {
			sc.debug("Depth %d: Source is invalid, skipNilSrc=false, overwrite=true. Zeroing destination %s.", ctx.depth, dest.Type())
			dest.Set(reflect.Zero(dest.Type()))
		} else {
			sc.debug("Depth %d: Source is invalid, skipNilSrc=false, overwrite=false. Destination %s remains unchanged.", ctx.depth, dest.Type())
		}
		return nil // Handled invalid source
	}

	// Dereference source pointer/interface *once* for subsequent checks and type dispatching
	// Keep the original value for cycle detection and potential handler needs.
	originalSrc := src
	srcType := src.Type()
	if src.Kind() == reflect.Ptr || src.Kind() == reflect.Interface {
		if src.IsNil() {
			if sc.skipNilSrc {
				sc.debug("Depth %d: Source pointer/interface is nil, skipNilSrc=true, skipping.", ctx.depth)
				return nil
			}
			if sc.overwrite && dest.CanSet() {
				sc.debug("Depth %d: Source pointer/interface is nil, skipNilSrc=false, overwrite=true. Zeroing destination %s.", ctx.depth, dest.Type())
				dest.Set(reflect.Zero(dest.Type()))
			} else {
				sc.debug("Depth %d: Source pointer/interface is nil, skipNilSrc=false, overwrite=false. Destination %s remains unchanged.", ctx.depth, dest.Type())
			}
			return nil // Handled nil pointer/interface
		}
		// Only dereference if not nil
		src = src.Elem()
		// Re-check validity after dereferencing, especially for interface{}(nil)
		if !src.IsValid() {
			if sc.skipNilSrc {
				sc.debug("Depth %d: Source dereferenced to invalid, skipNilSrc=true, skipping.", ctx.depth)
				return nil
			}
			if sc.overwrite && dest.CanSet() {
				sc.debug("Depth %d: Source dereferenced to invalid, skipNilSrc=false, overwrite=true. Zeroing destination %s.", ctx.depth, dest.Type())
				dest.Set(reflect.Zero(dest.Type()))
			} else {
				sc.debug("Depth %d: Source dereferenced to invalid, skipNilSrc=false, overwrite=false. Destination %s remains unchanged.", ctx.depth, dest.Type())
			}
			return nil
		}
		srcType = src.Type() // Update srcType after potential dereference
	}

	// Skip source based on zero/empty checks (using the potentially dereferenced value)
	if sc.skipZeroValue && src.IsZero() {
		sc.debug("Depth %d: Source is zero value (%v), skipZeroValue=true, skipping.", ctx.depth, src.Interface())
		return nil
	}
	if sc.skipEmptyValue && isEmptyValue(src) {
		sc.debug("Depth %d: Source is empty value (%v), skipEmptyValue=true, skipping.", ctx.depth, src.Interface())
		return nil
	}

	// Check destination settability
	if !dest.CanSet() {
		// This might happen if called on an unexported field without unsafe,
		// or a temporary value that isn't addressable.
		// We usually check this higher up, but double-check here.
		sc.debug("Depth %d: Destination %s (%s) is not settable, skipping.", ctx.depth, dest.Type(), dest.Kind())
		return nil
	}
	destType := dest.Type()

	// --- Cycle Detection ---
	// Use the *original* source value (before dereferencing) for pointer identity.
	if sc.enableCycleDetect && originalSrc.CanAddr() {
		// Use CanAddr as a proxy for types that might participate in cycles (pointers, structs).
		// Using Addr().Pointer() directly is problematic for non-addressable values (like map elements).
		// A more robust approach might be needed, but pointer equality is the primary goal here.
		var key visitKey
		addr := uintptr(0)

		// Get address reliably only for Ptr, Chan, Func, Map, Slice, UnsafePointer
		// For Structs, use Addr() if possible. Interfaces need Elem() check.
		kind := originalSrc.Kind()
		if kind == reflect.Ptr || kind == reflect.Map || kind == reflect.Slice || kind == reflect.Chan || kind == reflect.Func || kind == reflect.UnsafePointer {
			if !originalSrc.IsNil() {
				addr = originalSrc.Pointer()
				key = visitKey{ptr: addr, typ: originalSrc.Type()} // Use pointer type for key
			}
		} else if kind == reflect.Interface {
			if !originalSrc.IsNil() {
				elem := originalSrc.Elem()
				if elem.CanAddr() { // Check if the concrete value is addressable
					// Use the address and type of the *concrete* value
					addr = elem.Addr().Pointer()
					key = visitKey{ptr: addr, typ: elem.Type()}
				} else if elem.Kind() == reflect.Ptr && !elem.IsNil() {
					// If concrete value is a pointer, use its info
					addr = elem.Pointer()
					key = visitKey{ptr: addr, typ: elem.Type()}
				}
				// If concrete value not addressable and not pointer, cannot reliably detect cycle via ptr.
			}
		} else if kind == reflect.Struct {
			if originalSrc.CanAddr() {
				addr = originalSrc.Addr().Pointer()
				key = visitKey{ptr: addr, typ: originalSrc.Type()}
			}
			// If struct not addressable, cannot reliably detect cycle via ptr.
		}

		if key.ptr != 0 { // If we got a valid key
			if existingDest, ok := ctx.visited[key]; ok {
				sc.debug("Depth %d: Cycle detected! Source %s (ptr %x) already visited, mapped to dest %s.", ctx.depth, key.typ, key.ptr, existingDest.Type())
				// Found a cycle. Try to set the current destination 'dest'
				// to the *existing* destination value we stored earlier.
				if existingDest.IsValid() && existingDest.Type().AssignableTo(destType) {
					sc.debug(" -> Assigning existing dest (%s) directly.", existingDest.Type())
					dest.Set(existingDest)
					return nil // Cycle handled
				}
				// Handle T vs *T mismatch specifically for cycles
				if dest.Kind() == reflect.Ptr && existingDest.Type() == destType.Elem() {
					// Current dest is *T, existing is T. Try assigning address.
					if existingDest.CanAddr() {
						sc.debug(" -> Assigning address of existing dest (%s) to pointer dest (%s).", existingDest.Type(), destType)
						dest.Set(existingDest.Addr())
						return nil // Cycle handled
					} else {
						sc.debug(" -> Cannot assign address of existing dest (%s) to pointer dest (%s) (not addressable).", existingDest.Type(), destType)
					}
				}
				// Handle *T vs T mismatch (less common, but possible)
				if existingDest.Kind() == reflect.Ptr && !existingDest.IsNil() && existingDest.Elem().Type() == destType {
					// Current dest is T, existing is *T. Try assigning dereferenced value.
					sc.debug(" -> Assigning element of existing pointer dest (%s) to value dest (%s).", existingDest.Type(), destType)
					dest.Set(existingDest.Elem())
					return nil // Cycle handled
				}
				// Try conversion as a last resort
				if conv, err := sc.tryConvert(existingDest, destType); err == nil && conv.IsValid() {
					sc.debug(" -> Assigning converted existing dest (%s -> %s).", existingDest.Type(), destType)
					dest.Set(conv)
					return nil // Cycle handled
				}

				// If we couldn't assign, report error (or collect)
				err := fmt.Errorf("%w: source %s (ptr %x) already visited, cannot assign existing destination %s to current destination %s",
					ErrCycleDetected, key.typ, key.ptr, existingDest.Type(), destType)
				if sc.handlePotentialError(err, ctx) {
					return nil // Error collected, continue if possible
				}
				return err
			}
			// Not visited yet, record this source pointer -> destination value mapping.
			// Store the *current* destination value. If it's a pointer, store the pointer.
			// If it's a struct, store the struct value.
			sc.debug("Depth %d: Recording visit: Source %s (ptr %x) -> Dest %s", ctx.depth, key.typ, key.ptr, dest.Type())
			ctx.visited[key] = dest
		}
	}

	// --- Recursive Copy Dispatch ---
	ctx.depth++
	defer func() { ctx.depth-- }()

	// --- Type Conversion Check ---
	// Try custom or built-in converters first if types don't match exactly
	// Note: We use the potentially dereferenced `src` and its `srcType` here.
	if srcType != destType {
		if converted, err := sc.tryConvert(src, destType); err == nil && converted.IsValid() {
			if !sc.overwrite && dest.IsValid() && !isEmptyValue(dest) {
				sc.debug("Depth %d: Skipping assignment for converted value: overwrite=false and destination %s is non-empty", ctx.depth, destType)
				return nil
			}
			sc.debug("Depth %d: Converted %s -> %s successfully, assigning.", ctx.depth, srcType, destType)
			dest.Set(converted)
			return nil // Conversion successful
		}
		// If conversion failed, fall through to kind-based copying, which might still work
		// (e.g., map[string]interface{} -> struct) or fail with ErrTypeMismatch later.
		sc.debug("Depth %d: No direct conversion found for %s -> %s. Proceeding with kind-based copy.", ctx.depth, srcType, destType)
	}

	// --- Kind-based Copying ---
	// Use dest.Kind() for dispatch as destination structure dictates the copy method
	switch dest.Kind() {
	case reflect.Struct:
		// Source can be Struct or Map
		if src.Kind() == reflect.Struct {
			sc.debug("Depth %d: Dispatching to copyStruct (%s <- %s)", ctx.depth, destType, srcType)
			destInfo := sc.getTypeInfo(destType)
			return sc.copyStruct(dest, src, ctx, destInfo)
		}
		if src.Kind() == reflect.Map {
			// Check map key type compatibility before proceeding
			if srcType.Key().Kind() != reflect.String {
				err := ErrMapKeyNotString
				if sc.handlePotentialError(err, ctx) {
					return nil
				}
				return err
			}
			sc.debug("Depth %d: Dispatching to copyMapToStruct (%s <- %s)", ctx.depth, destType, srcType)
			destInfo := sc.getTypeInfo(destType)
			return sc.copyMapToStruct(dest, src, ctx, destInfo)
		}
		// Fall through to mismatch error if source is neither struct nor map

	case reflect.Slice:
		// Source must be Slice
		if src.Kind() == reflect.Slice {
			sc.debug("Depth %d: Dispatching to copySlice (%s <- %s)", ctx.depth, destType, srcType)
			return sc.copySlice(dest, src, ctx)
		}
		// Fall through to mismatch error

	case reflect.Map:
		// Source can be Map or Struct
		if src.Kind() == reflect.Map {
			// Check key type compatibility before proceeding
			if !srcType.Key().AssignableTo(destType.Key()) {
				// Attempt conversion if not assignable (e.g., int <-> int64)
				// This requires more complex handling in copyMap, currently assumes assignable/convertible handled by recursive calls on keys.
				// Let's focus on basic assignability for now.
				// TODO: Consider adding key conversion support in copyMap if needed.
				sc.debug("Depth %d: Map key types %s and %s are not assignable.", ctx.depth, srcType.Key(), destType.Key())
				// Fall through, might fail later or convert if lucky
			}
			sc.debug("Depth %d: Dispatching to copyMap (%s <- %s)", ctx.depth, destType, srcType)
			return sc.copyMap(dest, src, ctx)
		}
		if src.Kind() == reflect.Struct {
			// Check map key type compatibility before proceeding
			if destType.Key().Kind() != reflect.String {
				err := ErrMapKeyNotString
				if sc.handlePotentialError(err, ctx) {
					return nil
				}
				return err
			}
			sc.debug("Depth %d: Dispatching to copyStructToMap (%s <- %s)", ctx.depth, destType, srcType)
			return sc.copyStructToMap(dest, src, ctx)
		}
		// Fall through to mismatch error

	case reflect.Ptr:
		// Destination is a pointer. Source can be pointer or value.
		// Use the *original* source value here, as copyPtr handles dereferencing.
		sc.debug("Depth %d: Dispatching to copyPtr (%s <- %s)", ctx.depth, destType, originalSrc.Type())
		return sc.copyPtr(dest, originalSrc, ctx) // Pass original source

	case reflect.Interface:
		// Destination is an interface. Source can be anything.
		// Use the *original* source value here as copyInterface handles interfaces/values.
		sc.debug("Depth %d: Dispatching to copyInterface (%s <- %s)", ctx.depth, destType, originalSrc.Type())
		return sc.copyInterface(dest, originalSrc, ctx) // Pass original source

	case reflect.Array:
		// Source must be Array
		if src.Kind() == reflect.Array {
			sc.debug("Depth %d: Dispatching to copyArray (%s <- %s)", ctx.depth, destType, srcType)
			return sc.copyArray(dest, src, ctx)
		}
		// Fall through to mismatch error

	// --- Basic Kind Assignment ---
	// Handle primitive types and string where direct assignment or conversion might work.
	// This handles cases where tryConvert didn't apply earlier (e.g. types were identical but
	// one of the complex handlers above didn't match).
	default:
		if srcType.AssignableTo(destType) {
			sc.debug("Depth %d: Assigning %s to %s directly.", ctx.depth, srcType, destType)
			if !sc.overwrite && dest.IsValid() && !isEmptyValue(dest) {
				sc.debug(" -> Skipping assignment: overwrite=false and destination is non-empty")
				return nil
			}
			dest.Set(src)
			return nil
		}
		// Conversion already attempted by tryConvert earlier if types didn't match.
		// If we reach here, it means types match OR conversion failed OR
		// it's a kind mismatch not handled above (e.g., struct -> slice).
	}

	// --- Mismatch Error ---
	err := fmt.Errorf("%w: cannot copy %s (kind %s) to %s (kind %s) at depth %d",
		ErrTypeMismatch, srcType, src.Kind(), destType, dest.Kind(), ctx.depth-1) // depth-1 for user context
	if sc.handlePotentialError(err, ctx) {
		return nil
	}
	return err
}

// hasUnexportedFields checks if a struct type contains unexported fields.
// Note: Currently unused, but could be useful for optimizations or checks.
func hasUnexportedFields(typ reflect.Type) bool {
	if typ.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < typ.NumField(); i++ {
		if !typ.Field(i).IsExported() { // Use IsExported() for clarity
			return true
		}
	}
	return false
}

// unsafeMemCopy performs a raw memory copy using unsafe.
// WARNING: Use with extreme caution. Only safe for types without pointers
// or when the pointer semantics are intentionally being duplicated bit-for-bit.
// Relies on the caller ensuring src and dest are addressable and types are compatible.
func (sc *SmartCopier) unsafeMemCopy(dest, src reflect.Value, info *typeInfo) error {
	// Basic safety checks (should be guaranteed by caller, but double-check)
	if !dest.CanAddr() || !src.CanAddr() {
		return fmt.Errorf("internal error: unsafeMemCopy called on non-addressable values (%s / %s)", dest.Type(), src.Type())
	}
	if info.size == 0 {
		return nil // Nothing to copy
	}
	if info.containsPtrs {
		// Maybe add a specific option to allow unsafe copy even with pointers?
		// For now, assume it's only called when containsPtrs is false (via isMemCopySuitable).
		sc.debug("Warning: Attempting unsafeMemCopy on type %s which may contain pointers. This is generally unsafe.", dest.Type())
		// return fmt.Errorf("unsafeMemCopy rejected for type %s containing pointers", dest.Type())
	}

	destPtr := unsafe.Pointer(dest.UnsafeAddr())
	srcPtr := unsafe.Pointer(src.UnsafeAddr())
	size := info.size

	sc.debug("Performing unsafe memory copy: %d bytes from %s (addr %p) to %s (addr %p)", size, src.Type(), srcPtr, dest.Type(), destPtr)

	// Use a buffer from the pool for potentially larger copies?
	// Or just use direct memmove? Direct memmove is likely fine.
	// The buffer pool was likely intended for temporary byte slices during conversion, not memcopy.
	memmove(destPtr, srcPtr, size)

	// Keep the Go objects alive until the memmove is complete.
	runtime.KeepAlive(dest.Interface())
	runtime.KeepAlive(src.Interface())
	return nil
}

// --- Type Info Caching ---

// getTypeInfo retrieves cached reflection info for a type, or generates and caches it.
func (sc *SmartCopier) getTypeInfo(typ reflect.Type) *typeInfo {
	// Check cache first
	if cached, ok := sc.typeInfoCache.Load(typ); ok {
		return cached.(*typeInfo)
	}

	// Not in cache, analyze the type
	sc.debug("Analyzing type %s for cache", typ)
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
	// Determine if memory copy is generally suitable for this type
	info.canMemCopy = isMemCopySuitable(typ)

	// If it's a struct, analyze fields
	if info.isStruct {
		info.fields = make(map[string]fieldInfo)
		numFields := typ.NumField()
		for i := 0; i < numFields; i++ {
			field := typ.Field(i)
			fieldName := field.Name
			tagVal := field.Tag.Get(sc.tagName)
			tagOpts := parseTagOptions(tagVal)
			// sc.debug("  Field %s (%s): Tag '%s' -> Options %v", fieldName, field.Type, tagVal, tagOpts)

			isExported := field.IsExported()
			// Field is settable if exported OR if unexported copying is enabled via unsafe
			canSet := isExported || (sc.copyUnexported && sc.useUnsafe)

			fInfo := fieldInfo{
				name:        fieldName,
				structField: field,
				index:       field.Index,
				offset:      field.Offset,
				typ:         field.Type,
				canSet:      canSet, // Store calculated settability
				tagOpts:     tagOpts,
			}

			info.fields[fieldName] = fInfo
			if tagName, ok := tagOpts["name"]; ok {
				if info.nameToField == nil {
					info.nameToField = make(map[string]fieldInfo)
				}
				info.nameToField[tagName] = fInfo
			}

			// Check for 'unmapped' tag
			if _, isUnmapped := tagOpts["unmapped"]; isUnmapped {
				fieldType := field.Type
				// Validate type: must be map[string]any or map[string]interface{}
				isValidUnmappedType := fieldType.Kind() == reflect.Map &&
					fieldType.Key().Kind() == reflect.String &&
					(fieldType.Elem().Kind() == reflect.Interface || // map[string]interface{}
						fieldType.Elem().Kind() == reflect.Invalid) // map[string]any (Elem returns Invalid kind)

				if isValidUnmappedType {
					if info.unmappedField == nil {
						info.unmappedField = &fInfo // Store pointer to the fieldInfo
						sc.debug("  Found valid 'unmapped' field '%s' in type %s", fieldName, typ)
					} else {
						// Log warning if multiple unmapped fields are found
						log.Printf("[mm copier warning] Multiple fields tagged 'unmapped' in type %s. Using '%s', ignoring '%s'.", typ, info.unmappedField.name, fieldName)
					}
				} else {
					// Log warning if the type is incorrect
					log.Printf("[mm copier warning] Field '%s' in type %s tagged 'unmapped' has invalid type %s. It must be map[string]interface{} or map[string]any. Ignoring tag.", fieldName, typ, fieldType)
				}
			}
		}
	}

	// Store in cache (LoadOrStore handles race condition)
	actual, _ := sc.typeInfoCache.LoadOrStore(typ, info)
	return actual.(*typeInfo)
}

// isMemCopySuitable checks if a type is suitable for direct memory copying.
// Generally true for primitive types, arrays of primitives, and structs
// containing only memcopy-suitable types (no pointers, interfaces, maps, etc.).
func isMemCopySuitable(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128:
		return true // Primitive types are safe
	case reflect.Array:
		// Array is suitable if its element type is suitable
		return isMemCopySuitable(typ.Elem())
	case reflect.Struct:
		// Struct is suitable only if it does NOT contain pointers (or types that hide pointers)
		return !checkContainsPointers(typ)
	default:
		// Pointers, slices, maps, chans, funcs, interfaces, strings contain pointers or internal structure
		return false
	}
}

// checkContainsPointers recursively checks if a type contains any pointers,
// including those hidden within slices, maps, interfaces, or strings.
func checkContainsPointers(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Ptr, reflect.UnsafePointer,
		reflect.Map, reflect.Slice, // These inherently contain pointers
		reflect.Chan, reflect.Func, // Contain pointers internally
		reflect.Interface, // Can contain pointers
		reflect.String:    // Contains a pointer to data
		return true
	case reflect.Array:
		// Array contains pointers if its element type contains pointers
		return checkContainsPointers(typ.Elem())
	case reflect.Struct:
		// Struct contains pointers if any of its fields contain pointers
		for i := 0; i < typ.NumField(); i++ {
			if checkContainsPointers(typ.Field(i).Type) {
				return true
			}
		}
		return false // No pointer fields found
	default:
		// Other kinds (primitives) do not contain pointers
		return false
	}
}

// getSettableFieldValue attempts to get a settable reflect.Value for a struct field,
// using unsafe access if necessary and configured. Returns zero reflect.Value if not possible.
func (sc *SmartCopier) getSettableFieldValue(structVal reflect.Value, fInfo *fieldInfo) reflect.Value {
	// Try standard FieldByIndex first
	fieldVal := structVal.FieldByIndex(fInfo.index)

	// If standard access works and it's settable, return it
	if fieldVal.IsValid() && fieldVal.CanSet() {
		return fieldVal
	}

	// Standard access failed or field not settable. Try unsafe if applicable.
	// Need copyUnexported=true, useUnsafe=true, and field must be unexported.
	isUnexported := !fInfo.structField.IsExported()
	if isUnexported && sc.copyUnexported && sc.useUnsafe {
		// Need the struct itself to be addressable to calculate field address
		if structVal.CanAddr() {
			// Calculate pointer to the field using offset
			// Ensure field offset is valid (should be non-negative)
			if fInfo.offset >= 0 {
				fieldPtr := unsafe.Pointer(structVal.UnsafeAddr() + fInfo.offset)
				// Create a reflect.Value pointing to this memory location with the correct type
				// Use reflect.NewAt(typ, ptr).Elem() to get the value itself
				unsafeFieldVal := reflect.NewAt(fInfo.typ, fieldPtr).Elem()

				// Check if the unsafely accessed value is settable (it should be)
				if unsafeFieldVal.IsValid() && unsafeFieldVal.CanSet() {
					sc.debug("Unsafe access: field '%s' is settable via unsafe ptr %p", fInfo.name, fieldPtr)
					return unsafeFieldVal
				} else {
					sc.debug("Unsafe access: field '%s' value created via unsafe ptr %p is not settable (unexpected)", fInfo.name, fieldPtr)
				}
			} else {
				sc.debug("Unsafe access: field '%s' has invalid offset %d", fInfo.name, fInfo.offset)
			}
		} else {
			sc.debug("Unsafe access: cannot get address of struct %s to access unexported field '%s'", structVal.Type(), fInfo.name)
		}
	}

	// If we reach here, the field is not settable
	sc.debug("Field '%s' in %s is not settable (Exported: %t, CanSet: %t, Unsafe Enabled: %t, Field Unexported: %t)",
		fInfo.name, structVal.Type(), fInfo.structField.IsExported(), fieldVal.IsValid() && fieldVal.CanSet(), sc.copyUnexported && sc.useUnsafe, isUnexported)
	return reflect.Value{} // Return zero value to indicate failure
}

// --- Specific Kind Copy Functions ---

func (sc *SmartCopier) copyPtr(dest, src reflect.Value, ctx *mmContext) error {
	destType := dest.Type()         // e.g., *DestType
	destElemType := destType.Elem() // e.g., DestType

	sc.debug("Depth %d: copyPtr: Dest: %s <- Src: %s", ctx.depth, destType, src.Type())

	// --- Basic Checks ---
	if !dest.CanSet() {
		// Should have been caught earlier, but defense-in-depth
		err := fmt.Errorf("cannot set destination pointer %s", destType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	// --- Handle Nil Source ---
	// src might be the original value (e.g., *SrcType) or already dereferenced (e.g., SrcType)
	// Check original source kind and nilness if possible, otherwise check current src
	isSrcNil := false
	if !src.IsValid() {
		isSrcNil = true
	} else {
		kind := src.Kind()
		if kind == reflect.Ptr || kind == reflect.Interface || kind == reflect.Map || kind == reflect.Slice || kind == reflect.Chan || kind == reflect.Func {
			if src.IsNil() {
				isSrcNil = true
			}
		}
		// If src is not a nillable kind, it cannot be nil.
	}

	if isSrcNil {
		if sc.skipNilSrc {
			sc.debug(" -> Source is nil, skipNilSrc=true, skipping ptr set.", ctx.depth)
			return nil
		}
		if sc.overwrite {
			sc.debug(" -> Source is nil, skipNilSrc=false, overwrite=true. Setting dest ptr %s to nil.", ctx.depth, destType)
			dest.Set(reflect.Zero(destType)) // Set pointer itself to nil
		} else {
			sc.debug(" -> Source is nil, skipNilSrc=false, overwrite=false. Leaving dest ptr %s unchanged.", ctx.depth, destType)
		}
		return nil // Handled nil source
	}

	// --- Prepare Destination Pointer ---
	// If destination pointer is nil, create a new value for it to point to.
	if dest.IsNil() {
		sc.debug(" -> Dest ptr is nil. Creating new %s.", ctx.depth, destElemType)
		newPtr := reflect.New(destElemType) // Creates *DestType
		dest.Set(newPtr)                    // Assign the new pointer to the destination field/variable
	} else if !sc.overwrite {
		// Destination pointer exists, and we are not overwriting. Stop here.
		sc.debug(" -> Dest ptr exists, overwrite=false. Skipping ptr element copy.", ctx.depth)
		return nil
	}
	// Now, dest points to a valid, settable DestType value.

	// --- Get Settable Element and Source Element ---
	destElem := dest.Elem() // Get the value pointed to (DestType)
	if !destElem.CanSet() {
		// This should ideally not happen if dest.Set worked, but check defensively
		err := fmt.Errorf("destination pointer element %s is not settable (after New/Set)", destElem.Type())
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	// Dereference source *if* it's still a pointer (it might have been dereferenced by copyRecursive)
	srcElem := src
	if src.Kind() == reflect.Ptr { // No need to check IsNil again, handled above
		srcElem = src.Elem()
	}

	// --- Recursive Copy into Element ---
	sc.debug(" -> Recursively copying source element %s into dest element %s", ctx.depth, srcElem.Type(), destElem.Type())
	if err := sc.copyRecursive(destElem, srcElem, ctx); err != nil {
		// Error handled by copyRecursive or needs bubbling up
		return fmt.Errorf("copying pointer element %s <- %s: %w", destElem.Type(), srcElem.Type(), err)
	}

	return nil
}

func (sc *SmartCopier) copyStruct(dest, src reflect.Value, ctx *mmContext, destInfo *typeInfo) error {
	srcInfo := sc.getTypeInfo(src.Type())
	sc.debug("Depth %d: copyStruct: Dest: %s <- Src: %s", ctx.depth, dest.Type(), src.Type())

	// --- Optimization: Memory Copy ---
	// If types are identical, no special handling needed (tags, skip, formatters),
	// and type is suitable, perform a faster memory copy.
	// Requires careful consideration: Tags, skips, formatters, converters, unmapped fields must be bypassed.
	// This optimization is complex to get right with all features. Disable for now.
	// TODO: Revisit memcopy optimization if performance is critical.
	// if sc.useUnsafe && dest.Type() == src.Type() && destInfo.canMemCopy /* && !hasSpecialHandling(...) */ {
	// 	sc.debug(" -> Attempting unsafe memory copy for struct %s", dest.Type())
	// 	err := sc.unsafeMemCopy(dest, src, destInfo)
	// 	if err == nil {
	// 		return nil // Memcopy successful
	// 	}
	// 	sc.debug(" -> Unsafe memory copy failed: %v. Falling back to field-by-field.", err)
	// }

	// --- Field-by-Field Copy ---
	usedSrcFields := make(map[string]struct{}) // Track which source fields were used for mapping/copying

	// --- Prepare Unmapped Field Handling (Destination) ---
	var unmappedDestMap reflect.Value
	var unmappedDestValueType reflect.Type
	var unmappedDestFieldName string
	if destInfo.unmappedField != nil {
		unmappedDestFieldName = destInfo.unmappedField.name
		// Get the map field value. Must be settable.
		mapFieldVal := sc.getSettableFieldValue(dest, destInfo.unmappedField)
		if mapFieldVal.IsValid() { // Checks CanSet implicitly via getSettableFieldValue
			// Ensure it's the correct type (already checked in getTypeInfo, but double-check kind)
			if mapFieldVal.Kind() == reflect.Map {
				// If nil, create the map
				if mapFieldVal.IsNil() {
					sc.debug(" -> Initializing nil 'unmapped' field '%s'", unmappedDestFieldName)
					mapFieldVal.Set(reflect.MakeMap(mapFieldVal.Type()))
				}
				unmappedDestMap = mapFieldVal
				unmappedDestValueType = mapFieldVal.Type().Elem() // e.g., interface{} or any
				sc.debug(" -> Will collect unmapped fields into '%s' (type %s)", unmappedDestFieldName, mapFieldVal.Type())
			} else {
				// This case should have been caught by the warning in getTypeInfo
				sc.debug(" -> 'unmapped' field '%s' is not a map, ignoring.", unmappedDestFieldName)
			}
		} else {
			sc.debug(" -> 'unmapped' field '%s' is not settable, cannot collect.", unmappedDestFieldName)
		}
	}

	// --- Iterate Through Destination Fields ---
	for destFieldName, destFieldInfo := range destInfo.fields {
		sc.debug("  Processing dest field: %s", destFieldName)

		// Skip the unmapped field itself if it exists
		if destFieldName == unmappedDestFieldName {
			sc.debug("   - Skipping dest field '%s' (used for unmapped data)", destFieldName)
			continue
		}

		// Skip based on global skip list
		if _, skip := sc.skipFields[destFieldName]; skip {
			sc.debug("   - Skipping dest field '%s' (global skip list)", destFieldName)
			continue
		}

		// Skip based on "-" tag
		if _, ignore := destFieldInfo.tagOpts["-"]; ignore {
			sc.debug("   - Skipping dest field '%s' (tag '-)", destFieldName)
			continue
		}

		// --- Determine Corresponding Source Field Name ---
		// Default: Same name
		srcFieldName := destFieldName
		// Check destination tag "name" option
		if tagName, ok := destFieldInfo.tagOpts["name"]; ok {
			srcFieldName = tagName
			sc.debug("   - Dest tag 'name' maps dest '%s' to src field '%s'", destFieldName, srcFieldName)
		}

		for mapSrc, mapDest := range sc.fieldMappings {
			if mapDest == destFieldName {
				srcFieldName = mapSrc
				break
			}
		}

		// Check source's nameToField if no global mapping
		if srcFieldName == destFieldName {
			if srcFieldInfo, ok := srcInfo.nameToField[destFieldName]; ok {
				srcFieldName = srcFieldInfo.name
			}
		}

		if _, skip := sc.skipFields[destFieldName]; skip {
			continue // Skip this destination field
		}

		// Check global field mappings (src->dest). Does any source map to this destination?
		// Overrides tag or default name if found.
		for mapSrc, mapDest := range sc.fieldMappings {
			if mapDest == destFieldName {
				// Found a mapping: this source field `mapSrc` maps to our target dest field.
				// So, we should look for `mapSrc` in the source struct.
				srcFieldName = mapSrc
				sc.debug("   - Global mapping maps src field '%s' to dest field '%s'", srcFieldName, destFieldName)
				break // Found the relevant mapping for this destination field
			}
		}
		// srcFieldName now holds the name of the source field to look for.

		// --- Find Source Field Value ---
		srcFieldInfo, srcFieldFound := srcInfo.fields[srcFieldName]
		if !srcFieldFound {
			sc.debug("   - No corresponding source field '%s' found. Skipping.", srcFieldName)
			continue
		}

		// Check if source field is exported or if we can access unexported
		isSrcUnexported := !srcFieldInfo.structField.IsExported()
		if isSrcUnexported && !(sc.copyUnexported && sc.useUnsafe) {
			sc.debug("   - Source field '%s' is unexported and copyUnexported=false. Skipping.", srcFieldName)
			continue
		}
		// If unexported reading is enabled, try to get the value.
		// Note: Getting unexported fields directly via reflect can be unreliable.
		// Unsafe is needed even for reading in many cases.
		var srcVal reflect.Value
		if isSrcUnexported && sc.copyUnexported && sc.useUnsafe {
			// Try unsafe reading
			if src.CanAddr() {
				srcFieldPtr := unsafe.Pointer(src.UnsafeAddr() + srcFieldInfo.offset)
				srcVal = reflect.NewAt(srcFieldInfo.typ, srcFieldPtr).Elem()
				sc.debug("   - Reading unexported src field '%s' via unsafe.", srcFieldName)
			} else {
				sc.debug("   - Cannot read unexported src field '%s' (struct not addressable). Skipping.", srcFieldName)
				continue // Cannot read
			}
		} else if !isSrcUnexported {
			// Read exported field normally
			srcVal = src.FieldByIndex(srcFieldInfo.index)
		} else {
			// Should not happen due to check above, but defensively skip
			continue
		}

		if !srcVal.IsValid() {
			// This might happen if unsafe read failed or field somehow invalid
			sc.debug("   - Source field '%s' value is invalid. Skipping.", srcFieldName)
			continue
		}

		// Mark source field as used
		usedSrcFields[srcFieldName] = struct{}{}

		// --- Check Source Skip Conditions (omitempty, skipZero, skipEmpty) ---
		if _, omit := srcFieldInfo.tagOpts["omitempty"]; omit && isEmptyValue(srcVal) {
			sc.debug("   - Skipping src field '%s': 'omitempty' tag and value is empty.", srcFieldName)
			continue
		}
		// Note: We use the user-configured global skipZero/skipEmpty flags *in addition* to omitempty tag.
		if sc.skipZeroValue && srcVal.IsZero() {
			sc.debug("   - Skipping src field '%s': skipZeroValue=true and value is zero.", srcFieldName)
			continue
		}
		if sc.skipEmptyValue && isEmptyValue(srcVal) {
			sc.debug("   - Skipping src field '%s': skipEmptyValue=true and value is empty.", srcFieldName)
			continue
		}

		// --- Get Destination Field Value (Settable) ---
		destVal := sc.getSettableFieldValue(dest, &destFieldInfo)
		if !destVal.IsValid() {
			// If getSettableFieldValue failed (e.g., dest unexported, unsafe off)
			sc.debug("   - Destination field '%s' is not settable. Skipping.", destFieldName)
			continue // Cannot set this destination field
		}

		// --- Check Destination Overwrite Condition ---
		// If overwrite is disabled, check if the destination already has a non-empty value.
		if !sc.overwrite && destVal.IsValid() && !isEmptyValue(destVal) {
			sc.debug("   - Skipping field '%s': overwrite=false and destination is non-empty.", destFieldName)
			continue
		}

		// --- Apply Formatter (If Applicable) ---
		if formatter, ok := sc.formatters[destFieldName]; ok {
			if !srcVal.CanInterface() {
				sc.debug("   - Cannot apply formatter to field '%s': source value cannot be interfaced.", destFieldName)
				// Proceed without formatter? Or error? Let's proceed.
			} else {
				formattedSrc := formatter(srcVal.Interface())
				sc.debug("   - Applied formatter to '%s'. Result: %T (%v)", destFieldName, formattedSrc, formattedSrc)
				// Handle nil result from formatter
				if formattedSrc == nil {
					if sc.overwrite { // Only zero dest if overwriting
						sc.debug("    - Formatter returned nil. Zeroing destination field '%s'.", destFieldName)
						destVal.Set(reflect.Zero(destVal.Type()))
					} else {
						sc.debug("    - Formatter returned nil. Leaving destination field '%s' unchanged (overwrite=false).", destFieldName)
					}
					continue // Skip recursion for this field
				}
				// Use the formatted value as the new source for recursion
				srcVal = reflect.ValueOf(formattedSrc)
				if !srcVal.IsValid() { // Should not happen if formattedSrc != nil
					sc.debug("    - Formatted value '%s' is invalid. Skipping.", destFieldName)
					continue
				}
				// Fall through to recursive copy with the new srcVal
			}
		}

		// --- Recursive Copy ---
		sc.debug("   - Recursively copying field '%s' <- '%s'", destFieldName, srcFieldName)
		if err := sc.copyRecursive(destVal, srcVal, ctx); err != nil {
			fieldErr := fmt.Errorf("copying field '%s' <- '%s': %w", destFieldName, srcFieldName, err)
			if sc.handlePotentialError(fieldErr, ctx) {
				continue // Error collected, continue with next field
			}
			return fieldErr // Return immediately if not collecting errors
		}
	} // End loop through destination fields

	// --- Handle Unmapped Source Fields ---
	if unmappedDestMap.IsValid() {
		sc.debug("  Checking for unmapped source fields to put into '%s'", unmappedDestFieldName)
		for srcFieldName, srcFieldInfo := range srcInfo.fields {
			// Skip if source field was already used for a mapped destination field
			if _, used := usedSrcFields[srcFieldName]; used {
				continue
			}
			// Skip if source field has "-" tag
			if _, ignore := srcFieldInfo.tagOpts["-"]; ignore {
				continue
			}

			// Check if source field is readable (exported or unsafe enabled)
			isSrcUnexported := !srcFieldInfo.structField.IsExported()
			if isSrcUnexported && !(sc.copyUnexported && sc.useUnsafe) {
				continue // Cannot read unexported source
			}
			// Get source value (handle unsafe if needed)
			var srcVal reflect.Value
			if isSrcUnexported {
				if src.CanAddr() {
					srcFieldPtr := unsafe.Pointer(src.UnsafeAddr() + srcFieldInfo.offset)
					srcVal = reflect.NewAt(srcFieldInfo.typ, srcFieldPtr).Elem()
				} else {
					continue // Cannot read
				}
			} else {
				srcVal = src.FieldByIndex(srcFieldInfo.index)
			}

			if !srcVal.IsValid() {
				continue
			}

			// Skip based on omitempty, skipZero, skipEmpty
			if _, omit := srcFieldInfo.tagOpts["omitempty"]; omit && isEmptyValue(srcVal) {
				continue
			}
			if sc.skipZeroValue && srcVal.IsZero() {
				continue
			}
			if sc.skipEmptyValue && isEmptyValue(srcVal) {
				continue
			}

			// Determine the key for the unmapped map (usually the source field name)
			mapKey := reflect.ValueOf(srcFieldName)               // Key is string
			mapValue := reflect.New(unmappedDestValueType).Elem() // Create value of map's elem type

			sc.debug("   - Copying unmapped src field '%s' into map key '%s'", srcFieldName, srcFieldName)
			// Recursively copy the source value into the newly created map value holder
			if err := sc.copyRecursive(mapValue, srcVal, ctx); err != nil {
				mapErr := fmt.Errorf("copying unmapped field '%s' to map key '%s': %w", srcFieldName, srcFieldName, err)
				if sc.handlePotentialError(mapErr, ctx) {
					continue // Error collected, continue with next unmapped field
				}
				return mapErr // Return immediately
			}

			// Add to the unmapped destination map
			// Check overwrite for map entry? If overwrite=false, don't replace existing keys.
			if !sc.overwrite {
				if existing := unmappedDestMap.MapIndex(mapKey); existing.IsValid() && !isEmptyValue(existing) {
					sc.debug("    - Skipping unmapped key '%s': overwrite=false and key exists in map.", srcFieldName)
					continue
				}
			}
			unmappedDestMap.SetMapIndex(mapKey, mapValue)
		}
	}

	return nil // Struct copy successful
}

func (sc *SmartCopier) copyMapToStruct(dest reflect.Value, src reflect.Value, ctx *mmContext, destInfo *typeInfo) error {
	srcType := src.Type()   // Map type
	destType := dest.Type() // Struct type
	sc.debug("Depth %d: copyMapToStruct: Dest: %s <- Src: %s", ctx.depth, destType, srcType)

	// --- Basic Checks ---
	if src.Kind() != reflect.Map { // Should be guaranteed by caller dispatch, but check
		return fmt.Errorf("%w: source is %s, not map", ErrTypeMismatch, src.Kind())
	}
	if srcType.Key().Kind() != reflect.String { // Key must be string
		return ErrMapKeyNotString
	}
	if src.IsNil() {
		sc.debug(" -> Source map is nil, nothing to copy.", ctx.depth)
		return nil // Nothing to copy from a nil map
	}

	// --- Prepare Unmapped Field Handling (Destination) ---
	var unmappedDestMap reflect.Value
	var unmappedDestValueType reflect.Type
	var unmappedDestFieldName string
	if destInfo.unmappedField != nil {
		unmappedDestFieldName = destInfo.unmappedField.name
		mapFieldVal := sc.getSettableFieldValue(dest, destInfo.unmappedField)
		if mapFieldVal.IsValid() && mapFieldVal.Kind() == reflect.Map {
			if mapFieldVal.IsNil() {
				mapFieldVal.Set(reflect.MakeMap(mapFieldVal.Type()))
			}
			unmappedDestMap = mapFieldVal
			unmappedDestValueType = mapFieldVal.Type().Elem()
			sc.debug(" -> Will collect unused map keys into '%s' (type %s)", unmappedDestFieldName, mapFieldVal.Type())
		} else {
			sc.debug(" -> 'unmapped' field '%s' is not a valid map or not settable, ignoring.", unmappedDestFieldName)
		}
	}

	usedMapKeys := make(map[string]struct{}) // Track which map keys were used

	// --- Iterate Through Destination Struct Fields ---
	for destFieldName, destFieldInfo := range destInfo.fields {
		sc.debug("  Processing dest field: %s", destFieldName)

		// Skip unmapped field itself
		if destFieldName == unmappedDestFieldName {
			continue
		}
		// Skip based on global list or "-" tag
		if _, skip := sc.skipFields[destFieldName]; skip {
			continue
		}
		if _, ignore := destFieldInfo.tagOpts["-"]; ignore {
			continue
		}

		// --- Determine Corresponding Source Map Key ---
		srcKeyName := destFieldName                           // Default: field name matches map key
		if tagName, ok := destFieldInfo.tagOpts["name"]; ok { // Check tag
			srcKeyName = tagName
		}
		// Check global mappings (src->dest, so find src mapping to this dest)
		for mapSrc, mapDest := range sc.fieldMappings {
			if mapDest == destFieldName {
				srcKeyName = mapSrc
				break
			}
		}
		sc.debug("   - Looking for source map key '%s'", srcKeyName)
		srcKeyVal := reflect.ValueOf(srcKeyName) // reflect.Value(string)

		// --- Find Source Value in Map ---
		srcVal := src.MapIndex(srcKeyVal)
		if !srcVal.IsValid() {
			sc.debug("   - Key '%s' not found in source map. Skipping.", srcKeyName)
			continue // Key not found
		}
		usedMapKeys[srcKeyName] = struct{}{} // Mark key as used

		// --- Handle Interface Value from Map ---
		// If map is map[string]interface{}, value might be wrapped
		if srcVal.Kind() == reflect.Interface && !srcVal.IsNil() {
			srcVal = srcVal.Elem()
			// Re-check validity after Elem()
			if !srcVal.IsValid() {
				sc.debug("   - Value for key '%s' is nil interface or invalid after Elem(). Skipping.", srcKeyName)
				continue
			}
		} else if srcVal.Kind() == reflect.Interface && srcVal.IsNil() {
			sc.debug("   - Value for key '%s' is nil interface. Skipping.", srcKeyName)
			continue
		}

		// --- Skip Checks (Zero/Empty) ---
		if sc.skipZeroValue && srcVal.IsZero() {
			sc.debug("   - Skipping map key '%s': skipZeroValue=true and value is zero.", srcKeyName)
			continue
		}
		if sc.skipEmptyValue && isEmptyValue(srcVal) {
			sc.debug("   - Skipping map key '%s': skipEmptyValue=true and value is empty.", srcKeyName)
			continue
		}

		// --- Get Destination Field Value (Settable) ---
		destVal := sc.getSettableFieldValue(dest, &destFieldInfo)
		if !destVal.IsValid() {
			sc.debug("   - Destination field '%s' is not settable. Skipping.", destFieldName)
			continue
		}

		// --- Overwrite Check ---
		if !sc.overwrite && destVal.IsValid() && !isEmptyValue(destVal) {
			sc.debug("   - Skipping field '%s': overwrite=false and destination is non-empty.", destFieldName)
			continue
		}

		// --- Formatter ---
		if formatter, ok := sc.formatters[destFieldName]; ok {
			// Apply formatter logic similar to copyStruct
			if !srcVal.CanInterface() {
				sc.debug("   - Cannot apply formatter to field '%s': map value cannot be interfaced.", destFieldName)
			} else {
				formattedSrc := formatter(srcVal.Interface())
				sc.debug("   - Applied formatter to '%s' from key '%s'. Result: %T (%v)", destFieldName, srcKeyName, formattedSrc, formattedSrc)
				if formattedSrc == nil {
					if sc.overwrite {
						destVal.Set(reflect.Zero(destVal.Type()))
					}
					continue // Skip recursion
				}
				srcVal = reflect.ValueOf(formattedSrc)
				if !srcVal.IsValid() {
					continue
				}
				// Fall through to recursive copy with formatted srcVal
			}
		}

		// --- Recursive Copy ---
		sc.debug("   - Recursively copying field '%s' <- map['%s']", destFieldName, srcKeyName)
		if err := sc.copyRecursive(destVal, srcVal, ctx); err != nil {
			fieldErr := fmt.Errorf("copying field '%s' <- map['%s']: %w", destFieldName, srcKeyName, err)
			if sc.handlePotentialError(fieldErr, ctx) {
				continue // Error collected, continue with next field
			}
			return fieldErr // Return immediately
		}
	} // End loop through destination fields

	// --- Handle Unused Map Keys (Collect into Unmapped Field) ---
	if unmappedDestMap.IsValid() {
		sc.debug("  Checking for unused map keys to put into '%s'", unmappedDestFieldName)
		iter := src.MapRange()
		for iter.Next() {
			key := iter.Key() // reflect.Value(string)
			keyStr := key.String()

			// Skip if this key was already mapped to a struct field
			if _, used := usedMapKeys[keyStr]; used {
				continue
			}

			val := iter.Value()
			// Handle interface value
			if val.Kind() == reflect.Interface && !val.IsNil() {
				val = val.Elem()
			}
			if !val.IsValid() || (val.Kind() == reflect.Interface && val.IsNil()) {
				continue // Skip invalid or nil interface values
			}

			// Skip based on zero/empty
			if sc.skipZeroValue && val.IsZero() {
				continue
			}
			if sc.skipEmptyValue && isEmptyValue(val) {
				continue
			}

			// Create a holder for the map value type
			mapValueHolder := reflect.New(unmappedDestValueType).Elem()

			sc.debug("   - Copying unused map key '%s' into unmapped field map", keyStr)
			// Copy the map value into the holder
			if err := sc.copyRecursive(mapValueHolder, val, ctx); err != nil {
				mapErr := fmt.Errorf("copying unmapped map key '%s' to map: %w", keyStr, err)
				if sc.handlePotentialError(mapErr, ctx) {
					continue // Error collected, continue
				}
				return mapErr // Return immediately
			}

			// Add to the destination unmapped map
			// Check overwrite
			if !sc.overwrite {
				if existing := unmappedDestMap.MapIndex(key); existing.IsValid() && !isEmptyValue(existing) {
					sc.debug("    - Skipping unmapped key '%s': overwrite=false and key exists.", keyStr)
					continue
				}
			}
			unmappedDestMap.SetMapIndex(key, mapValueHolder)
		}
	}

	return nil // Map to Struct copy successful
}

func (sc *SmartCopier) copyStructToMap(dest, src reflect.Value, ctx *mmContext) error {
	destType := dest.Type() // Map type
	srcType := src.Type()   // Struct type
	sc.debug("Depth %d: copyStructToMap: Dest: %s <- Src: %s", ctx.depth, destType, srcType)

	// --- Basic Checks ---
	if dest.Kind() != reflect.Map {
		return ErrDestNotMap
	} // Should be guaranteed by caller
	if destType.Key().Kind() != reflect.String {
		return ErrMapKeyNotString
	}
	if !dest.CanSet() {
		err := fmt.Errorf("cannot set destination map %s", destType)
		// No context here yet, but finalizeResult will handle panic
		if sc.panicOnError {
			panic(err)
		}
		return err
	}

	// --- Initialize Destination Map ---
	if dest.IsNil() {
		sc.debug(" -> Initializing nil destination map %s", destType)
		dest.Set(reflect.MakeMap(destType))
	} else if sc.overwrite {
		// If overwriting, clear existing map? Or just overwrite keys?
		// Standard behavior is to overwrite/add keys. Let's stick with that.
		// If a full clear is needed, user should pass an empty map.
		sc.debug(" -> Overwrite=true. Will overwrite existing keys in dest map %s", destType)
	}

	srcInfo := sc.getTypeInfo(srcType)
	destValueType := destType.Elem() // Type of values in the destination map

	// --- Iterate Through Source Struct Fields ---
	for srcFieldName, srcFieldInfo := range srcInfo.fields {
		sc.debug("  Processing src field: %s", srcFieldName)

		// Skip unexported unless allowed
		isSrcUnexported := !srcFieldInfo.structField.IsExported()
		if isSrcUnexported && !(sc.copyUnexported && sc.useUnsafe) {
			sc.debug("   - Skipping src field '%s' (unexported)", srcFieldName)
			continue
		}
		// Skip based on "-" tag
		if _, ignore := srcFieldInfo.tagOpts["-"]; ignore {
			sc.debug("   - Skipping src field '%s' (tag '-')", srcFieldName)
			continue
		}

		// --- Get Source Field Value ---
		var srcVal reflect.Value
		if isSrcUnexported { // Must use unsafe if enabled
			if src.CanAddr() {
				srcFieldPtr := unsafe.Pointer(src.UnsafeAddr() + srcFieldInfo.offset)
				srcVal = reflect.NewAt(srcFieldInfo.typ, srcFieldPtr).Elem()
				sc.debug("   - Reading unexported src field '%s' via unsafe.", srcFieldName)
			} else {
				sc.debug("   - Cannot read unexported src field '%s' (struct not addressable). Skipping.", srcFieldName)
				continue
			}
		} else {
			srcVal = src.FieldByIndex(srcFieldInfo.index)
		}

		if !srcVal.IsValid() {
			sc.debug("   - Source field '%s' value is invalid. Skipping.", srcFieldName)
			continue
		}

		// --- Skip Checks (omitempty, Zero, Empty) ---
		if _, omit := srcFieldInfo.tagOpts["omitempty"]; omit && isEmptyValue(srcVal) {
			sc.debug("   - Skipping src field '%s': 'omitempty' tag and value is empty.", srcFieldName)
			continue
		}
		if sc.skipZeroValue && srcVal.IsZero() {
			sc.debug("   - Skipping src field '%s': skipZeroValue=true and value is zero.", srcFieldName)
			continue
		}
		if sc.skipEmptyValue && isEmptyValue(srcVal) {
			sc.debug("   - Skipping src field '%s': skipEmptyValue=true and value is empty.", srcFieldName)
			continue
		}

		// --- Determine Destination Map Key ---
		destKeyName := srcFieldName // Default: field name
		// Check source tag "name" option
		if tagName, ok := srcFieldInfo.tagOpts["name"]; ok {
			destKeyName = tagName
			sc.debug("   - Source tag 'name' maps to dest key '%s'", destKeyName)
		}
		// Check global field mappings (src->dest)
		if mappedName, ok := sc.fieldMappings[srcFieldName]; ok {
			destKeyName = mappedName
			sc.debug("   - Global mapping maps src '%s' to dest key '%s'", srcFieldName, destKeyName)
		}
		destKeyVal := reflect.ValueOf(destKeyName) // reflect.Value(string)

		// --- Overwrite Check for Map Key ---
		if !sc.overwrite {
			if existing := dest.MapIndex(destKeyVal); existing.IsValid() && !isEmptyValue(existing) {
				sc.debug("   - Skipping map key '%s': overwrite=false and key exists with non-empty value.", destKeyName)
				continue
			}
		}

		// --- Prepare Destination Value Holder ---
		destMapValueHolder := reflect.New(destValueType).Elem()

		// --- Recursive Copy ---
		sc.debug("   - Recursively copying src field '%s' into map key '%s'", srcFieldName, destKeyName)
		if err := sc.copyRecursive(destMapValueHolder, srcVal, ctx); err != nil {
			fieldErr := fmt.Errorf("copying struct field '%s' to map key '%s': %w", srcFieldName, destKeyName, err)
			if sc.handlePotentialError(fieldErr, ctx) {
				continue // Error collected, continue
			}
			return fieldErr // Return immediately
		}

		// --- Set Map Index ---
		dest.SetMapIndex(destKeyVal, destMapValueHolder)

	} // End loop through source fields

	return nil // Struct to Map copy successful
}

func (sc *SmartCopier) copySlice(dest, src reflect.Value, ctx *mmContext) error {
	destType := dest.Type()
	srcType := src.Type()
	srcLen := src.Len()
	sc.debug("Depth %d: copySlice: Dest: %s <- Src: %s (len %d)", ctx.depth, destType, srcType, srcLen)

	// --- Basic Checks ---
	if !dest.CanSet() {
		err := fmt.Errorf("cannot set destination slice %s", destType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	// --- Handle Nil Source ---
	if src.IsNil() {
		if sc.skipNilSrc {
			sc.debug(" -> Source slice is nil, skipNilSrc=true. Skipping.", ctx.depth)
			return nil
		}
		if sc.overwrite {
			sc.debug(" -> Source slice is nil, overwrite=true. Setting dest slice %s to nil.", ctx.depth, destType)
			dest.Set(reflect.Zero(destType)) // Set slice itself to nil
		} else {
			sc.debug(" -> Source slice is nil, overwrite=false. Leaving dest slice %s unchanged.", ctx.depth, destType)
		}
		return nil
	}

	// --- Prepare Destination Slice ---
	if dest.IsNil() || sc.overwrite {
		// Create a new slice with same length and capacity as source
		newSlice := reflect.MakeSlice(destType, srcLen, srcLen)
		sc.debug(" -> Created/Replaced dest slice %s with len/cap %d", destType, srcLen)
		dest.Set(newSlice)
	} else { // Destination exists and overwrite is false
		// Merge behavior? For slices, merging usually means appending or updating matching elements.
		// Current CopyTo logic doesn't support slice merging easily.
		// With overwrite=false, we could potentially only copy if dest len matches src len,
		// otherwise do nothing? Let's adopt the latter for now.
		if dest.Len() != srcLen {
			sc.debug(" -> Skipping slice copy: overwrite=false and lengths differ (dest %d != src %d)", dest.Len(), srcLen)
			return nil // Don't copy if lengths differ and not overwriting
		}
		sc.debug(" -> Dest slice exists, overwrite=false, lengths match. Will copy elements recursively (honoring overwrite).", ctx.depth)
	}

	// If source length is 0, we are done (already handled dest slice creation/clearing)
	if srcLen == 0 {
		return nil
	}

	// --- Optimization: reflect.Copy ---
	// Use reflect.Copy if element types are assignable AND memory copy is suitable
	// (meaning no pointers or complex types requiring deep copies or conversions).
	destElemType := destType.Elem()
	// Moved srcElemType declaration inside the if to fix unused variable warning
	if srcElemType := srcType.Elem(); srcElemType.AssignableTo(destElemType) && isMemCopySuitable(destElemType) {
		sc.debug(" -> Using reflect.Copy for slice %s (Elem type %s suitable for memcopy)", destType, destElemType)
		reflect.Copy(dest, src)
		return nil // Bulk copy done
	}

	// --- Element-by-Element Copy ---
	sc.debug(" -> Performing element-by-element copy for slice %s", destType)
	for i := 0; i < srcLen; i++ {
		srcElem := src.Index(i)
		destElem := dest.Index(i)

		// --- Destination Element Settability Check ---
		// While the slice itself is settable, individual elements might not be
		// if they are complex types obtained in non-settable ways (unlikely here).
		if !destElem.CanSet() {
			err := fmt.Errorf("cannot set element %d in destination slice %s", i, destType)
			if sc.handlePotentialError(err, ctx) {
				continue // Skip this element
			}
			return err
		}

		// --- Overwrite Check for Element ---
		// Only perform the copy if overwriting is enabled OR if the destination element is empty.
		if !sc.overwrite && destElem.IsValid() && !isEmptyValue(destElem) {
			sc.debug("    - Skipping element %d: overwrite=false and dest element is non-empty.", i)
			continue
		}

		// --- Recursive Copy for Element ---
		// Note: Skip checks (nil/zero/empty) for srcElem are handled within the recursive call
		sc.debug("    - Recursively copying element %d", i)
		if err := sc.copyRecursive(destElem, srcElem, ctx); err != nil {
			elemErr := fmt.Errorf("copying slice index %d: %w", i, err)
			if sc.handlePotentialError(elemErr, ctx) {
				continue // Error collected, continue with next element
			}
			return elemErr // Return immediately
		}
	}

	return nil // Slice copy successful
}

func (sc *SmartCopier) copyMap(dest, src reflect.Value, ctx *mmContext) error {
	destType := dest.Type()
	srcType := src.Type()
	srcLen := src.Len() // Length for MakeMapWithSize hint
	sc.debug("Depth %d: copyMap: Dest: %s <- Src: %s (len hint %d)", ctx.depth, destType, srcType, srcLen)

	// --- Basic Checks ---
	if !dest.CanSet() {
		err := fmt.Errorf("cannot set destination map %s", destType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	// --- Handle Nil Source ---
	if src.IsNil() {
		if sc.skipNilSrc {
			sc.debug(" -> Source map is nil, skipNilSrc=true. Skipping.", ctx.depth)
			return nil
		}
		if sc.overwrite {
			sc.debug(" -> Source map is nil, overwrite=true. Setting dest map %s to nil.", ctx.depth, destType)
			dest.Set(reflect.Zero(destType)) // Set map itself to nil
		} else {
			sc.debug(" -> Source map is nil, overwrite=false. Leaving dest map %s unchanged.", ctx.depth, destType)
		}
		return nil
	}

	// --- Prepare Destination Map ---
	if dest.IsNil() || sc.overwrite {
		// Create a new map. Use size hint from source.
		newMap := reflect.MakeMapWithSize(destType, srcLen)
		sc.debug(" -> Created/Replaced dest map %s with size hint %d", destType, srcLen)
		dest.Set(newMap)
		// If overwrite=true and dest was not nil, the old map is now garbage.
	} else {
		// Destination map exists, overwrite is false. We will add/update keys recursively.
		sc.debug(" -> Dest map exists, overwrite=false. Will merge keys recursively.", ctx.depth)
	}

	// If source map is empty, we're done.
	if src.Len() == 0 {
		return nil
	}

	// --- Element-by-Element Copy ---
	sc.debug(" -> Iterating through source map keys for %s", srcType)
	iter := src.MapRange()
	destKeyType := destType.Key()
	destValType := destType.Elem()

	for iter.Next() {
		srcKey := iter.Key()
		srcVal := iter.Value()

		sc.debug("    - Processing source key: %v (%s)", srcKey.Interface(), srcKey.Type())

		// --- Handle Invalid Source Value ---
		// Can happen? Maybe if map contains invalid values somehow.
		if !srcVal.IsValid() {
			sc.debug("     - Source value for key %v is invalid. Skipping.", srcKey.Interface())
			continue
		}

		// --- Skip Source Value Checks (Nil/Zero/Empty) ---
		// These checks apply to the *value* being copied.
		if sc.skipNilSrc && (srcVal.Kind() == reflect.Ptr || srcVal.Kind() == reflect.Interface || srcVal.Kind() == reflect.Map || srcVal.Kind() == reflect.Slice || srcVal.Kind() == reflect.Chan || srcVal.Kind() == reflect.Func) && srcVal.IsNil() {
			sc.debug("     - Skipping key %v: skipNilSrc=true and value is nil.", srcKey.Interface())
			continue
		}
		if sc.skipZeroValue && srcVal.IsZero() {
			sc.debug("     - Skipping key %v: skipZeroValue=true and value is zero.", srcKey.Interface())
			continue
		}
		if sc.skipEmptyValue && isEmptyValue(srcVal) {
			sc.debug("     - Skipping key %v: skipEmptyValue=true and value is empty.", srcKey.Interface())
			continue
		}

		// --- Prepare Destination Key ---
		// Destination key must be of destKeyType. We need to copy/convert srcKey.
		destKey := reflect.New(destKeyType).Elem() // Create settable holder for dest key
		sc.debug("     - Recursively copying source key %s to dest key %s", srcKey.Type(), destKeyType)
		if err := sc.copyRecursive(destKey, srcKey, ctx); err != nil {
			// Failed to copy/convert the key itself
			keyErr := fmt.Errorf("copying map key (%v :: %s -> %s): %w", srcKey.Interface(), srcKey.Type(), destKeyType, err)
			if sc.handlePotentialError(keyErr, ctx) {
				continue // Skip this key/value pair
			}
			return keyErr
		}
		// Now destKey holds the key suitable for the destination map.

		// --- Overwrite Check for Key ---
		if !sc.overwrite {
			existingVal := dest.MapIndex(destKey)
			if existingVal.IsValid() && !isEmptyValue(existingVal) {
				sc.debug("     - Skipping key %v: overwrite=false and key exists with non-empty value in dest map.", destKey.Interface())
				continue // Skip this key/value pair
			}
		}

		// --- Prepare Destination Value ---
		destValue := reflect.New(destValType).Elem() // Create settable holder for dest value
		sc.debug("     - Recursively copying source value %s to dest value %s for key %v", srcVal.Type(), destValType, destKey.Interface())
		if err := sc.copyRecursive(destValue, srcVal, ctx); err != nil {
			// Failed to copy/convert the value
			valErr := fmt.Errorf("copying map value for key (%v :: %s -> %s): %w", destKey.Interface(), srcVal.Type(), destValType, err)
			if sc.handlePotentialError(valErr, ctx) {
				continue // Skip this key/value pair
			}
			return valErr
		}

		// --- Set Map Index ---
		sc.debug("     - Setting dest map key %v (%s) to value (%s)", destKey.Interface(), destKey.Type(), destValue.Type())
		dest.SetMapIndex(destKey, destValue)
	}

	return nil // Map copy successful
}

func (sc *SmartCopier) copyInterface(dest, src reflect.Value, ctx *mmContext) error {
	destType := dest.Type() // Interface type (e.g., interface{}, io.Writer)
	srcType := src.Type()   // Could be interface or concrete type
	sc.debug("Depth %d: copyInterface: Dest: %s <- Src: %s (%s)", ctx.depth, destType, srcType, src.Kind())

	// --- Basic Checks ---
	if !dest.CanSet() {
		err := fmt.Errorf("cannot set destination interface %s", destType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	// --- Handle Nil/Invalid Source ---
	// Check original source `src` directly
	if !src.IsValid() || ((src.Kind() == reflect.Interface || src.Kind() == reflect.Ptr) && src.IsNil()) {
		if sc.skipNilSrc {
			sc.debug(" -> Source is nil/invalid interface/ptr, skipNilSrc=true. Skipping interface set.", ctx.depth)
			return nil
		}
		if sc.overwrite {
			sc.debug(" -> Source is nil/invalid, overwrite=true. Setting dest interface %s to nil.", ctx.depth, destType)
			dest.Set(reflect.Zero(destType)) // Set interface itself to nil
		} else {
			sc.debug(" -> Source is nil/invalid, overwrite=false. Leaving dest interface %s unchanged.", ctx.depth, destType)
		}
		return nil
	}

	// --- Get Concrete Source Value ---
	srcConcreteVal := src
	if src.Kind() == reflect.Interface {
		srcConcreteVal = src.Elem()
		// Check if concrete value inside interface is valid
		if !srcConcreteVal.IsValid() {
			if sc.skipNilSrc {
				sc.debug(" -> Source interface contains invalid value, skipNilSrc=true. Skipping.", ctx.depth)
				return nil
			}
			if sc.overwrite {
				sc.debug(" -> Source interface contains invalid value, overwrite=true. Setting dest interface %s to nil.", ctx.depth, destType)
				dest.Set(reflect.Zero(destType))
			} else {
				sc.debug(" -> Source interface contains invalid value, overwrite=false. Leaving dest interface %s unchanged.", ctx.depth, destType)
			}
			return nil
		}
		// Handle case where interface contains nil pointer/map/slice etc.
		concreteKind := srcConcreteVal.Kind()
		if (concreteKind == reflect.Ptr || concreteKind == reflect.Map || concreteKind == reflect.Slice || concreteKind == reflect.Chan || concreteKind == reflect.Func) && srcConcreteVal.IsNil() {
			if sc.skipNilSrc {
				sc.debug(" -> Source interface contains nil pointer/map/slice etc., skipNilSrc=true. Skipping.", ctx.depth)
				return nil
			}
			if sc.overwrite {
				sc.debug(" -> Source interface contains nil pointer/map/slice etc., overwrite=true. Setting dest interface %s to nil.", ctx.depth, destType)
				dest.Set(reflect.Zero(destType))
			} else {
				sc.debug(" -> Source interface contains nil pointer/map/slice etc., overwrite=false. Leaving dest interface %s unchanged.", ctx.depth, destType)
			}
			return nil
		}

	}
	srcConcreteType := srcConcreteVal.Type()
	sc.debug(" -> Concrete source value type: %s", srcConcreteType)

	// --- Skip Concrete Value Checks (Zero/Empty) ---
	if sc.skipZeroValue && srcConcreteVal.IsZero() {
		sc.debug(" -> Skipping: skipZeroValue=true and concrete source value is zero.", ctx.depth)
		return nil
	}
	if sc.skipEmptyValue && isEmptyValue(srcConcreteVal) {
		sc.debug(" -> Skipping: skipEmptyValue=true and concrete source value is empty.", ctx.depth)
		return nil
	}

	// --- Overwrite Check ---
	// If not overwriting, check if the destination interface already holds a non-nil value.
	if !sc.overwrite && dest.IsValid() && !dest.IsNil() {
		sc.debug(" -> Skipping interface set: overwrite=false and destination interface %s is non-nil.", destType)
		return nil
	}

	// --- Direct Assignment Check ---
	// Can the concrete source value be assigned directly to the destination interface type?
	// This checks if the concrete type implements the interface (or if dest is interface{}).
	if srcConcreteType.AssignableTo(destType) {
		sc.debug(" -> Concrete source type %s is assignable to dest interface %s. Assigning directly.", srcConcreteType, destType)
		dest.Set(srcConcreteVal)
		return nil
	}
	// What if the source is *T and the interface requires T? Or vice versa?
	// Check pointer assignability as well.
	if srcConcreteVal.CanAddr() && srcConcreteVal.Addr().Type().AssignableTo(destType) {
		// If pointer to concrete value is assignable (e.g., interface needs *T, source is T)
		sc.debug(" -> Pointer to concrete source type %s is assignable to dest interface %s. Assigning address.", srcConcreteVal.Addr().Type(), destType)
		dest.Set(srcConcreteVal.Addr())
		return nil
	}
	if srcConcreteVal.Kind() == reflect.Ptr && !srcConcreteVal.IsNil() && srcConcreteVal.Elem().Type().AssignableTo(destType) {
		// If element of concrete pointer value is assignable (e.g., interface needs T, source is *T)
		sc.debug(" -> Element of concrete source pointer type %s is assignable to dest interface %s. Assigning element.", srcConcreteType, destType)
		dest.Set(srcConcreteVal.Elem())
		return nil
	}

	// --- Copy into a New Holder ---
	// If direct assignment isn't possible, or if we want a deep copy,
	// we create a new value of the *source concrete type*, copy into it,
	// and then try to assign that holder to the destination interface.
	// This preserves the original concrete type within the interface but creates a copy of the data.
	sc.debug(" -> Direct assignment failed. Creating new holder of type %s for deep copy.", srcConcreteType)
	holder := reflect.New(srcConcreteType).Elem() // Create settable instance of source concrete type

	sc.debug(" -> Recursively copying concrete source %s into holder %s.", srcConcreteType, holder.Type())
	if err := sc.copyRecursive(holder, srcConcreteVal, ctx); err != nil {
		// Failed to copy data into the holder
		copyErr := fmt.Errorf("copying concrete value (%s) into interface holder (%s): %w", srcConcreteType, holder.Type(), err)
		// Error handled by copyRecursive or needs bubbling up
		// No need for handlePotentialError here as copyRecursive already did it.
		return copyErr
	}

	// --- Assign Holder to Destination Interface ---
	// Now try assigning the populated holder to the destination interface.
	if holder.Type().AssignableTo(destType) {
		sc.debug(" -> Assigning holder (%s) to destination interface %s.", holder.Type(), destType)
		dest.Set(holder)
	} else if holder.CanAddr() && holder.Addr().Type().AssignableTo(destType) {
		// Assign pointer to holder if that fits the interface
		sc.debug(" -> Assigning address of holder (%s) to destination interface %s.", holder.Addr().Type(), destType)
		dest.Set(holder.Addr())
	} else {
		// This shouldn't typically happen if the original srcConcreteVal was assignable,
		// but could occur if copyRecursive somehow changed the holder type incompatibly,
		// or if the original assignability check missed a case.
		err := fmt.Errorf("cannot assign copied holder type %s to destination interface %s", holder.Type(), destType)
		if sc.handlePotentialError(err, ctx) {
			return nil
		}
		return err
	}

	return nil // Interface copy successful
}

func (sc *SmartCopier) copyArray(dest, src reflect.Value, ctx *mmContext) error {
	destType := dest.Type()
	srcType := src.Type()
	destLen := dest.Len()
	srcLen := src.Len()
	sc.debug("Depth %d: copyArray: Dest: %s (len %d) <- Src: %s (len %d)", ctx.depth, destType, destLen, srcType, srcLen)

	// --- Basic Checks ---
	if destLen != srcLen {
		err := fmt.Errorf("%w: array length mismatch (dest %d != src %d)", ErrTypeMismatch, destLen, srcLen)
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

	// --- Optimization: MemCopy ---
	// Use direct memory copy if element types are identical and suitable.
	// Note: Array elements *are* addressable directly.
	destElemType := destType.Elem()
	if sc.useUnsafe && destType == srcType && isMemCopySuitable(destElemType) {
		// Get type info for size (might require caching array types too)
		// For now, calculate size directly: elemSize * length
		elemSize := destElemType.Size()
		totalSize := elemSize * uintptr(destLen)
		if totalSize > 0 && dest.CanAddr() && src.CanAddr() {
			sc.debug(" -> Attempting unsafe memory copy for array %s (size %d)", destType, totalSize)
			destPtr := unsafe.Pointer(dest.UnsafeAddr())
			srcPtr := unsafe.Pointer(src.UnsafeAddr())
			memmove(destPtr, srcPtr, totalSize)
			runtime.KeepAlive(dest.Interface())
			runtime.KeepAlive(src.Interface())
			return nil // Memcopy successful
		}
	}

	// --- Element-by-Element Copy ---
	sc.debug(" -> Performing element-by-element copy for array %s", destType)
	for i := 0; i < srcLen; i++ {
		srcElem := src.Index(i)
		destElem := dest.Index(i)

		// Destination element settability check (arrays elements are usually settable if array is)
		if !destElem.CanSet() {
			err := fmt.Errorf("cannot set array element at index %d in %s", i, destType)
			if sc.handlePotentialError(err, ctx) {
				continue // Skip this element
			}
			return err
		}

		// --- Overwrite Check for Element ---
		if !sc.overwrite && destElem.IsValid() && !isEmptyValue(destElem) {
			sc.debug("    - Skipping element %d: overwrite=false and dest element is non-empty.", i)
			continue
		}

		// --- Recursive Copy for Element ---
		sc.debug("    - Recursively copying element %d", i)
		if err := sc.copyRecursive(destElem, srcElem, ctx); err != nil {
			elemErr := fmt.Errorf("copying array index %d: %w", i, err)
			if sc.handlePotentialError(elemErr, ctx) {
				continue // Error collected, continue
			}
			return elemErr // Return immediately
		}
	}
	return nil // Array copy successful
}

// tryConvert attempts to convert src value to destType using custom converters or Go's built-in Convert().
func (sc *SmartCopier) tryConvert(src reflect.Value, destType reflect.Type) (reflect.Value, error) {
	if !src.IsValid() {
		// Converting invalid value doesn't make sense
		return reflect.Value{}, fmt.Errorf("cannot convert invalid source value")
	}
	srcType := src.Type()

	// 1. Check for Custom Converter
	if destConvMap, ok := sc.typeConverters[srcType]; ok {
		if converter, ok := destConvMap[destType]; ok {
			sc.debug(" -> Using custom converter: %s -> %s", srcType, destType)
			converted, err := converter(src)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("custom converter %s -> %s failed: %w", srcType, destType, err)
			}
			// Check if converter returned nil/invalid for non-nillable types
			if !converted.IsValid() {
				kind := destType.Kind()
				isNillable := kind == reflect.Ptr || kind == reflect.Map || kind == reflect.Slice ||
					kind == reflect.Interface || kind == reflect.Chan || kind == reflect.Func
				if !isNillable {
					return reflect.Value{}, fmt.Errorf("custom converter %s -> %s returned invalid/nil value for non-nillable type %s", srcType, destType, destType)
				}
				// Return zero value for nillable types if converter returned nil
				return reflect.Zero(destType), nil
			}
			// Check if returned type is assignable or convertible to the target type
			if converted.Type().AssignableTo(destType) {
				sc.debug("  -> Custom converter returned assignable type %s", converted.Type())
				return converted, nil
			}
			if converted.Type().ConvertibleTo(destType) {
				sc.debug("  -> Custom converter returned convertible type %s, converting to %s", converted.Type(), destType)
				return converted.Convert(destType), nil
			}
			return reflect.Value{}, fmt.Errorf("custom converter %s -> %s returned incompatible type %s", srcType, destType, converted.Type())
		}
	}

	// 2. Check Go's Built-in Convertibility
	if srcType.ConvertibleTo(destType) {
		sc.debug(" -> Using Go's built-in conversion: %s -> %s", srcType, destType)
		return src.Convert(destType), nil
	}

	// 3. Handle Conversion from Interface's Concrete Type
	// If src is an interface containing a value, try converting the concrete value.
	if src.Kind() == reflect.Interface && !src.IsNil() {
		srcElem := src.Elem()
		if srcElem.IsValid() && srcElem.Type() != srcType { // Avoid infinite recursion if Elem gives same type
			sc.debug(" -> Retrying conversion from concrete type %s (within interface %s) to %s", srcElem.Type(), srcType, destType)
			// Recurse using tryConvert, not copyRecursive
			return sc.tryConvert(srcElem, destType)
		}
	}

	// 4. Handle Conversion Between Pointer Types (by converting elements)
	// If src is *S and dest is *D, try converting S to D and wrapping in *D.
	if src.Kind() == reflect.Ptr && !src.IsNil() && destType.Kind() == reflect.Ptr {
		srcElem := src.Elem()
		destElemType := destType.Elem()
		if srcElem.IsValid() {
			sc.debug(" -> Attempting pointer element conversion: %s -> %s", srcElem.Type(), destElemType)
			// Try converting the element
			convertedElem, err := sc.tryConvert(srcElem, destElemType)
			if err == nil && convertedElem.IsValid() {
				// Conversion successful, create new pointer of destination type
				newDestPtr := reflect.New(destElemType)
				newDestPtr.Elem().Set(convertedElem) // Set the converted value
				sc.debug("  -> Pointer element conversion successful, returning new %s", newDestPtr.Type())
				return newDestPtr, nil
			}
			sc.debug("  -> Pointer element conversion failed: %v", err)
		}
	}

	// No suitable conversion found
	sc.debug(" -> No custom or built-in converter found for %s -> %s", srcType, destType)
	return reflect.Value{}, fmt.Errorf("%w: from %s to %s", ErrNoConverter, srcType, destType)
}

// --- Utility Functions ---

// debug logs messages if debugMode is enabled.
func (sc *SmartCopier) debug(format string, args ...interface{}) {
	if sc.debugMode {
		// Add prefix for clarity
		log.Printf("[mm debug] "+format, args...)
	}
}

// memmove is assumed to be defined elsewhere (e.g., unsafe.go) using linkname or cgo.
// If not, uncommenting the linkname below might work, but risks conflicts/brittleness.
// //go:linkname memmove runtime.memmove
// //go:noescape
// func memmove(to, from unsafe.Pointer, n uintptr)

// Removed the memmove definition from here to resolve the "redeclared" error.
// Ensure it's defined in exactly one place accessible to this package, likely via linkname in a separate `unsafe_*.go` file.
