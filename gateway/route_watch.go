package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	core "github.com/xs23933/core/v3"
	"github.com/xs23933/core/v3/gateway/route"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type routeSnapshotRevision struct {
	Snapshot routeSnapshot
	Revision int64
}

type routeWatchResponse struct {
	Revision int64
	Events   []routeEvent
	Err      error
}

type routeWatchSource interface {
	Load(context.Context) (routeSnapshotRevision, error)
	Watch(context.Context, int64) <-chan routeWatchResponse
}

type etcdRouteWatchSource struct {
	client      *clientv3.Client
	routePrefix string
	launch      func(func())
}

func (source etcdRouteWatchSource) Load(ctx context.Context) (routeSnapshotRevision, error) {
	if source.client == nil {
		return routeSnapshotRevision{}, errors.New("gateway: route watch etcd client is nil")
	}
	prefix := normalizeRouteStoragePrefix(source.routePrefix)
	loadCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := source.client.Get(loadCtx, prefix, clientv3.WithPrefix())
	if err != nil {
		return routeSnapshotRevision{}, err
	}

	values := make(map[string]*routeSourceValue, len(response.Kvs))
	for _, value := range response.Kvs {
		storageKey := string(value.Key)
		decoded, err := decodeWatchedRouteSource(prefix, storageKey, value.Value)
		if err != nil {
			core.Warn("[Gateway] ignore invalid route source %s in snapshot: %v", storageKey, err)
			continue
		}
		values[storageKey] = decoded
	}
	return routeSnapshotRevision{
		Snapshot: newRouteSnapshot(values),
		Revision: response.Header.GetRevision(),
	}, nil
}

func (source etcdRouteWatchSource) Watch(ctx context.Context, revision int64) <-chan routeWatchResponse {
	responses := make(chan routeWatchResponse)
	if source.client == nil || source.launch == nil {
		close(responses)
		return responses
	}
	prefix := normalizeRouteStoragePrefix(source.routePrefix)
	options := []clientv3.OpOption{clientv3.WithPrefix()}
	if revision > 0 {
		options = append(options, clientv3.WithRev(revision))
	}
	watch := source.client.Watch(ctx, prefix, options...)
	forward := func() {
		defer close(responses)
		for {
			select {
			case <-ctx.Done():
				return
			case response, ok := <-watch:
				if !ok {
					return
				}
				converted := routeWatchResponse{
					Revision: response.Header.GetRevision(),
					Err:      response.Err(),
				}
				if converted.Err == nil {
					converted.Events = decodeRouteWatchEvents(prefix, response.Events)
				}
				select {
				case <-ctx.Done():
					return
				case responses <- converted:
				}
				if converted.Err != nil {
					return
				}
			}
		}
	}
	source.launch(forward)
	return responses
}

func decodeRouteWatchEvents(prefix string, events []*clientv3.Event) []routeEvent {
	result := make([]routeEvent, 0, len(events))
	for _, event := range events {
		if event == nil || event.Kv == nil {
			continue
		}
		storageKey := string(event.Kv.Key)
		item := routeEvent{StorageKey: storageKey, Deleted: event.Type == clientv3.EventTypeDelete}
		if !item.Deleted {
			value, err := decodeWatchedRouteSource(prefix, storageKey, event.Kv.Value)
			if err != nil {
				core.Warn("[Gateway] invalidate malformed route source %s: %v", storageKey, err)
				item.Deleted = true
			} else {
				item.Value = value
			}
		}
		result = append(result, item)
	}
	return result
}

func decodeWatchedRouteSource(prefix, storageKey string, value []byte) (*routeSourceValue, error) {
	prefix = normalizeRouteStoragePrefix(prefix)
	if !strings.HasPrefix(storageKey, prefix) {
		return nil, errors.New("gateway: route key is outside route prefix")
	}
	relativeKey := strings.TrimPrefix(storageKey, prefix)
	if relativeKey == "" {
		return nil, errors.New("gateway: route key is empty")
	}

	if strings.HasPrefix(relativeKey, string(route.SourceAutoHTTP)+"/") {
		ownerID := strings.TrimPrefix(relativeKey, string(route.SourceAutoHTTP)+"/")
		if ownerID == "" || strings.Contains(ownerID, "/") {
			return nil, errors.New("gateway: automatic http catalog key is invalid")
		}
		return decodeAutomaticHTTPRouteCatalog(ownerID, value)
	}

	if strings.HasPrefix(relativeKey, string(route.SourceManual)+"/") {
		slot := strings.TrimPrefix(relativeKey, string(route.SourceManual)+"/")
		if slot == "" || strings.Contains(slot, "/") {
			return nil, errors.New("gateway: manual route key is invalid")
		}
		definition, err := decodeManualRouteDefinition(value)
		if err != nil {
			return nil, err
		}
		return &routeSourceValue{Definition: definition}, nil
	}

	if strings.Contains(relativeKey, "/") {
		return nil, errors.New("gateway: unsupported route source key")
	}
	definition, err := decodeManualRouteDefinition(value)
	if err != nil {
		return nil, err
	}
	if definition.ID == "" {
		definition.ID = relativeKey
	}
	return &routeSourceValue{Definition: definition}, nil
}

func decodeManualRouteDefinition(value []byte) (*route.Definition, error) {
	var definition route.Definition
	if err := json.Unmarshal(value, &definition); err != nil {
		return nil, err
	}
	definition.Source = route.SourceManual
	definition.Owner = ""
	if err := normalizeAndValidateRoute(&definition); err != nil {
		return nil, err
	}
	return &definition, nil
}

func decodeAutomaticHTTPRouteCatalog(ownerID string, value []byte) (*routeSourceValue, error) {
	var catalog route.Catalog
	if err := json.Unmarshal(value, &catalog); err != nil {
		return nil, err
	}
	catalog.ServiceName = strings.TrimSpace(catalog.ServiceName)
	if catalog.ServiceName == "" {
		return nil, errors.New("gateway: automatic http catalog service_name is required")
	}
	if want := route.OwnerID(catalog.ServiceName); ownerID != want {
		return nil, fmt.Errorf("gateway: automatic http catalog owner %s does not match service_name", ownerID)
	}

	prepared := make([]*route.Definition, len(catalog.Routes))
	seenSlots := make(map[string]struct{}, len(catalog.Routes))
	for index, definition := range catalog.Routes {
		if definition == nil {
			return nil, errors.New("gateway: automatic http catalog contains a nil route")
		}
		copy := cloneRouteDefinition(definition)
		if copy.Protocol == "" {
			copy.Protocol = route.ProtocolHTTP
		}
		if copy.Protocol != route.ProtocolHTTP {
			return nil, errors.New("gateway: automatic http catalog contains a non-http route")
		}
		serviceName := strings.TrimSpace(copy.ServiceName)
		if serviceName != "" && serviceName != catalog.ServiceName {
			return nil, fmt.Errorf("gateway: route service_name %q does not match catalog service %q", serviceName, catalog.ServiceName)
		}
		copy.ServiceName = catalog.ServiceName
		copy.Source = route.SourceAutoHTTP
		copy.Owner = catalog.ServiceName
		if err := normalizeAndValidateRoute(copy); err != nil {
			return nil, err
		}
		if _, exists := seenSlots[copy.Slot]; exists {
			return nil, fmt.Errorf("gateway: automatic http catalog contains duplicate slot %s", copy.Slot)
		}
		seenSlots[copy.Slot] = struct{}{}
		prepared[index] = copy
	}
	catalog.Routes = prepared
	return &routeSourceValue{Catalog: &catalog}, nil
}

func runRouteWatchLoopFrom(
	ctx context.Context,
	source routeWatchSource,
	initial *routeSnapshotRevision,
	replace func(routeSnapshot),
	apply func([]routeEvent),
	wait func(context.Context, int) bool,
) {
	failures := 0
	for ctx.Err() == nil {
		var loaded routeSnapshotRevision
		if initial != nil {
			loaded = *initial
			initial = nil
		} else {
			var err error
			loaded, err = source.Load(ctx)
			if err != nil {
				core.Warn("[Gateway] refresh route snapshot failed: %v", err)
				if !wait(ctx, failures) {
					return
				}
				failures++
				continue
			}
		}
		replace(loaded.Snapshot)

		watchCtx, cancelWatch := context.WithCancel(ctx)
		responses := source.Watch(watchCtx, nextRouteWatchRevision(loaded.Revision))
		broken := false
		for !broken {
			select {
			case <-ctx.Done():
				cancelWatch()
				return
			case response, ok := <-responses:
				if !ok || response.Err != nil {
					broken = true
					continue
				}
				apply(response.Events)
				failures = 0
			}
		}
		cancelWatch()
		if !wait(ctx, failures) {
			return
		}
		failures++
	}
}

func waitRouteWatchBackoff(ctx context.Context, failures int) bool {
	delay := time.Second
	for range failures {
		if delay >= 15*time.Second {
			delay = 30 * time.Second
			break
		}
		delay *= 2
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
