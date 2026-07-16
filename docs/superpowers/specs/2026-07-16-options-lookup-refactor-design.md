# Options Lookup Refactor Design

## Goal

Remove duplicated dotted-path traversal from the `Options` typed getters and make the duration getter use the same lookup foundation as existing getters.

## Design

Add one unexported lookup primitive:

```go
func (opt Options) lookup(path string) (any, bool)
```

`lookup` splits dotted paths, traverses nested `Options` and `map[string]any` values, and returns the raw leaf value. Direct keys and dotted paths use the same code path.

The following typed getters will call `lookup` before applying their existing conversion and default rules:

- `GetString`
- `GetStrings`
- `GetInt`
- `GetInt64`
- `GetBool`
- `GetDuration`

Existing exported `GetPathString`, `GetPathInt`, `GetPathBool`, and `GetPathStrings` methods remain available for compatibility. Each delegates to its corresponding typed getter rather than maintaining a second traversal implementation.

`GetInt64` will gain dotted-path support, aligning it with the other typed getters. No other conversion rules will be broadened: existing accepted types, fallback values, and public signatures remain unchanged.

## Testing

Regression tests will verify:

- All typed getters resolve both direct and dotted YAML values through the shared lookup.
- Nested values represented as either `Options` or `map[string]any` are supported.
- Existing `GetPathXxx` methods return the same results as their corresponding `GetXxx` methods.
- Existing defaults and invalid-value behavior remain intact.
- `GetDuration("test.delay")` continues returning `20 * time.Second`.

The refactor will follow red-green-refactor: add coverage for shared behavior and the new dotted `GetInt64` behavior, verify the new test fails before production changes, replace duplicated traversal, then run focused and repository-wide tests.
