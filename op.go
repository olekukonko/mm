// op.go
package mm

import (
	"github.com/olekukonko/ll"
	"github.com/olekukonko/ll/lx"
	"log" // Keep standard log for this specific warning
	"reflect"
	"sync"
)

type Option func(*Copier)

// WithTag sets the primary tag name for struct field parsing.
func WithTag(tag string) Option {
	return func(c *Copier) {
		c.tagName = tag
		c.typeInfoCache = sync.Map{}
	}
}

// WithFieldMapping adds a field mapping from source to destination field name.
func WithFieldMapping(src, dst string) Option {
	return func(c *Copier) {
		if c.fieldMappings == nil {
			c.fieldMappings = make(map[string]string)
		}
		c.fieldMappings[src] = dst
	}
}

// WithSkip adds fields to skip during copying.
func WithSkip(fields ...string) Option {
	return func(c *Copier) {
		if c.skipFields == nil {
			c.skipFields = make(map[string]struct{})
		}
		for _, f := range fields {
			c.skipFields[f] = struct{}{}
		}
	}
}

// WithFormatter adds a formatter for a specific field.
func WithFormatter(field string, fn func(interface{}) interface{}) Option {
	return func(c *Copier) {
		if c.formatters == nil {
			c.formatters = make(map[string]func(interface{}) interface{})
		}
		c.formatters[field] = fn
	}
}

// WithMaxDepth sets the maximum recursion depth.
func WithMaxDepth(depth int) Option {
	return func(c *Copier) {
		c.maxDepth = depth
	}
}

// WithMerge enables merging instead of overwriting.
func WithMerge() Option {
	return func(c *Copier) {
		c.overwrite = false
		c.merging = true
	}
}

// WithSkipZero skips zero-valued source fields.
func WithSkipZero(skip bool) Option {
	return func(c *Copier) {
		c.skipZeroValue = skip
	}
}

// WithSkipEmpty skips empty-valued source fields.
func WithSkipEmpty(skip bool) Option {
	return func(c *Copier) {
		c.skipEmptyValue = skip
	}
}

// WithSkipNil skips nil source values.
func WithSkipNil(skip bool) Option {
	return func(c *Copier) {
		c.skipNilSrc = skip
	}
}

// WithSkipCycle disables cycle detection if skip is true.
func WithSkipCycle(skip bool) Option {
	return func(c *Copier) {
		c.enableCycleDetect = !skip
	}
}

// WithSkipUnexported skips unexported fields if skip is true.
func WithSkipUnexported(skip bool) Option {
	return func(c *Copier) {
		c.copyUnexported = !skip
	}
}

// WithSkipUnsafe disables unsafe operations if skip is true.
func WithSkipUnsafe(skip bool) Option {
	return func(c *Copier) {
		c.useUnsafe = !skip
		if !c.useUnsafe {
			c.copyUnexported = false // Cannot copy unexported without unsafe
		}
		c.typeInfoCache = sync.Map{}
	}
}

// WithSkipErrors disables error collection if skip is true.
func WithSkipErrors(skip bool) Option {
	return func(c *Copier) {
		c.collectErrors = !skip
	}
}

// WithSkipPanic enables panic on error if skip is false.
func WithSkipPanic(skip bool) Option {
	return func(c *Copier) {
		c.panicOnError = !skip
	}
}

// WithSkipSilent enables silent errors if skip is false.
func WithSkipSilent(skip bool) Option {
	return func(c *Copier) {
		c.silentErrors = !skip
	}
}

// WithSkipDebug enables debug mode if skip is false.
func WithSkipDebug(skip bool) Option {
	return func(c *Copier) {
		c.debugMode = !skip
	}
}

// WithTagResolution enables or disables tag resolution.
func WithTagResolution(use bool) Option {
	return func(c *Copier) {
		c.tagResolution = use
		c.typeInfoCache = sync.Map{}
	}
}

// WithFallbackTagKeys sets the fallback tag keys.
func WithFallbackTagKeys(keys ...string) Option {
	return func(c *Copier) {
		if len(keys) == 0 {
			c.fallbackTagKeys = []string{}
		} else {
			c.fallbackTagKeys = keys
		}
		c.typeInfoCache = sync.Map{}
	}
}

// WithFallbackTagKeysFrom appends fallback tag keys.
func WithFallbackTagKeysFrom(keys ...string) Option {
	return func(c *Copier) {
		c.fallbackTagKeys = append(c.fallbackTagKeys, keys...)
		c.typeInfoCache = sync.Map{}
	}
}

// WithErrors enables or disables error collection.
func WithErrors(collect bool) Option {
	return func(c *Copier) {
		c.collectErrors = collect
	}
}

// WithUnsafe enables or disables unsafe operations.
func WithUnsafe(enable bool) Option {
	return func(c *Copier) {
		c.useUnsafe = enable
		if !enable {
			// If unsafe is disabled, copyUnexported MUST be disabled.
			// Emit warning if user explicitly enabled copyUnexported before disabling unsafe.
			if c.copyUnexported {
				log.Println("[mm warning] WithUnsafe(false) called after WithUnexported(true). Unexported fields cannot be copied without unsafe access.")
			}
			c.copyUnexported = false
		}
		c.typeInfoCache = sync.Map{}
	}
}

// WithCycleDetection enables or disables cycle detection.
func WithCycleDetection(enable bool) Option {
	return func(c *Copier) {
		c.enableCycleDetect = enable
	}
}

// WithUnexported enables or disables copying of unexported fields.
func WithUnexported(enable bool) Option {
	return func(c *Copier) {
		if enable && !c.useUnsafe {
			// Emit warning when enabling unexported without unsafe being enabled.
			log.Println("[mm warning] WithUnexported(true) requires WithUnsafe(true). Unexported fields will not be copied.")
			c.copyUnexported = false // Force false if unsafe is not enabled
		} else {
			c.copyUnexported = enable
		}
		c.typeInfoCache = sync.Map{} // May affect type info regarding field access
	}
}

// WithTypeConverter adds a type converter for specific source and destination types.
func WithTypeConverter(src, dst reflect.Type, converter func(reflect.Value) (reflect.Value, error)) Option {
	return func(c *Copier) {
		if c.typeConverters == nil {
			c.typeConverters = make(map[reflect.Type]map[reflect.Type]func(reflect.Value) (reflect.Value, error))
		}
		if c.typeConverters[src] == nil {
			c.typeConverters[src] = make(map[reflect.Type]func(reflect.Value) (reflect.Value, error))
		}
		c.typeConverters[src][dst] = converter
	}
}

// WithPanic enables or disables panic on error.
func WithPanic(panicOnError bool) Option {
	return func(c *Copier) {
		c.panicOnError = panicOnError
	}
}

// WithSilentErrors enables or disables silent errors.
func WithSilentErrors(silent bool) Option {
	return func(c *Copier) {
		c.silentErrors = silent
	}
}

// WithSkipSensitive enables skipping sensitive fields/types
func WithSkipSensitive(skip bool) Option {
	return func(copier *Copier) {
		copier.skipSensitive = skip
	}
}

// WithLogger for logging
func WithLogger(l *ll.Logger) Option {
	return func(c *Copier) {
		c.logger = l
	}
}

// WithDebug enables or disables debug logging
func WithDebug(enable bool) Option { // This option configures the logger
	return func(c *Copier) {
		c.debugMode = enable // Keep internal flag for conditional logic if needed
		if c.logger != nil {
			if enable {
				c.logger.Enable()
				c.logger.Level(lx.LevelDebug)
			} else {
				// Keep logger enabled but potentially set to a higher level? Or disable?
				// Disabling might be unexpected if a logger was explicitly provided.
				// Let's just ensure level is not Debug if enable is false.
				if c.logger.GetLevel() == lx.LevelDebug {
					c.logger.Level(lx.LevelInfo) // Default to Info if disabling Debug specifically
				}
				// Or maybe just: c.logger.SetLevel(ll.LevelWarn) or higher?
				// Safest might be to not change level if disabling debug.
			}
		}
	}
}

// Configuration Methods
func (c *Copier) WithTag(tag string) *Copier {
	c.tagName = tag
	c.typeInfoCache = sync.Map{}
	return c
}
func (c *Copier) WithFieldMapping(src, dst string) *Copier {
	c.fieldMappings[src] = dst
	return c
}
func (c *Copier) WithSkip(fields ...string) *Copier {
	for _, f := range fields {
		c.skipFields[f] = struct{}{}
	}
	return c
}
func (c *Copier) WithFormatter(field string, fn func(interface{}) interface{}) *Copier {
	c.formatters[field] = fn
	return c
}
func (c *Copier) WithMaxDepth(depth int) *Copier       { c.maxDepth = depth; return c }
func (c *Copier) WithOverwrite(overwrite bool) *Copier { c.overwrite = overwrite; return c }
func (c *Copier) WithSkipZero(skip bool) *Copier       { c.skipZeroValue = skip; return c }
func (c *Copier) WithSkipEmpty(skip bool) *Copier      { c.skipEmptyValue = skip; return c }
func (c *Copier) WithSkipNil(skip bool) *Copier        { c.skipNilSrc = skip; return c }
func (c *Copier) WithSkipCycle(skip bool) *Copier      { c.enableCycleDetect = !skip; return c }
func (c *Copier) WithSkipUnexported(skip bool) *Copier { c.copyUnexported = !skip; return c }
func (c *Copier) WithSkipUnsafe(skip bool) *Copier     { c.useUnsafe = !skip; return c }
func (c *Copier) WithSkipErrors(skip bool) *Copier     { c.collectErrors = !skip; return c }
func (c *Copier) WithSkipPanic(skip bool) *Copier      { c.panicOnError = !skip; return c }
func (c *Copier) WithSkipSilent(skip bool) *Copier     { c.silentErrors = !skip; return c }
func (c *Copier) WithSkipDebug(skip bool) *Copier      { c.debugMode = !skip; WithDebug(!skip)(c); return c } // Also apply logger setting
func (c *Copier) WithTagResolution(use bool) *Copier {
	c.tagResolution = use
	c.typeInfoCache = sync.Map{} // Clear cache
	return c
}
func (c *Copier) WithFallbackTagKeys(keys ...string) *Copier {
	if len(keys) == 0 {
		c.fallbackTagKeys = []string{} // Clear them if no keys are provided
	} else {
		c.fallbackTagKeys = keys
	}
	c.typeInfoCache = sync.Map{} // Clear cache
	return c
}
func (c *Copier) WithFallbackTagKeysFrom(keys ...string) *Copier {
	c.fallbackTagKeys = append(c.fallbackTagKeys, keys...)
	return c
}
func (c *Copier) WithErrors(collect bool) *Copier { c.collectErrors = collect; return c }
func (c *Copier) WithUnsafe(enable bool) *Copier {
	WithUnsafe(enable)(c) // Call the Option function
	return c
}
func (c *Copier) WithCycleDetection(enable bool) *Copier {
	WithCycleDetection(enable)(c)
	return c
}
func (c *Copier) WithUnexported(enable bool) *Copier {
	WithUnexported(enable)(c) // Call the Option function
	return c
}
func (c *Copier) WithTypeConverter(src, dst reflect.Type, converter func(reflect.Value) (reflect.Value, error)) *Copier {
	WithTypeConverter(src, dst, converter)(c)
	return c
}
func (c *Copier) WithPanic(panicOnError bool) *Copier {
	WithPanic(panicOnError)(c)
	return c
}
func (c *Copier) WithSilentErrors(silent bool) *Copier {
	WithSilentErrors(silent)(c)
	return c
}
func (c *Copier) WithDebug(enable bool) *Copier {
	WithDebug(enable)(c)
	return c
}

func (c *Copier) WithLogger(logger *ll.Logger) *Copier {
	WithLogger(logger)(c)
	return c
}

func (c *Copier) WithSkipSensitive(skip bool) *Copier {
	WithSkipSensitive(skip)(c)
	return c
}
