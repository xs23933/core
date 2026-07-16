# Options YAML Duration Design

## Goal

Allow callers to read Go duration values from `Options`, including nested YAML values such as:

```yaml
test:
  delay: 20s
```

Calling `Conf.GetDuration("test.delay")` must return `20 * time.Second` as a `time.Duration`.

## API and behavior

Add this method to `Options`:

```go
func (opt *Options) GetDuration(k string, def ...time.Duration) time.Duration
```

The method follows the existing typed getter conventions:

- A key containing `.` is resolved as a nested path.
- A string value is parsed with `time.ParseDuration`, supporting standard Go duration syntax such as `500ms`, `20s`, and `2m30s`.
- A value already stored as `time.Duration` is returned directly.
- A missing key, unsupported value type, or invalid duration string returns the first supplied default.
- If no default is supplied, failure returns the zero duration.

The implementation remains scoped to a dedicated getter. It does not change generic `GetAs` conversion or YAML unmarshalling behavior.

## Testing

Tests will cover:

- Reading a top-level duration string.
- Reading `test.delay: 20s` through the dotted path `test.delay`.
- Returning an existing `time.Duration` value.
- Returning the supplied default for a missing key or invalid duration.
- Returning zero when parsing fails and no default is supplied.

Implementation will follow red-green-refactor: add the behavior test and verify it fails because `GetDuration` is absent, add the minimum implementation, then run focused and package-level tests.
