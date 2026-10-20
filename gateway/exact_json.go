package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// ExactJSONBinding opts a single service/method into raw request bytes and
// descriptor-checked exact JSON numbers. Paths use protobuf field names and [*]
// for repeated message fields; all selected leaves are required on success.
// Register bindings after NewEtcdGateway and before serving HTTP requests.
// Money strings must be decimal values representable as signed int64 * 10^-8.
// MaxRecvBytes is a per-call receive limit, from 1 byte through 32 MiB.
type ExactJSONBinding struct {
	ServiceName     string
	FullMethod      string
	RequestMessage  string
	ResponseMessage string
	MoneyPaths      []string
	IntegerPaths    []string
	MaxRecvBytes    int
}

var exactFieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var exactFullName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+$`)
var exactDecimal = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]{1,8})?$`)
var exactInteger = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

type exactPathPart struct {
	name string
	list bool
}

func parseExactPath(path string) ([]exactPathPart, error) {
	parts := strings.Split(path, ".")
	if len(parts) < 2 || parts[0] != "data" {
		return nil, errors.New("exact JSON paths must start with data")
	}
	out := make([]exactPathPart, 0, len(parts))
	for i, part := range parts {
		list := strings.HasSuffix(part, "[*]")
		name := strings.TrimSuffix(part, "[*]")
		if !exactFieldName.MatchString(name) || list && i == len(parts)-1 {
			return nil, fmt.Errorf("invalid exact JSON path %q", path)
		}
		out = append(out, exactPathPart{name, list})
	}
	return out, nil
}

// RegisterExactJSONBinding registers an immutable, service-scoped binding.
// Duplicate methods and invalid configuration fail without replacing a binding.
// Registration is concurrency safe; descriptor checks also run before every RPC
// so reflected schema changes fail closed before business side effects.
func (gw *EtcdGateway) RegisterExactJSONBinding(b ExactJSONBinding) error {
	if gw == nil {
		return errors.New("nil gateway")
	}
	method := strings.Split(strings.TrimPrefix(b.FullMethod, "/"), "/")
	if b.ServiceName == "" || strings.TrimSpace(b.ServiceName) != b.ServiceName || len(method) != 2 || !strings.HasPrefix(b.FullMethod, "/") || !exactFullName.MatchString(method[0]) || !exactFieldName.MatchString(method[1]) || !exactFullName.MatchString(b.RequestMessage) || !exactFullName.MatchString(b.ResponseMessage) {
		return errors.New("invalid exact JSON service, method or message name")
	}
	if b.MaxRecvBytes <= 0 || b.MaxRecvBytes > 32<<20 {
		return errors.New("exact JSON receive limit must be within 1..33554432 bytes")
	}
	seen := map[string]bool{}
	for _, paths := range [][]string{b.MoneyPaths, b.IntegerPaths} {
		for _, path := range paths {
			if _, err := parseExactPath(path); err != nil {
				return err
			}
			if seen[path] {
				return fmt.Errorf("duplicate exact JSON path %q", path)
			}
			seen[path] = true
		}
	}
	b.MoneyPaths = append([]string(nil), b.MoneyPaths...)
	b.IntegerPaths = append([]string(nil), b.IntegerPaths...)
	if _, loaded := gw.exactJSONBindings.LoadOrStore(b.ServiceName+"\x00"+b.FullMethod, &b); loaded {
		return errors.New("exact JSON method already registered")
	}
	return nil
}

func (gw *EtcdGateway) exactJSONBinding(service, method string) *ExactJSONBinding {
	b, ok := gw.exactJSONBindings.Load(service + "\x00" + method)
	if !ok {
		return nil
	}
	return b.(*ExactJSONBinding)
}

func (b *ExactJSONBinding) validateDescriptor(desc *MethodDescriptor) error {
	req := desc.NewRequest().ProtoReflect().Descriptor()
	resp := desc.NewResponse().ProtoReflect().Descriptor()
	if desc.FullMethod != b.FullMethod || string(req.FullName()) != b.RequestMessage || string(resp.FullName()) != b.ResponseMessage {
		return errors.New("exact JSON method message type mismatch")
	}
	fields := req.Fields()
	raw := fields.ByName("raw_body")
	if fields.Len() != 1 || raw == nil || raw.Kind() != protoreflect.BytesKind || raw.IsList() || raw.IsMap() {
		return errors.New("exact JSON request must contain only bytes raw_body")
	}
	code := resp.Fields().ByName("code")
	data := resp.Fields().ByName("data")
	if code == nil || code.Kind() != protoreflect.Int32Kind || code.IsList() || code.IsMap() || data == nil || data.Kind() != protoreflect.MessageKind || data.IsList() || data.IsMap() {
		return errors.New("exact JSON response requires int32 code and message data")
	}
	for i, paths := range [][]string{b.MoneyPaths, b.IntegerPaths} {
		for _, path := range paths {
			parts, err := parseExactPath(path)
			if err != nil {
				return err
			}
			current := resp
			for n, part := range parts {
				f := current.Fields().ByName(protoreflect.Name(part.name))
				if f == nil || f.IsMap() || f.IsList() != part.list {
					return fmt.Errorf("exact JSON schema path mismatch %s", path)
				}
				if n == len(parts)-1 {
					if i == 0 && f.Kind() != protoreflect.StringKind || i == 1 && f.Kind() != protoreflect.StringKind && f.Kind() != protoreflect.Int64Kind && f.Kind() != protoreflect.Sint64Kind && f.Kind() != protoreflect.Sfixed64Kind {
						return fmt.Errorf("exact JSON leaf kind mismatch %s", path)
					}
				} else {
					if f.Kind() != protoreflect.MessageKind {
						return fmt.Errorf("exact JSON container mismatch %s", path)
					}
					current = f.Message()
				}
			}
		}
	}
	return nil
}

func exactNumberToken(value string, money bool) (json.RawMessage, error) {
	if len(value) > 32 {
		return nil, errors.New("exact JSON number too long")
	}
	scaled := value
	if money {
		if !exactDecimal.MatchString(value) {
			return nil, errors.New("invalid exact JSON money")
		}
		parts := strings.SplitN(value, ".", 2)
		scaled = parts[0]
		fraction := ""
		if len(parts) == 2 {
			fraction = parts[1]
		}
		scaled += fraction + strings.Repeat("0", 8-len(fraction))
	} else if !exactInteger.MatchString(value) {
		return nil, errors.New("invalid exact JSON integer")
	}
	number, ok := new(big.Int).SetString(scaled, 10)
	if !ok || !number.IsInt64() {
		return nil, errors.New("exact JSON number outside signed int64 range")
	}
	return json.RawMessage(value), nil
}

func convertExactPath(raw json.RawMessage, parts []exactPathPart, money bool) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, errors.New("exact JSON object missing")
	}
	part := parts[0]
	value, ok := obj[part.name]
	if !ok || string(value) == "null" {
		return nil, fmt.Errorf("exact JSON field missing: %s", part.name)
	}
	var err error
	switch {
	case len(parts) == 1:
		var decimal string
		if err = json.Unmarshal(value, &decimal); err == nil {
			obj[part.name], err = exactNumberToken(decimal, money)
		}
	case part.list:
		var list []json.RawMessage
		if err = json.Unmarshal(value, &list); err == nil && list == nil {
			err = errors.New("exact JSON list missing")
		}
		if err == nil {
			for i := range list {
				list[i], err = convertExactPath(list[i], parts[1:], money)
				if err != nil {
					break
				}
			}
		}
		if err == nil {
			obj[part.name], err = json.Marshal(list)
		}
	default:
		obj[part.name], err = convertExactPath(value, parts[1:], money)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(obj)
}

func (b *ExactJSONBinding) marshalResponse(msg proto.Message, resolver interface {
	protoregistry.MessageTypeResolver
	protoregistry.ExtensionTypeResolver
}) ([]byte, error) {
	body, err := (&protojson.MarshalOptions{UseProtoNames: true, EmitDefaultValues: true, Resolver: resolver}).Marshal(msg)
	if err != nil {
		return nil, err
	}
	reflected := msg.ProtoReflect()
	code := reflected.Descriptor().Fields().ByName("code")
	if code == nil {
		return nil, errors.New("exact JSON code missing")
	}
	dataField := reflected.Descriptor().Fields().ByName("data")
	if reflected.Get(code).Int() != 0 && dataField != nil && !reflected.Has(dataField) {
		return body, nil
	}
	var root map[string]json.RawMessage
	if err = json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	var data map[string]json.RawMessage
	if err = json.Unmarshal(root["data"], &data); err != nil || data == nil {
		return nil, errors.New("exact JSON success data missing")
	}
	for i, paths := range [][]string{b.MoneyPaths, b.IntegerPaths} {
		for _, path := range paths {
			parts, e := parseExactPath(path)
			if e != nil {
				return nil, e
			}
			body, err = convertExactPath(body, parts, i == 0)
			if err != nil {
				return nil, fmt.Errorf("exact JSON encode %s: %w", path, err)
			}
		}
	}
	return body, nil
}
