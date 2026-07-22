package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xs23933/core/v3/etcd"
	gatewayroute "github.com/xs23933/core/v3/gateway/route"
)

type automaticHTTPRoute struct {
	Method  string
	Path    string
	Handler string
}

type autoHTTPRouteConfig struct {
	Enabled         bool
	IncludePrefixes []string
	ExcludePrefixes []string
}

type automaticHTTPDiscoveryStore struct {
	discovery *etcd.Discovery
}

func (s automaticHTTPDiscoveryStore) Put(ctx context.Context, key string, value []byte) error {
	if s.discovery == nil {
		return errors.New("core: etcd discovery is not enabled")
	}
	return s.discovery.PutStringContext(ctx, key, string(value))
}

func (app *Core) recordAutomaticHTTPRoute(route automaticHTTPRoute) {
	methods := []string{strings.ToUpper(strings.TrimSpace(route.Method))}
	if len(methods) == 1 && methods[0] == MethodAll {
		methods = Methods[:METHOD_ALL]
	}

	app.automaticHTTPRoutesMu.Lock()
	defer app.automaticHTTPRoutesMu.Unlock()
	for _, method := range methods {
		route.Method = method
		key := method + "\x00" + route.Path
		if index, exists := app.automaticHTTPRouteIndexes[key]; exists {
			app.automaticHTTPRoutes[index] = route
			continue
		}
		app.automaticHTTPRouteIndexes[key] = len(app.automaticHTTPRoutes)
		app.automaticHTTPRoutes = append(app.automaticHTTPRoutes, route)
	}
}

func (app *Core) automaticHTTPRouteCatalog() []automaticHTTPRoute {
	app.automaticHTTPRoutesMu.RLock()
	defer app.automaticHTTPRoutesMu.RUnlock()
	return append([]automaticHTTPRoute(nil), app.automaticHTTPRoutes...)
}

func (app *Core) automaticHTTPRouteConfig() autoHTTPRouteConfig {
	return autoHTTPRouteConfig{
		Enabled:         app.Conf.GetBool("gateway.auto_http_routes.enabled", false),
		IncludePrefixes: normalizeAutomaticHTTPRoutePrefixes(app.Conf.GetStrings("gateway.auto_http_routes.include_prefixes")),
		ExcludePrefixes: normalizeAutomaticHTTPRoutePrefixes(app.Conf.GetStrings("gateway.auto_http_routes.exclude_prefixes")),
	}
}

func (app *Core) automaticHTTPRouteRegistryTuple() (*etcd.Registry, *etcd.Discovery, string, string) {
	app.mutex.Lock()
	defer app.mutex.Unlock()
	return app.etcdRegistry, app.EtcdDiscovery, app.etcdRegistryServiceName, app.etcdRegistryNamespace
}

func (app *Core) syncAutomaticHTTPRoutes(ctx context.Context) error {
	config := app.automaticHTTPRouteConfig()
	if !config.Enabled {
		return nil
	}
	if len(config.IncludePrefixes) == 0 {
		return errors.New("core: gateway.auto_http_routes.include_prefixes requires at least one prefix when enabled")
	}

	registry, discovery, serviceName, namespace := app.automaticHTTPRouteRegistryTuple()
	serviceName = strings.TrimSpace(serviceName)
	if registry == nil || serviceName == "" {
		return errors.New("core: automatic HTTP routes require an enabled etcd registry identity")
	}
	if discovery == nil {
		return errors.New("core: automatic HTTP routes require etcd discovery")
	}

	registeredRoutes := app.automaticHTTPRouteCatalog()
	definitions := make([]*gatewayroute.Definition, 0, len(registeredRoutes))
	for _, registered := range registeredRoutes {
		if !matchesAnyAutomaticHTTPRoutePrefix(registered.Path, config.IncludePrefixes) ||
			matchesAnyAutomaticHTTPRoutePrefix(registered.Path, config.ExcludePrefixes) {
			continue
		}
		definitions = append(definitions, &gatewayroute.Definition{
			Source:       gatewayroute.SourceAutoHTTP,
			Owner:        serviceName,
			Protocol:     gatewayroute.ProtocolHTTP,
			Method:       registered.Method,
			Path:         registered.Path,
			ServiceName:  serviceName,
			UpstreamPath: registered.Path,
			Description:  registered.Handler,
			Enabled:      true,
		})
	}

	publisher := gatewayroute.Publisher{
		Store:       automaticHTTPDiscoveryStore{discovery: discovery},
		RoutePrefix: etcd.NamespacePrefix(namespace, "gateway/routes"),
	}
	if err := publisher.SyncAutomaticHTTP(ctx, serviceName, definitions); err != nil {
		return fmt.Errorf("core: sync automatic HTTP routes: %w", err)
	}
	return nil
}

func normalizeAutomaticHTTPRoutePrefixes(prefixes []string) []string {
	result := make([]string, 0, len(prefixes))
	seen := make(map[string]struct{}, len(prefixes))
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		if strings.Trim(prefix, "/") == "" {
			prefix = "/"
		} else if !strings.HasPrefix(prefix, "/") {
			prefix = "/" + prefix
		}
		if len(prefix) > 1 {
			prefix = strings.TrimRight(prefix, "/")
		}
		if _, exists := seen[prefix]; exists {
			continue
		}
		seen[prefix] = struct{}{}
		result = append(result, prefix)
	}
	return result
}

func matchesAnyAutomaticHTTPRoutePrefix(routePath string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix == "/" || routePath == prefix || strings.HasPrefix(routePath, prefix+"/") {
			return true
		}
	}
	return false
}
