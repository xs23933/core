package route

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Store is the persistent key-value boundary used by route publishers.
type Store interface {
	Put(ctx context.Context, key string, value []byte) error
}

// Publisher persists route definitions.
type Publisher struct {
	Store       Store
	RoutePrefix string
}

type publication struct {
	key   string
	value []byte
}

// PublishManual validates the complete batch before persisting any definition.
func (p Publisher) PublishManual(ctx context.Context, definitions ...*Definition) error {
	routePrefix, err := p.validate()
	if err != nil {
		return err
	}
	prepared, err := prepareDefinitions(definitions, SourceManual, "")
	if err != nil {
		return err
	}

	publications := make([]publication, 0, len(prepared))
	for _, definition := range prepared {
		value, err := json.Marshal(definition)
		if err != nil {
			return fmt.Errorf("route: marshal manual definition: %w", err)
		}
		publications = append(publications, publication{
			key:   routePrefix + string(SourceManual) + "/" + definition.Slot,
			value: value,
		})
	}
	for _, item := range publications {
		if err := p.Store.Put(ctx, item.key, item.value); err != nil {
			return fmt.Errorf("route: publish manual definition %s: %w", item.key, err)
		}
	}
	return nil
}

// SyncAutomaticHTTP replaces the complete automatic HTTP route catalog for a
// logical service with one store write.
func (p Publisher) SyncAutomaticHTTP(ctx context.Context, serviceName string, definitions []*Definition) error {
	routePrefix, err := p.validate()
	if err != nil {
		return err
	}
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return errors.New("route: automatic service_name is required")
	}

	owned := make([]*Definition, len(definitions))
	for index, definition := range definitions {
		if definition == nil {
			return errors.New("route: definition is nil")
		}
		copy := cloneDefinition(definition)
		if copy.Protocol == "" {
			copy.Protocol = ProtocolHTTP
		}
		if copy.Protocol != ProtocolHTTP {
			return errors.New("route: automatic http catalog contains a non-http route")
		}
		definitionService := strings.TrimSpace(copy.ServiceName)
		if definitionService != "" && definitionService != serviceName {
			return fmt.Errorf("route: service_name %q does not match catalog service %q", definitionService, serviceName)
		}
		copy.ServiceName = serviceName
		owned[index] = copy
	}

	prepared, err := prepareDefinitions(owned, SourceAutoHTTP, serviceName)
	if err != nil {
		return err
	}
	value, err := json.Marshal(Catalog{ServiceName: serviceName, Routes: prepared})
	if err != nil {
		return fmt.Errorf("route: marshal automatic http catalog: %w", err)
	}
	key := routePrefix + string(SourceAutoHTTP) + "/" + OwnerID(serviceName)
	if err := p.Store.Put(ctx, key, value); err != nil {
		return fmt.Errorf("route: publish automatic http catalog %s: %w", key, err)
	}
	return nil
}

func (p Publisher) validate() (string, error) {
	if p.Store == nil {
		return "", errors.New("route: publisher store is nil")
	}
	prefix := strings.TrimSpace(p.RoutePrefix)
	if prefix == "" || !strings.HasPrefix(prefix, "/") || strings.Trim(prefix, "/") == "" {
		return "", errors.New("route: invalid route prefix")
	}
	return normalizePrefix(prefix), nil
}

func prepareDefinitions(definitions []*Definition, source Source, owner string) ([]*Definition, error) {
	prepared := make([]*Definition, 0, len(definitions))
	seenSlots := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if err := Validate(definition); err != nil {
			return nil, err
		}
		slot, err := SlotID(definition.Method, definition.Path)
		if err != nil {
			return nil, err
		}
		if _, exists := seenSlots[slot]; exists {
			return nil, fmt.Errorf("route: duplicate slot %s", slot)
		}
		seenSlots[slot] = struct{}{}

		copy := cloneDefinition(definition)
		copy.Slot = slot
		copy.Source = source
		copy.Owner = owner
		copy.Method = strings.ToUpper(strings.TrimSpace(copy.Method))
		copy.Path = normalizeDefinitionPath(copy.Path)
		copy.UpstreamPath = normalizeDefinitionPath(copy.UpstreamPath)
		copy.ServiceName = strings.TrimSpace(copy.ServiceName)
		copy.GRPCMethod = strings.TrimSpace(copy.GRPCMethod)
		if copy.Protocol == "" {
			copy.Protocol = ProtocolGRPC
		}
		prepared = append(prepared, copy)
	}
	sort.Slice(prepared, func(i, j int) bool { return prepared[i].Slot < prepared[j].Slot })
	return prepared, nil
}

func cloneDefinition(definition *Definition) *Definition {
	copy := *definition
	if definition.Headers != nil {
		copy.Headers = make(map[string]string, len(definition.Headers))
		for key, value := range definition.Headers {
			copy.Headers[key] = value
		}
	}
	return &copy
}

func normalizeDefinitionPath(path string) string {
	path = strings.TrimSpace(path)
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	return path
}

func normalizePrefix(prefix string) string {
	return strings.TrimRight(strings.TrimSpace(prefix), "/") + "/"
}
