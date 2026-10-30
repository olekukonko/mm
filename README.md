# mm

`mm` copies and merges Go values. It goes beyond a plain struct decoder:

- **Deep copy** of pointers, slices, maps, interfaces and nested structs.
- **Cycle-safe**: self-referencing graphs are detected and rebuilt instead of
  recursing forever.
- **Merge / CopyCat**: fill a destination from several sources without
  clobbering values that are already set.
- **Conversion-aware**: struct ↔ struct, struct ↔ map, map ↔ struct,
  slice ↔ slice, with numeric/string conversions and custom converters.
- **Copy hooks**: `Copiable`, `MarshalCopier`, `UnmarshalCopier` and
  `Sensitivity` let a type control (or refuse) its own copy — useful for
  secrets that must never be copied verbatim.
- **Tags**: `mm:"Name"`, `mm:"-"`, `mm:",omitempty"`, `mm:",sensitive"`,
  `mm:",unmapped"`, with fallback keys (`json`, `yaml`, `hcl`, ...).
- **Unexported fields** can be copied with `unsafe` when you explicitly opt in.

## Install

```bash
go get github.com/olekukonko/mm
```

Requires Go 1.21+.

## Usage

```go
package main

import (
	"fmt"

	"github.com/olekukonko/mm"
	"github.com/olekukonko/mm/copier"
)

type Source struct {
	Name  string
	Years int    `mm:"Age"`
	ID    string `mm:"-"`
}

type Dest struct {
	Name string
	Age  int
}

func main() {
	src := Source{Name: "Alice", Years: 30, ID: "ignored"}

	// Allocate and decode in one step.
	dst, err := mm.Copy[Dest](src)
	fmt.Println(dst, err) // {Alice 30} <nil>

	// Or decode into an existing value.
	var into Dest
	if err := copier.NewCopier().Copy(&into, src); err != nil {
		panic(err)
	}

	// Deep copy (cycles are handled).
	clone, err := mm.Deep(src)
	fmt.Println(clone, err)
}
```

### Configuring a copier

```go
c := copier.NewCopier(
	copier.WithTag("json"),
	copier.WithSkipZero(true),
	copier.WithSkipSensitive(true),
	copier.WithFieldMapping("FullName", "Name"),
	copier.WithDebug(false),
)
```

`copier.NewCopier()` returns a copier that is safe for concurrent use. Use
`Clone()` to derive a variant (for example to enable panic-on-error) without
mutating the original.

### Struct tags

| Tag | Meaning |
| --- | --- |
| `mm:"Name"` | Copy to/from the field named `Name`. |
| `mm:"-"` | Never copy this field. |
| `mm:",omitempty"` | Skip this field when the source value is empty. |
| `mm:",sensitive"` | Require custom copy logic; see `Sensitivity`. |
| `mm:",unmapped"` | Collect unmatched source keys into this `map[string]any`. |

### Copy hooks

```go
type Secret string

func (s Secret) Sensitive() bool { return true }

func (s Secret) Copy(ctx *copier.CopyContext, src interface{}) (interface{}, error) {
	ctx.Overwrite = true
	return "[redacted]", nil
}
```

With `WithSkipSensitive(true)` the copier refuses to copy such a value unless
the type provides `Copiable` (or `MarshalCopier`) logic, so a secret cannot leak
into a log, DTO or config dump by accident.

## Migrating from mapstructure

`mm` is not a drop-in replacement, but the mapping is short:

| mapstructure | mm |
| --- | --- |
| `mapstructure.Decode(src, &dst)` | `copier.NewCopier().Copy(&dst, src)` |
| `DecoderConfig.TagName` | `copier.WithTag("json")` |
| `` mapstructure:",remain" `` | `` mm:",unmapped" `` |
| `ZeroFields: true` (default) | default: unmatched destination fields are zeroed |
| `ZeroFields: false` | `copier.NewCopier(copier.WithMerge())` |
| `WeaklyTypedInput` | numeric/string conversions are applied when Go allows them (`ConvertibleTo`); otherwise register `WithTypeConverter` |
| `DecodeHook` | `WithFormatter` (per field) or `WithTypeConverter` (per type pair) |
| `ErrorUnused` | use `mm:",unmapped"` and inspect the collected map |
| `Metadata` | not implemented |

Differences to be aware of:

- `mm` always deep-copies reference values (slices, maps, pointers) and detects
  cycles; mapstructure decodes into the target without trying to be a deep
  copier, and cannot handle cyclic input.
- Embedded structs are copied as fields of their own name; there is no
  equivalent of `mapstructure:",squash"` yet.
- `mm` supports struct → struct and struct → map as first-class operations, not
  just map → struct.

## Performance

Measured with `go test ./copier/tests -bench . -benchmem -count=3` on an
Apple M3 Pro (Go 1.27); medians shown, `allocs/op` in parentheses. The full run
is committed as `copier/tests/after.txt`; `copier/tests/before.txt` has the
pre-optimisation numbers.

| Scenario | mm | mapstructure v1.5 |
| --- | --- | --- |
| struct → struct, 4 plain fields | **189 ns** (0) | 1 212 ns (37) |
| struct → struct, nested | **343 ns** (0) | 2 332 ns (66) |
| same type deep copy | **130 ns** (2) | — |
| 50-element slice of structs | **20 µs** (471) | 68 µs (2 065) |
| `map[string]any` → struct | **439 ns** (8) | 956 ns (26) |
| map → struct with `,unmapped` | **692 ns** (16) | 1 105 ns (28) |
| struct → `map[string]any` | 398 ns (6) | **318 ns** (10) |
| cyclic graph copy | 3 314 ns (94) | not supported |

Notes:

- Debug logging is **off by default**. It is intentionally expensive
  (`ll.Logger.Debugf` formats before checking enablement) — use it for
  troubleshooting, not in hot paths.
- The top rows use cached per-type copy plans. Shapes that need custom logic
  (hooks, tags that change semantics, merge mode) keep using the generic field
  walk; struct → map is the one shape where mapstructure is still faster.
- `mm.Copy[T]` and `copier.NewCopier().Copy` share the same defaults; both use a
  suspended logger unless `WithDebug(true)` is set.
- Numbers move with machine load; compare runs with `benchstat`, not by eye.

## Status

Pre-1.0. The API may still change. `go vet ./...` and `go test -race ./...`
are expected to pass and run in CI.

## License

MIT — see [LICENSE](LICENSE).
