package copier

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/olekukonko/ll"
	"github.com/olekukonko/mm/cycle"
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
		logger:          ll.New("copier"),
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
func NewCopier(options ...Option) *Copier {
	c := &Copier{
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
		logger:            ll.New("copier"),
	}

	for _, opt := range options {
		opt(c)
	}

	return c

}

// AddFallbackTagKeys appends to the list of fallback tag keys.
func (c *Copier) AddFallbackTagKeys(keys ...string) *Copier {
	c.fallbackTagKeys = append(c.fallbackTagKeys, keys...)
	c.typeInfoCache = sync.Map{}
	return c
}

// Copy copies data from src into dest. dest must be a non-nil pointer.
func (c *Copier) Copy(dest interface{}, src interface{}) error {
	c.logger.Debugf("Copier.Copy started. Overwrite: %v, Debug: %v", c.overwrite, c.debugMode)
	if dest == nil {
		return c.FinalizeResult(ErrDestinationIsNil, &SysContext{Errors: make([]error, 0)})
	}
	destVal := reflect.ValueOf(dest)

	if destVal.Kind() != reflect.Ptr || destVal.IsNil() {
		return c.FinalizeResult(ErrDestNotPointer, &SysContext{Errors: make([]error, 0)})
	}

	if src == nil {
		if c.skipNilSrc {
			c.logger.Debugf("Copy: Source is nil, skipping (skipNilSrc=true).")
			return nil
		}
		c.logger.Debugf("Copy: Source is nil (skipNilSrc=false, overwrite=%v). Proceeding to CopyRecursive (will zero dest or error).", c.overwrite)
	} else {
		c.logger.Debugf("Copy: Source is non-nil. Type: %T", src)
	}

	destElem := destVal.Elem()
	if !destElem.CanSet() {
		canPotentiallySet := c.useUnsafe && destElem.CanAddr()
		if !canPotentiallySet {
			err := fmt.Errorf("destination element (type %s) is not settable and unsafe cannot help", destElem.Type())
			return c.FinalizeResult(err, &SysContext{Errors: make([]error, 0)})
		}
		c.logger.Debugf("Destination element %s not settable via standard means, relying on unsafe.", destElem.Type())
	}

	ctx := &SysContext{depth: 0, Visited: make(map[cycle.VisitKey]reflect.Value), Errors: make([]error, 0), Detector: cycle.New(cycle.WithLogger(c.logger))}

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
			c.logger.Debugf("Copier.Copy: Priming cycle Detector for top-level object: %p (%s) -> %p (%s)",
				unsafe.Pointer(srcReflectVal.Pointer()), srcReflectVal.Type(),
				unsafe.Pointer(destVal.Pointer()), destVal.Type())
			ctx.Detector.Track(srcReflectVal.Pointer(), srcReflectVal.Type().Elem(), destVal)
		}
	}

	err := c.CopyRecursive(destElem, srcReflectVal, ctx)

	finalErr := c.FinalizeResult(err, ctx)
	if finalErr != nil && c.silentErrors {
		c.logger.Debugf("Copy: Error occurred but suppressed due to silentErrors=true: %v", finalErr)
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

	c.logger.Debugf("Copier.Merge called. Effective copier config for merge -> Overwrite: %v, Debug: %v", tempCopier.overwrite, tempCopier.debugMode)
	return tempCopier.Copy(dest, src)
}

// CopyCat copies data from the first source using standard Copy logic,
// then merges data from subsequent sources using Merge logic (overwrite=false).
func (c *Copier) CopyCat(dest interface{}, sources ...interface{}) error {
	if len(sources) == 0 {
		return nil
	}

	if dest == nil {
		return c.FinalizeResult(ErrDestinationIsNil, &SysContext{Errors: make([]error, 0)})
	}
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr || destVal.IsNil() {
		return c.FinalizeResult(ErrDestNotPointer, &SysContext{Errors: make([]error, 0)})
	}
	destElem := destVal.Elem()
	if !destElem.CanSet() {
		canPotentiallySet := c.useUnsafe && destElem.CanAddr()
		if !canPotentiallySet {
			err := fmt.Errorf("destination element (type %s) is not settable and unsafe cannot help", destElem.Type())
			return c.FinalizeResult(err, &SysContext{Errors: make([]error, 0)})
		}
	}

	ctx := &SysContext{depth: 0, Visited: make(map[cycle.VisitKey]reflect.Value), Errors: make([]error, 0), Detector: cycle.New(cycle.WithLogger(c.logger))}
	firstCopier := *c
	mergeCopier := *c
	mergeCopier.overwrite = false
	mergeCopier.merging = true
	mergeCopier.debugMode = c.debugMode

	var overallErr error

	c.logger.Debugf("CopyCat: Processing first source...")
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
			ctx.Detector.Track(firstSrcReflectVal.Pointer(), firstSrcReflectVal.Type().Elem(), destVal)
		}
	}

	err := firstCopier.CopyRecursive(destElem, firstSrcReflectVal, ctx)
	if err != nil {
		if firstCopier.handlePotentialError(err, ctx) {
			c.logger.Debugf("CopyCat: Error collected from first source, continuing...")
		} else {
			overallErr = err
			if !firstCopier.collectErrors {
				c.logger.Debugf("CopyCat: Error from first source, stopping (collectErrors=false).")
				return c.FinalizeResult(overallErr, ctx)
			}
		}
	}

	if len(sources) > 1 {
		c.logger.Debugf("CopyCat: Processing subsequent sources for merge...")
		for i := 1; i < len(sources); i++ {
			c.logger.Debugf("CopyCat: Merging source %d...", i+1)
			currentSrcVal := reflect.ValueOf(sources[i])
			err := mergeCopier.CopyRecursive(destElem, currentSrcVal, ctx)
			if err != nil {
				if mergeCopier.handlePotentialError(err, ctx) {
					c.logger.Debugf("CopyCat: Error collected from source %d, continuing...", i+1)
				} else {
					if overallErr == nil {
						overallErr = err
					}
					if !mergeCopier.collectErrors {
						c.logger.Debugf("CopyCat: Error from source %d, stopping (collectErrors=false).", i+1)
						break
					}
				}
			}
		}
	}

	finalErr := c.FinalizeResult(overallErr, ctx)
	if finalErr != nil && c.silentErrors {
		c.logger.Debugf("CopyCat: Error occurred but suppressed due to silentErrors=true: %v", finalErr)
		return nil
	}
	return finalErr
}

// Deep creates a deep copy of the input value v.
// It returns a new value independent of the original.
func (c *Copier) Deep(v interface{}) (interface{}, error) {
	c.logger.Debugf("Copier.Deep called for type %T", v)
	if v == nil {
		if c.skipNilSrc {
			return nil, nil
		}
		errToRet := ErrSourceIsNil
		return nil, c.FinalizeResult(errToRet, &SysContext{Errors: make([]error, 0)})
	}
	srcVal := reflect.ValueOf(v)
	if !srcVal.IsValid() {
		err := fmt.Errorf("cannot deep copy an invalid source value (was not nil, but reflect.Value is invalid)")
		return nil, c.FinalizeResult(err, &SysContext{Errors: make([]error, 0)})
	}

	destType := srcVal.Type()
	destVal := reflect.New(destType).Elem()

	ctx := &SysContext{depth: 0, Visited: make(map[cycle.VisitKey]reflect.Value), Errors: make([]error, 0), Detector: cycle.New(cycle.WithLogger(c.logger))}

	// For Deep(), avoid pre-tracking the top-level source pointer if it's a special type
	// like *bytes.Buffer, as these have dedicated handlers in CopyRecursive that
	// will perform the correct allocation and setting. Pre-tracking can cause the
	// cycle Detector to short-circuit to the initially nil destVal.
	isSpecialPointerType := false
	if c.enableCycleDetect && srcVal.Kind() == reflect.Ptr && !srcVal.IsNil() {
		elemType := srcVal.Type().Elem()
		if elemType == reflect.TypeOf(bytes.Buffer{}) || elemType == reflect.TypeOf(strings.Builder{}) {
			isSpecialPointerType = true
		}
		if !isSpecialPointerType {
			// Track src pointer, its element type, to the new destination pointer
			// (which is destVal itself if destType is a Ptr)
			ctx.Detector.Track(srcVal.Pointer(), elemType, destVal)
		}
	}

	err := c.CopyRecursive(destVal, srcVal, ctx)

	finalErr := c.FinalizeResult(err, ctx)
	if finalErr != nil {
		if c.silentErrors {
			c.logger.Debugf("Deep: Error occurred but suppressed due to silentErrors=true: %v", finalErr)
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

func (c *Copier) FinalizeResult(returnedErr error, ctx *SysContext) error {
	if c.collectErrors && len(ctx.Errors) > 0 {
		finalErr := errors.Join(ctx.Errors...)
		if c.panicOnError {
			panic(fmt.Sprintf("mm copy failed with %d error(s): %v", len(ctx.Errors), finalErr))
		}
		return finalErr
	}
	if returnedErr != nil {
		if c.panicOnError {
			panic(fmt.Sprintf("mm copy failed: %v", returnedErr))
		}
		return returnedErr
	}
	return nil
}

func (c *Copier) DebugMode() bool {
	return c.debugMode
}

func (c *Copier) SkipNilSrc() bool {
	return c.skipNilSrc
}

func (c *Copier) CycleDetect() bool {
	return c.enableCycleDetect
}

func (c *Copier) Logger() *ll.Logger {
	return c.logger
}
