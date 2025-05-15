package mm

import (
	"errors"
	"github.com/olekukonko/ll"
	"github.com/olekukonko/ll/lx"
	"reflect"
)

// Error Definitions
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
	ErrReadWriterCopy      = errors.New("failed to copy io.ReadWriter data")
	ErrSensitiveTypeCopied = errors.New("sensitive type copied without custom logic")
)

// Default Copier and Package-Level Functions
var defaultCopier = NewCopier()

var logger = ll.New("mm").Level(lx.LevelDebug)

// Copiable defines an interface for types that handle their own field-level copying logic.
// The Copy method is called for individual fields of a struct when the field's type implements this interface.
// It receives a CopyContext with field metadata and settings, and a source value to transform.
// The method returns the transformed value and an error, setting ctx.Overwrite to indicate whether the value should replace the destination.
// Example: A Hello type (type Hello string) might implement Copy to encode its value to base64, setting ctx.Overwrite = true and returning the encoded string.
// Types implementing Copiable may also implement Sensitivity to indicate they require custom copying for security.
type Copiable interface {
	Copy(ctx *CopyContext, src interface{}) (value interface{}, err error)
}

// Sensitivity defines an interface for types that are sensitive and require custom copying to prevent accidental exposure of raw values.
// The Sensitive method returns true if the type is sensitive, signaling the copier to enforce custom copying (e.g., via Copiable, MarshalCopier, or UnmarshalCopier).
// Example: A Hello type might implement Sensitive to return true, ensuring its string value is only copied as a base64-encoded string.
type Sensitivity interface {
	Sensitive() bool
}

// CopiableDeprecated defines an interface for types that handle their own type-level copying logic.
// Deprecated: Use the new Copiable interface for field-level copying or MarshalCopier/UnmarshalCopier for type-level transformations.
type CopiableDeprecated interface {
	// Copy receives the original source value (before any potential dereferencing by CopyRecursive)
	// and the active Copier instance.
	// The receiver of this method should populate itself based on the src value.
	// It can use c.CopyRecursive for nested fields or access c's configuration.
	// Returning an error will halt the copy process unless the Copier is configured
	// to collect Errors.
	Copy(c *Copier, src reflect.Value) error
}

// MarshalCopier defines an interface for types that serialize themselves into a format for copying.
// The MarshalCopy method is called for the entire type (e.g., struct, map, slice, array) when the source implements this interface.
// It returns a serialized form (e.g., map[string]interface{}) that the copier can process.
// Example: A struct might implement MarshalCopy to flatten itself into a map for storage in a key-value store.
// Types implementing MarshalCopier may also implement Sensitivity for security.
type MarshalCopier interface {
	MarshalCopy(ctx *CopyContext) (interface{}, error)
}

// UnmarshalCopier defines an interface for types that deserialize data into themselves during copying.
// The UnmarshalCopy method is called for the entire type (e.g., struct, map, slice, array) when the destination implements this interface.
// It populates the receiver from the provided data (e.g., map[string]interface{}).
// Example: A struct might implement UnmarshalCopy to populate its fields from a key-value store map.
// Types implementing UnmarshalCopier may also implement Sensitivity for security.
type UnmarshalCopier interface {
	UnmarshalCopy(ctx *CopyContext, data interface{}) error
}

// CopyContext provides metadata and settings for custom copying operations.
// It is used by Copiable, MarshalCopier, and UnmarshalCopier to access field information, source data, and copier settings.
// The Overwrite field is set by the Copy method to indicate whether the destination value should be replaced with the result of the copy operation.
type CopyContext struct {
	FieldName string      // The name of the field being copied (empty for type-level operations).
	Source    interface{} // Optional source for external data (e.g., DataSource for Consul queries).
	Settings  struct {    // Simplified copier settings.
		Overwrite, SkipEmpty, SkipZero, SkipNil bool
	}
	Overwrite bool                    // Indicates whether the destination should be overwritten.
	SetValue  func(interface{}) error // Assigns the value to the destination (optional for Copiable).
	Copier    *Copier                 // Reference to the active Copier for recursive calls or configuration.
}

type SysContext struct {
	depth    int
	Detector *cycleDetector
	Visited  map[VisitKey]reflect.Value
	Errors   []error
}
type TypeInfo struct {
	fields                                       map[string]FieldInfo
	unmappedField                                *FieldInfo
	size                                         uintptr
	canMemCopy                                   bool
	alignment                                    uintptr
	containsPtrs                                 bool
	isStruct, isSlice, isMap, isPtr, isInterface bool
}
type FieldInfo struct {
	name        string
	structField reflect.StructField
	index       []int
	offset      uintptr
	typ         reflect.Type
	tagOpts     map[string]string
	sensitive   bool
}
