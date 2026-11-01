package gateway

import (
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/xs23933/core/v3/gateway/route"
)

type routeConflictRecord struct {
	StorageKey  string         `json:"storage_key"`
	ID          string         `json:"id"`
	Source      route.Source   `json:"source,omitempty"`
	Owner       string         `json:"owner,omitempty"`
	Protocol    route.Protocol `json:"protocol"`
	ServiceName string         `json:"service_name"`
	Target      string         `json:"target"`
}

type routeConflict struct {
	Slot    string                `json:"slot"`
	Method  string                `json:"method"`
	Path    string                `json:"path"`
	Records []routeConflictRecord `json:"records"`
}

type activeRouteChange struct {
	Slot string
	Old  *route.Definition
	New  *route.Definition
}

// routeSourceValue retains one etcd value without expanding an automatic
// catalog into synthetic storage records.
type routeSourceValue struct {
	Definition *route.Definition
	Catalog    *route.Catalog
}

type routeRecord struct {
	StorageKey string
	Index      int
	Definition *route.Definition
}

type routeSnapshot struct {
	sources          map[string]*routeSourceValue
	activeSlots      map[string]*route.Definition
	activeRecordKeys map[string]bool
	conflicts        map[string]routeConflict
	recordsByID      map[string][]routeRecord
}

type routeEvent struct {
	StorageKey string
	Value      *routeSourceValue
	Deleted    bool
}

type routeCandidate struct {
	record routeRecord
}

const runtimeAutoGRPCRoutePrefix = "runtime:auto-grpc/"

func emptyRouteSnapshot() routeSnapshot {
	return newRouteSnapshot(nil)
}

func newRouteSnapshot(values map[string]*routeSourceValue) routeSnapshot {
	snapshot := routeSnapshot{sources: make(map[string]*routeSourceValue, len(values))}
	for storageKey, value := range values {
		if value == nil || (value.Definition == nil) == (value.Catalog == nil) {
			continue
		}
		snapshot.sources[storageKey] = cloneRouteSourceValue(value)
	}
	return resolveRouteSnapshot(snapshot.sources)
}

// Sources are owned and frozen before publishing a snapshot.
func resolveRouteSnapshot(sources map[string]*routeSourceValue) routeSnapshot {
	snapshot := routeSnapshot{sources: sources, activeSlots: make(map[string]*route.Definition), activeRecordKeys: make(map[string]bool), conflicts: make(map[string]routeConflict), recordsByID: make(map[string][]routeRecord)}
	records := snapshot.allRecords()
	for _, record := range records {
		if record.Definition.ID == "" {
			continue
		}
		snapshot.recordsByID[record.Definition.ID] = append(snapshot.recordsByID[record.Definition.ID], record)
	}
	for id := range snapshot.recordsByID {
		sortRouteRecords(snapshot.recordsByID[id])
	}
	snapshot.resolveActiveRoutes(records)
	return snapshot
}

func (snapshot routeSnapshot) withRouteBatch(events []routeEvent) routeSnapshot {
	var values map[string]*routeSourceValue
	for _, event := range events {
		if event.StorageKey == "" {
			continue
		}
		current := snapshot.sources
		if values != nil {
			current = values
		}
		value := event.Value
		if event.Deleted || value == nil || (value.Definition == nil) == (value.Catalog == nil) {
			value = nil
		}
		if reflect.DeepEqual(current[event.StorageKey], value) {
			continue
		}
		if values == nil {
			values = make(map[string]*routeSourceValue, len(snapshot.sources)+len(events))
			for k, v := range snapshot.sources {
				values[k] = v
			}
		}
		if value == nil {
			delete(values, event.StorageKey)
		} else {
			values[event.StorageKey] = cloneRouteSourceValue(value)
		}
	}
	if values == nil {
		return snapshot
	}
	return resolveRouteSnapshot(values)
}

func (snapshot routeSnapshot) withRuntimeAutoGRPCSources(current routeSnapshot) routeSnapshot {
	values := make(map[string]*routeSourceValue, len(snapshot.sources)+len(current.sources))
	for storageKey, value := range snapshot.sources {
		values[storageKey] = value
	}
	for storageKey, value := range current.sources {
		if isRuntimeAutoGRPCSource(storageKey, value) {
			values[storageKey] = value
		}
	}
	return newRouteSnapshot(values)
}

func isRuntimeAutoGRPCSource(storageKey string, value *routeSourceValue) bool {
	return strings.HasPrefix(storageKey, runtimeAutoGRPCRoutePrefix) &&
		value != nil && value.Definition != nil && value.Catalog == nil &&
		value.Definition.Source == route.SourceAutoGRPC
}

func (snapshot *routeSnapshot) resolveActiveRoutes(records []routeRecord) {
	manualBySlot := make(map[string][]routeCandidate)
	automaticBySlot := make(map[string][]routeCandidate)
	for _, record := range records {
		definition := record.Definition
		if definition == nil || !definition.Enabled {
			continue
		}
		if definition.Slot == "" {
			continue
		}
		candidate := routeCandidate{record: record}
		switch definition.Source {
		case "", route.SourceManual:
			manualBySlot[definition.Slot] = append(manualBySlot[definition.Slot], candidate)
		case route.SourceAutoHTTP, route.SourceAutoGRPC:
			automaticBySlot[definition.Slot] = append(automaticBySlot[definition.Slot], candidate)
		}
	}

	slots := make(map[string]struct{}, len(manualBySlot)+len(automaticBySlot))
	for slot := range manualBySlot {
		slots[slot] = struct{}{}
	}
	for slot := range automaticBySlot {
		slots[slot] = struct{}{}
	}
	for slot := range slots {
		manual := manualBySlot[slot]
		automatic := automaticBySlot[slot]
		manualGroups := groupRouteCandidates(manual)
		automaticGroups := groupRouteCandidates(automatic)

		if len(manualGroups) > 1 {
			snapshot.conflicts[slot] = newRouteConflict(slot, manual)
			continue
		}
		if len(manualGroups) == 1 {
			snapshot.activateIdenticalCandidates(slot, manual)
			if len(automaticGroups) > 1 {
				snapshot.conflicts[slot] = newRouteConflict(slot, automatic)
			}
			continue
		}
		if len(automaticGroups) > 1 {
			snapshot.conflicts[slot] = newRouteConflict(slot, automatic)
			continue
		}
		if len(automaticGroups) == 1 {
			snapshot.activateIdenticalCandidates(slot, automatic)
		}
	}
}

func (snapshot *routeSnapshot) activateIdenticalCandidates(slot string, candidates []routeCandidate) {
	candidates = sortedRouteCandidates(candidates)
	if len(candidates) == 0 {
		return
	}
	snapshot.activeSlots[slot] = candidates[0].record.Definition
	for _, candidate := range candidates {
		snapshot.activeRecordKeys[routeRecordKey(candidate.record)] = true
	}
}

func groupRouteCandidates(candidates []routeCandidate) map[routeDispatchKey][]routeCandidate {
	groups := make(map[routeDispatchKey][]routeCandidate)
	for _, candidate := range candidates {
		identity := routeDispatchIdentity(candidate.record.Definition)
		groups[identity] = append(groups[identity], candidate)
	}
	return groups
}

// routeDispatchKey excludes publication metadata and is directly comparable.
type routeDispatchKey struct {
	Protocol                              route.Protocol
	Method, Path, ServiceName, GRPCMethod string
}

func routeDispatchIdentity(definition *route.Definition) routeDispatchKey {
	if definition == nil {
		return routeDispatchKey{}
	}
	protocol := definition.Protocol
	if protocol == "" {
		protocol = route.ProtocolGRPC
	}
	return routeDispatchKey{Protocol: protocol, Method: strings.ToUpper(strings.TrimSpace(definition.Method)), Path: normalizeRoutePath(definition.Path), ServiceName: strings.TrimSpace(definition.ServiceName), GRPCMethod: strings.TrimSpace(definition.GRPCMethod)}
}

func newRouteConflict(slot string, candidates []routeCandidate) routeConflict {
	candidates = sortedRouteCandidates(candidates)
	conflict := routeConflict{Slot: slot, Records: make([]routeConflictRecord, 0, len(candidates))}
	if len(candidates) > 0 {
		conflict.Method = candidates[0].record.Definition.Method
		conflict.Path = candidates[0].record.Definition.Path
	}
	for _, candidate := range candidates {
		definition := candidate.record.Definition
		target := definition.ServiceName
		if definition.Protocol == route.ProtocolGRPC {
			target += definition.GRPCMethod
		}
		source := definition.Source
		if source == "" {
			source = route.SourceManual
		}
		conflict.Records = append(conflict.Records, routeConflictRecord{
			StorageKey:  candidate.record.StorageKey,
			ID:          definition.ID,
			Source:      source,
			Owner:       definition.Owner,
			Protocol:    definition.Protocol,
			ServiceName: definition.ServiceName,
			Target:      target,
		})
	}
	return conflict
}

func sortedRouteCandidates(candidates []routeCandidate) []routeCandidate {
	result := append([]routeCandidate(nil), candidates...)
	sort.Slice(result, func(i, j int) bool {
		left := result[i].record
		right := result[j].record
		if left.StorageKey != right.StorageKey {
			return left.StorageKey < right.StorageKey
		}
		return left.Index < right.Index
	})
	return result
}

func (snapshot routeSnapshot) allRecords() []routeRecord {
	records := make([]routeRecord, 0, len(snapshot.sources))
	for storageKey, value := range snapshot.sources {
		switch {
		case value == nil:
			continue
		case value.Definition != nil:
			records = append(records, routeRecord{StorageKey: storageKey, Index: -1, Definition: value.Definition})
		case value.Catalog != nil:
			for index, definition := range value.Catalog.Routes {
				if definition != nil {
					records = append(records, routeRecord{StorageKey: storageKey, Index: index, Definition: definition})
				}
			}
		}
	}
	sortRouteRecords(records)
	return records
}

func sortRouteRecords(records []routeRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].StorageKey != records[j].StorageKey {
			return records[i].StorageKey < records[j].StorageKey
		}
		return records[i].Index < records[j].Index
	})
}

func routeRecordKey(record routeRecord) string {
	return record.StorageKey + "\x00" + strconv.Itoa(record.Index)
}

func cloneRouteSourceValue(value *routeSourceValue) *routeSourceValue {
	if value == nil {
		return nil
	}
	copy := &routeSourceValue{}
	cloneFrozenDefinition := func(definition *route.Definition) *route.Definition {
		copy := cloneRouteDefinition(definition)
		if copy != nil && copy.Enabled && copy.Slot == "" {
			copy.Slot, _ = route.SlotID(copy.Method, copy.Path)
		}
		return copy
	}
	if value.Definition != nil {
		copy.Definition = cloneFrozenDefinition(value.Definition)
	}
	if value.Catalog != nil {
		copy.Catalog = &route.Catalog{
			ServiceName: value.Catalog.ServiceName,
			Routes:      make([]*route.Definition, len(value.Catalog.Routes)),
		}
		for index, definition := range value.Catalog.Routes {
			copy.Catalog.Routes[index] = cloneFrozenDefinition(definition)
		}
	}
	return copy
}

func cloneRouteDefinition(definition *route.Definition) *route.Definition {
	if definition == nil {
		return nil
	}
	copy := *definition
	if definition.Headers != nil {
		copy.Headers = make(map[string]string, len(definition.Headers))
		for key, value := range definition.Headers {
			copy.Headers[key] = value
		}
	}
	return &copy
}

func activeRouteChanges(oldSnapshot, newSnapshot routeSnapshot) []activeRouteChange {
	slots := make(map[string]struct{}, len(oldSnapshot.activeSlots)+len(newSnapshot.activeSlots))
	for slot := range oldSnapshot.activeSlots {
		slots[slot] = struct{}{}
	}
	for slot := range newSnapshot.activeSlots {
		slots[slot] = struct{}{}
	}
	orderedSlots := make([]string, 0, len(slots))
	for slot := range slots {
		orderedSlots = append(orderedSlots, slot)
	}
	sort.Strings(orderedSlots)

	changes := make([]activeRouteChange, 0, len(orderedSlots))
	for _, slot := range orderedSlots {
		oldRoute := oldSnapshot.activeSlots[slot]
		newRoute := newSnapshot.activeSlots[slot]
		if routeDispatchIdentity(oldRoute) == routeDispatchIdentity(newRoute) {
			continue
		}
		changes = append(changes, activeRouteChange{Slot: slot, Old: oldRoute, New: newRoute})
	}
	return changes
}

func (gw *EtcdGateway) loadRouteSnapshot() routeSnapshot {
	if gw == nil {
		return emptyRouteSnapshot()
	}
	if snapshot, ok := gw.routeState.Load().(routeSnapshot); ok {
		return snapshot
	}
	return emptyRouteSnapshot()
}

func (gw *EtcdGateway) storeRouteSnapshot(snapshot routeSnapshot) {
	gw.routeState.Store(snapshot)
}

func (gw *EtcdGateway) applyRouteBatch(events []routeEvent) []activeRouteChange {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	return gw.applyRouteBatchLocked(events)
}

func (gw *EtcdGateway) applyRouteBatchLocked(events []routeEvent) []activeRouteChange {
	current := gw.loadRouteSnapshot()
	next := current.withRouteBatch(events)
	changes := activeRouteChanges(current, next)
	gw.applyActiveRouteChanges(changes)
	gw.storeRouteSnapshot(next)
	return changes
}

func (gw *EtcdGateway) replaceEtcdRouteSnapshot(snapshot routeSnapshot) []activeRouteChange {
	gw.mu.Lock()
	defer gw.mu.Unlock()

	current := gw.loadRouteSnapshot()
	next := snapshot.withRuntimeAutoGRPCSources(current)
	changes := activeRouteChanges(current, next)
	gw.applyActiveRouteChanges(changes)
	gw.storeRouteSnapshot(next)
	return changes
}

func (gw *EtcdGateway) applyActiveRouteChanges(changes []activeRouteChange) {
	for _, change := range changes {
		if change.Old != nil && change.New != nil {
			next := cloneRouteDefinition(change.New)
			gw.app.ReplaceHandle([]string{next.Method}, change.Old.Path, next.Path, gw.routeHandler(next))
			continue
		}
		if change.Old != nil {
			gw.unregisterRoute(change.Old)
		}
		if change.New != nil {
			gw.registerRoute(cloneRouteDefinition(change.New))
		}
	}
}
