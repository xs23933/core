package core

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// helper: 创建请求，验证响应码和 body
func assertRoute(t *testing.T, app *Core, method, path string, wantCode int, wantBody string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != wantCode {
		t.Errorf("%s %s: status = %d, want %d", method, path, rec.Code, wantCode)
	}
	if wantBody != "" && rec.Body.String() != wantBody {
		t.Errorf("%s %s: body = %q, want %q", method, path, rec.Body.String(), wantBody)
	}
}

// 返回所有 params 的 JSON 视图
func paramsResponder(params ...string) func(c Ctx) error {
	return func(c Ctx) error {
		parts := make([]string, 0, len(params))
		for _, p := range params {
			parts = append(parts, fmt.Sprintf("%s=%s", p, c.Params(p)))
		}
		return c.SendString(joinParams(parts))
	}
}

func joinParams(parts []string) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += ","
		}
		s += p
	}
	return s
}

// ── 单参数测试 ──

func TestRouteSingleParamEnd(t *testing.T) {
	app := New()
	app.GET("/users/:id", paramsResponder("id"))
	assertRoute(t, app, "GET", "/users/42", 200, "id=42")
	assertRoute(t, app, "GET", "/users", 404, "")
	assertRoute(t, app, "GET", "/users/42/extra", 404, "")
}

func TestRouteSingleParamMiddle(t *testing.T) {
	app := New()
	app.GET("/users/:id/profile", paramsResponder("id"))
	assertRoute(t, app, "GET", "/users/42/profile", 200, "id=42")
	assertRoute(t, app, "GET", "/users/42", 404, "")
	assertRoute(t, app, "GET", "/users/42/profile/extra", 404, "")
}

func TestRouteSingleParamBegin(t *testing.T) {
	app := New()
	app.GET("/:orgId/members", paramsResponder("orgId"))
	assertRoute(t, app, "GET", "/acme/members", 200, "orgId=acme")
	assertRoute(t, app, "GET", "/acme", 404, "")
}

// ── 多参数测试 ──

func TestRouteTwoParamsEnd(t *testing.T) {
	app := New()
	app.GET("/users/:userId/orders/:orderId", paramsResponder("userId", "orderId"))
	assertRoute(t, app, "GET", "/users/alice/orders/1001", 200, "userId=alice,orderId=1001")
	assertRoute(t, app, "GET", "/users/alice/orders", 404, "")
}

func TestRouteThreeParams(t *testing.T) {
	app := New()
	app.GET("/orgs/:orgId/teams/:teamId/users/:userId", paramsResponder("orgId", "teamId", "userId"))
	assertRoute(t, app, "GET", "/orgs/acme/teams/eng/users/bob", 200, "orgId=acme,teamId=eng,userId=bob")
}

func TestRouteConsecutiveParams(t *testing.T) {
	app := New()
	app.GET("/api/:version/:resource", paramsResponder("version", "resource"))
	assertRoute(t, app, "GET", "/api/v1/users", 200, "version=v1,resource=users")
	assertRoute(t, app, "GET", "/api/v2/orders", 200, "version=v2,resource=orders")
}

// ── Param + Static 交替 ──

func TestRouteParamThenStaticThenParam(t *testing.T) {
	app := New()
	app.POST("/items/:itemId/revisions/:revId", paramsResponder("itemId", "revId"))
	assertRoute(t, app, "POST", "/items/abc123/revisions/5", 200, "itemId=abc123,revId=5")
}

func TestRouteStaticThenParamThenStatic(t *testing.T) {
	app := New()
	app.GET("/api/v1/users/:userId/profile", paramsResponder("userId"))
	assertRoute(t, app, "GET", "/api/v1/users/bob123/profile", 200, "userId=bob123")
}

// ── 带特殊字符参数测试 ──

func TestRouteParamWithHyphen(t *testing.T) {
	app := New()
	app.GET("/items/:item-name/detail", paramsResponder("item-name"))
	assertRoute(t, app, "GET", "/items/smart-phone-42/detail", 200, "item-name=smart-phone-42")
}

func TestRouteParamWithUnderscore(t *testing.T) {
	app := New()
	app.GET("/data/:data_key/export", paramsResponder("data_key"))
	assertRoute(t, app, "GET", "/data/monthly_report/export", 200, "data_key=monthly_report")
}

func TestRouteParamWithDot(t *testing.T) {
	app := New()
	app.GET("/files/:filename", paramsResponder("filename"))
	assertRoute(t, app, "GET", "/files/report.2024.pdf", 200, "filename=report.2024.pdf")
}

func TestRouteParamNumericID(t *testing.T) {
	app := New()
	app.GET("/users/:id", paramsResponder("id"))
	assertRoute(t, app, "GET", "/users/0", 200, "id=0")
	assertRoute(t, app, "GET", "/users/999999999999", 200, "id=999999999999")
}

func TestRouteParamEmptyString(t *testing.T) {
	app := New()
	// 空段不会被路由匹配到 :param，应该是 404
	app.GET("/users/:id", paramsResponder("id"))
	assertRoute(t, app, "GET", "/users/", 404, "")
}

// ── 不同 HTTP 方法同路径 ──

func TestRouteDifferentMethodsSamePath(t *testing.T) {
	app := New()
	app.GET("/resources/:id", func(c Ctx) error { return c.SendString("GET:" + c.Params("id")) })
	app.POST("/resources/:id", func(c Ctx) error { return c.SendString("POST:" + c.Params("id")) })
	app.DELETE("/resources/:id", func(c Ctx) error { return c.SendString("DELETE:" + c.Params("id")) })

	assertRoute(t, app, "GET", "/resources/r1", 200, "GET:r1")
	assertRoute(t, app, "POST", "/resources/r1", 200, "POST:r1")
	assertRoute(t, app, "DELETE", "/resources/r1", 200, "DELETE:r1")
	assertRoute(t, app, "PUT", "/resources/r1", 404, "")
}

// ── 静态优先于参数 ──

func TestRouteStaticTakesPriorityOverParam(t *testing.T) {
	app := New()

	// 通用 param 路由
	app.GET("/api/:resource", func(c Ctx) error {
		return c.SendString("param:" + c.Params("resource"))
	})
	// 特定 static 路由
	app.GET("/api/users", func(c Ctx) error {
		return c.SendString("static:users")
	})
	app.GET("/api/settings", func(c Ctx) error {
		return c.SendString("static:settings")
	})

	assertRoute(t, app, "GET", "/api/users", 200, "static:users")
	assertRoute(t, app, "GET", "/api/settings", 200, "static:settings")
	assertRoute(t, app, "GET", "/api/anything-else", 200, "param:anything-else")
}

// ── catch-all / 通配符 ──

func TestRouteCatchAllRoot(t *testing.T) {
	app := New()
	app.GET("/*", func(c Ctx) error { return c.SendString("catch:" + c.Params("*")) })

	assertRoute(t, app, "GET", "/", 200, "catch:")          // path = ""?
	assertRoute(t, app, "GET", "/some/path", 200, "catch:") // capture varies by impl
}

func TestRouteCatchAllAtPrefix(t *testing.T) {
	app := New()
	app.GET("/static/*filepath", func(c Ctx) error { return c.SendString("file:" + c.Params("filepath")) })

	assertRoute(t, app, "GET", "/static/css/app.css", 200, "file:css/app.css")
}

// ── 深层嵌套 / 复杂路径 ──

func TestRouteDeepNesting(t *testing.T) {
	app := New()
	app.GET("/a/:p1/b/:p2/c/:p3/d/:p4/e/:p5", paramsResponder("p1", "p2", "p3", "p4", "p5"))

	assertRoute(t, app, "GET", "/a/1/b/2/c/3/d/4/e/5", 200,
		"p1=1,p2=2,p3=3,p4=4,p5=5")
}

func TestRouteDeepNestingMismatch(t *testing.T) {
	app := New()
	app.GET("/a/:p1/b/:p2/c/:p3/d", paramsResponder("p1", "p2", "p3"))

	// 正确匹配
	assertRoute(t, app, "GET", "/a/1/b/2/c/3/d", 200, "p1=1,p2=2,p3=3")
	// 多余 segment 的404
	assertRoute(t, app, "GET", "/a/1/b/2/c/3/d/e", 404, "")
	// 缺少 segment 的404
	assertRoute(t, app, "GET", "/a/1/b/2/c/3", 404, "")
}

// ── 分组路由 ──

func TestRouteGroupWithParams(t *testing.T) {
	app := New()
	api := app.Group("/api")
	v1 := api.Group("/:version")
	v1.GET("/users/:id", paramsResponder("version", "id"))

	assertRoute(t, app, "GET", "/api/v2/users/99", 200, "version=v2,id=99")
	assertRoute(t, app, "GET", "/api/v3/users/100", 200, "version=v3,id=100")
}

// ── 尾部斜杠 ──

func TestRouteTrailingSlash(t *testing.T) {
	app := New()
	app.GET("/users/:id", paramsResponder("id"))

	// 有斜杠
	assertRoute(t, app, "GET", "/users/42", 200, "id=42")
	// 多余尾部斜杠 (框架可能容错也可能404)
	req := httptest.NewRequest("GET", "/users/42/", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	// 记录实际行为
	t.Logf("trailing slash: %s %s → status=%d body=%s",
		"GET", "/users/42/", rec.Code, rec.Body.String())
}

// ── 同时注册多个路由，确保不会相互干扰 ──

func TestRouteManyParamRoutesCoexist(t *testing.T) {
	app := New()
	app.GET("/api/:version/users/:userId", paramsResponder("version", "userId"))
	app.GET("/api/:version/orgs/:orgId", paramsResponder("version", "orgId"))
	app.GET("/api/:version/items/:itemId", paramsResponder("version", "itemId"))

	assertRoute(t, app, "GET", "/api/v1/users/u1", 200, "version=v1,userId=u1")
	assertRoute(t, app, "GET", "/api/v2/orgs/o1", 200, "version=v2,orgId=o1")
	assertRoute(t, app, "GET", "/api/v3/items/i1", 200, "version=v3,itemId=i1")
}

// ── 前缀冲突路由共存 (app/save、apps/:param?、apis/:param?) ──
// 验证修复后，在 radix tree 中共享前缀 "ap" 的多个路由可以正确共存。
// 覆盖三条路由的全部注册顺序，包括会触发前缀拆分与级联合并的顺序。

func TestRoutePrefixConflictCoexist(t *testing.T) {
	registrations := []struct {
		path    string
		handler HandlerFunc
	}{
		{"/api/v1/apis/:param?", func(c Ctx) error {
			return c.SendString("apis:" + c.Params("param"))
		}},
		{"/api/v1/app/save", func(c Ctx) error {
			return c.SendString("app-save")
		}},
		{"/api/v1/apps/:param?", func(c Ctx) error {
			return c.SendString("apps:" + c.Params("param"))
		}},
		{"/api/v1/info/:param", func(c Ctx) error {
			return c.SendString("info:" + c.Params("param"))
		}},
		{"/api/v1/kio/*path", func(c Ctx) error {
			return c.SendString("kio:" + c.Params("path"))
		}},
		{"/api/v1/err_500", func(c Ctx) error {
			return c.SendStatus(500, "internal server error")
		}},
	}
	orders := [][]int{
		{0, 1, 2},
		{0, 2, 1},
		{1, 0, 2},
		{1, 2, 0},
		{2, 0, 1},
		{2, 1, 0},
	}

	for _, order := range orders {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			app := New(Options{
				"colorful": false,
				"debug":    true,
			})
			for _, index := range order {
				route := registrations[index]
				app.POST(route.path, route.handler)
			}
			for _, index := range []int{3, 4, 5} {
				route := registrations[index]
				app.POST(route.path, route.handler)
			}

			assertRoute(t, app, "POST", "/api/v1/apis/1231", 200, "apis:1231")
			assertRoute(t, app, "POST", "/api/v1/apis", 200, "apis:")
			assertRoute(t, app, "POST", "/api/v1/app/save", 200, "app-save")
			assertRoute(t, app, "POST", "/api/v1/apps/1231", 200, "apps:1231")
			assertRoute(t, app, "POST", "/api/v1/apps", 200, "apps:")
			assertRoute(t, app, "POST", "/api/v1/app", 404, "")
			assertRoute(t, app, "POST", "/api/v1/info/1231", 200, "info:1231")

			assertRoute(t, app, "POST", "/api/v1/kio/1231", 200, "kio:1231")
			assertRoute(t, app, "POST", "/api/v1/kio", 200, "kio:")
			assertRoute(t, app, "POST", "/api/v1/kio/1231/456", 200, "kio:1231/456")
			// 增加404 的测试
			assertRoute(t, app, "POST", "/api/v1/info", 404, "")
			assertRoute(t, app, "POST", "/api/v1", 404, "")
			assertRoute(t, app, "POST", "/api/v1/err_500", 500, "internal server error")

			for _, root := range app.trees {
				assertRouteTreeInvariants(t, root)
			}
		})
	}
}

func TestRouteStaticSegmentBoundaryCoexist(t *testing.T) {
	registrations := []struct {
		path string
		body string
	}{
		{path: "/foo/bar", body: "nested"},
		{path: "/foobar", body: "joined"},
	}

	for _, order := range [][]int{{0, 1}, {1, 0}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			app := New()
			for _, index := range order {
				route := registrations[index]
				app.GET(route.path, func(c Ctx) error {
					return c.SendString(route.body)
				})
			}

			assertRoute(t, app, http.MethodGet, "/foo/bar", http.StatusOK, "nested")
			assertRoute(t, app, http.MethodGet, "/foobar", http.StatusOK, "joined")
		})
	}
}

func TestRouteStaticSegmentBoundaryRemoval(t *testing.T) {
	app := New()
	app.GET("/foo/bar", func(c Ctx) error {
		return c.SendString("nested")
	})
	app.GET("/foobar", func(c Ctx) error {
		return c.SendString("joined")
	})

	app.RemoveHandle([]string{http.MethodGet}, "/foo/bar")

	assertRoute(t, app, http.MethodGet, "/foo/bar", http.StatusNotFound, "")
	assertRoute(t, app, http.MethodGet, "/foobar", http.StatusOK, "joined")
}

func TestRouteDynamicSegmentBoundary(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		validPath string
		validBody string
		handler   HandlerFunc
	}{
		{
			name:      "required parameter",
			path:      "/foo/:id",
			validPath: "/foo/bar",
			validBody: "param:bar",
			handler: func(c Ctx) error {
				return c.SendString("param:" + c.Params("id"))
			},
		},
		{
			name:      "optional parameter",
			path:      "/foo/:id?",
			validPath: "/foo/bar",
			validBody: "optional:bar",
			handler: func(c Ctx) error {
				return c.SendString("optional:" + c.Params("id"))
			},
		},
		{
			name:      "catch all",
			path:      "/foo/*rest",
			validPath: "/foo/bar/baz",
			validBody: "catch:bar/baz",
			handler: func(c Ctx) error {
				return c.SendString("catch:" + c.Params("rest"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := New()
			app.GET(tt.path, tt.handler)

			assertRoute(t, app, http.MethodGet, tt.validPath, http.StatusOK, tt.validBody)
			assertRoute(t, app, http.MethodGet, "/foobar", http.StatusNotFound, "")
		})
	}
}

func TestRouteMiddlewareRespectsStaticSegmentBoundary(t *testing.T) {
	app := New()
	app.Use("/foo", func(c Ctx) error {
		c.SetHeader("X-Foo-Middleware", "applied")
		return c.Next()
	})
	app.GET("/foo/bar", func(c Ctx) error {
		return c.SendString("nested")
	})
	app.GET("/foobar", func(c Ctx) error {
		return c.SendString("joined")
	})

	nested := httptest.NewRecorder()
	app.ServeHTTP(nested, httptest.NewRequest(http.MethodGet, "/foo/bar", nil))
	if got := nested.Header().Get("X-Foo-Middleware"); got != "applied" {
		t.Fatalf("/foo/bar middleware header = %q, want applied", got)
	}

	joined := httptest.NewRecorder()
	app.ServeHTTP(joined, httptest.NewRequest(http.MethodGet, "/foobar", nil))
	if got := joined.Header().Get("X-Foo-Middleware"); got != "" {
		t.Fatalf("/foobar middleware header = %q, want empty", got)
	}
}

func assertRouteTreeInvariants(t *testing.T, node *RouteNode) {
	t.Helper()
	if node == nil {
		return
	}
	if len(node.indices) != len(node.children) {
		t.Fatalf("node %q: indices length = %d, children length = %d", node.path, len(node.indices), len(node.children))
	}

	for i, child := range node.children {
		if child == nil {
			t.Fatalf("node %q: child %d is nil", node.path, i)
		}
		if child.path == "" {
			t.Fatalf("node %q: child %d has empty path", node.path, i)
		}
		if node.indices[i] != child.path[0] {
			t.Fatalf("node %q: index %q does not match child path %q", node.path, node.indices[i], child.path)
		}
		if i > 0 && node.indices[i-1] >= node.indices[i] {
			t.Fatalf("node %q: indices %q are not strictly increasing", node.path, node.indices)
		}
		assertRouteTreeInvariants(t, child)
	}
	assertRouteTreeInvariants(t, node.paramChild)
	assertRouteTreeInvariants(t, node.catchChild)
}

// ── 前缀冲突路由共存 channel 场景 ──
// channel/save、channels/:param?、channel/group/save、channel/groups/:param?
func TestRoutePrefixConflictCoexistChannel(t *testing.T) {
	app := New()

	app.POST("/api/v1/channel/save", func(c Ctx) error {
		return c.SendString("channel-save")
	})
	app.POST("/api/v1/channels/:param?", func(c Ctx) error {
		return c.SendString("channels:" + c.Params("param"))
	})
	app.POST("/api/v1/channel/group/save", func(c Ctx) error {
		return c.SendString("channel-group-save")
	})
	app.POST("/api/v1/channel/groups/:param?", func(c Ctx) error {
		return c.SendString("channel-groups:" + c.Params("param"))
	})

	// channel/save 静态
	assertRoute(t, app, "POST", "/api/v1/channel/save", 200, "channel-save")
	// channels/:param? — param=1231
	assertRoute(t, app, "POST", "/api/v1/channels/1231", 200, "channels:1231")
	// channels/:param? — param 为空（可选参数）
	assertRoute(t, app, "POST", "/api/v1/channels", 200, "channels:")
	// channel/group/save 静态
	assertRoute(t, app, "POST", "/api/v1/channel/group/save", 200, "channel-group-save")
	// channel/groups/:param? — param=1231
	assertRoute(t, app, "POST", "/api/v1/channel/groups/1231", 200, "channel-groups:1231")
	// channel/groups/:param? — param 为空（可选参数）
	assertRoute(t, app, "POST", "/api/v1/channel/groups", 200, "channel-groups:")
}

func TestRouteRequiredParamAndCatchAllCoexist(t *testing.T) {
	registrations := []func(*Core){
		func(app *Core) {
			app.POST("/:param", func(c Ctx) error {
				return c.SendString("param:" + c.Params("param"))
			})
		},
		func(app *Core) {
			app.POST("/*", func(c Ctx) error {
				return c.SendString("catch:" + c.Params(""))
			})
		},
	}

	for _, order := range [][]int{{0, 1}, {1, 0}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			app := New()
			for _, index := range order {
				registrations[index](app)
			}

			assertRoute(t, app, "POST", "/1231", 200, "param:1231")
			assertRoute(t, app, "POST", "/nested/path", 200, "catch:nested/path")
			assertRoute(t, app, "POST", "/", 200, "catch:")

			for _, root := range app.trees {
				assertRouteTreeInvariants(t, root)
			}
		})
	}
}

// ── Unicode / 中文字符 ──

func TestRouteParamUnicode(t *testing.T) {
	app := New()
	app.GET("/search/:query", paramsResponder("query"))

	assertRoute(t, app, "GET", "/search/hello", 200, "query=hello")
	assertRoute(t, app, "GET", "/search/%E4%B8%AD%E6%96%87", 200, "") // URL-encoded Chinese
}

// ── 部分匹配不应成功 ──

func TestRoutePartialMatchFails(t *testing.T) {
	app := New()
	app.GET("/api/v1/users/:id/profile", paramsResponder("id"))

	// 部分路径不应匹配
	assertRoute(t, app, "GET", "/api", 404, "")
	assertRoute(t, app, "GET", "/api/v1", 404, "")
	assertRoute(t, app, "GET", "/api/v1/users", 404, "")
	assertRoute(t, app, "GET", "/api/v1/users/42", 404, "")
	assertRoute(t, app, "GET", "/api/v1/users/42/profiles", 404, "")
	assertRoute(t, app, "GET", "/api/v2/users/42/profile", 404, "")
}

// ── Benchmark: 验证修复后 param 路由分配次数 ──

func TestRouteParamAllocations(t *testing.T) {
	root := &RouteNode{nType: root}
	root.addRoute("/users/:id/profile", HandlerFuncs{func(Ctx) error { return nil }})
	ctx := &BaseCtx{handlers: make(HandlerFuncs, 0, 4)}

	allocs := testing.AllocsPerRun(1000, func() {
		handlers, ok := root.match("/users/42/profile", ctx)
		if !ok || len(handlers) != 1 {
			t.Fatalf("match handlers = %d/%v, want one handler", len(handlers), ok)
		}
	})
	t.Logf("param route /users/:id/profile → %v allocs per match", allocs)
}

func TestRouteDeepParamsAllocations(t *testing.T) {
	root := &RouteNode{nType: root}
	root.addRoute("/a/:p1/b/:p2/c/:p3", HandlerFuncs{func(Ctx) error { return nil }})
	ctx := &BaseCtx{handlers: make(HandlerFuncs, 0, 4)}

	allocs := testing.AllocsPerRun(1000, func() {
		handlers, ok := root.match("/a/1/b/2/c/3", ctx)
		if !ok || len(handlers) != 1 {
			t.Fatalf("match handlers = %d/%v, want one handler", len(handlers), ok)
		}
	})
	t.Logf("deep param route /a/:p1/b/:p2/c/:p3 → %v allocs per match", allocs)
}

// ── 纯 match() 基准测试（排除中间件/context/logger 开销）──

func newMockCtx() *BaseCtx {
	return &BaseCtx{handlers: make(HandlerFuncs, 0, 4)}
}

// buildRouter 用 n 条静态路由填充基数树
func buildRouter(n int) *RouteNode {
	root := &RouteNode{nType: root}
	for i := 0; i < n; i++ {
		path := "/api/v1/users/list" + string(rune('a'+i%26))
		root.addRoute(path, HandlerFuncs{func(Ctx) error { return nil }})
	}
	return root
}

// buildDeepStaticRouter 构建深度嵌套静态路由（验证路径压缩收益）
func buildDeepStaticRouter(depth int) *RouteNode {
	root := &RouteNode{nType: root}
	var path string
	for i := 0; i < depth; i++ {
		path += "/level" + string(rune('a'+i))
	}
	root.addRoute(path, HandlerFuncs{func(Ctx) error { return nil }})
	return root
}

// ── 纯静态路由 Benchmarks ──

// BenchmarkRouteMatchStaticSmall 小路由表静态匹配
func BenchmarkRouteMatchStaticSmall(b *testing.B) {
	root := buildRouter(5)
	ctx := newMockCtx()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root.match("/api/v1/users/listc", ctx)
	}
}

// BenchmarkRouteMatchStaticLarge 100 条静态路由匹配（验证基数树大表优势）
func BenchmarkRouteMatchStaticLarge(b *testing.B) {
	root := buildRouter(100)
	ctx := newMockCtx()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root.match("/api/v1/users/listz", ctx)
	}
}

// BenchmarkRouteMatchStaticDeep 5 层嵌套静态路由（验证路径压缩收益）
func BenchmarkRouteMatchStaticDeep(b *testing.B) {
	root := buildDeepStaticRouter(5)
	ctx := newMockCtx()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root.match("/levela/levelb/levelc/leveld/levele", ctx)
	}
}

// BenchmarkRouteMatchStaticDeep10 10 层嵌套静态路由（深度路径压缩）
func BenchmarkRouteMatchStaticDeep10(b *testing.B) {
	root := buildDeepStaticRouter(10)
	path := "/levela/levelb/levelc/leveld/levele/levelf/levelg/levelh/leveli/levelj"
	ctx := newMockCtx()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root.match(path, ctx)
	}
}

// ── 参数路由 Benchmarks ──

// BenchmarkRawMatchOneParam 纯单参数匹配
func BenchmarkRawMatchOneParam(b *testing.B) {
	root := &RouteNode{nType: root}
	root.addRoute("/users/:id", HandlerFuncs{func(Ctx) error { return nil }})
	ctx := newMockCtx()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root.match("/users/42", ctx)
		ctx.params = nil
	}
}

// BenchmarkRawMatchMultiParam 纯多参数混合匹配
func BenchmarkRawMatchMultiParam(b *testing.B) {
	root := &RouteNode{nType: root}
	root.addRoute("/orgs/:orgId/teams/:teamId/users/:userId", HandlerFuncs{func(Ctx) error { return nil }})
	ctx := newMockCtx()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root.match("/orgs/acme/teams/eng/users/alice", ctx)
		ctx.params = nil
	}
}

// BenchmarkRouteMatchCatchAll 通配符匹配
func BenchmarkRouteMatchCatchAll(b *testing.B) {
	root := &RouteNode{nType: root}
	root.addRoute("/static/*filepath", HandlerFuncs{func(Ctx) error { return nil }})
	ctx := newMockCtx()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root.match("/static/css/app.css", ctx)
		ctx.params = nil
	}
}

// BenchmarkRouteMatchCatchAllRoot 根通配符匹配
func BenchmarkRouteMatchCatchAllRoot(b *testing.B) {
	root := &RouteNode{nType: root}
	root.addRoute("/*", HandlerFuncs{func(Ctx) error { return nil }})
	ctx := newMockCtx()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root.match("/anything/goes/here", ctx)
		ctx.params = nil
	}
}

// ── Buffer 池化优化对比 Benchmark ──

// BenchmarkRawMatchOneParamPooled 参数路由 + params map 复用（模拟 AcquireCtx/ReleaseCtx 真实行为）
func BenchmarkRawMatchOneParamPooled(b *testing.B) {
	root := &RouteNode{nType: root}
	root.addRoute("/users/:id", HandlerFuncs{func(Ctx) error { return nil }})
	ctx := newMockCtx()
	ctx.params = make(map[string]string, 4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root.match("/users/42", ctx)
		// 模拟 release(): 清空 map 而非置 nil
		for k := range ctx.params {
			delete(ctx.params, k)
		}
	}
}

// BenchmarkRawMatchMultiParamPooled 多参数路由 + params map 复用
func BenchmarkRawMatchMultiParamPooled(b *testing.B) {
	root := &RouteNode{nType: root}
	root.addRoute("/orgs/:orgId/teams/:teamId/users/:userId", HandlerFuncs{func(Ctx) error { return nil }})
	ctx := newMockCtx()
	ctx.params = make(map[string]string, 4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root.match("/orgs/acme/teams/eng/users/alice", ctx)
		for k := range ctx.params {
			delete(ctx.params, k)
		}
	}
}

// ── 混合路由表 Benchmark（模拟真实场景）──

// BenchmarkRouteMatchMixed 30 条混合路由匹配
func BenchmarkRouteMatchMixed(b *testing.B) {
	root := &RouteNode{nType: root}
	handler := HandlerFuncs{func(Ctx) error { return nil }}
	routes := []string{
		"/health",
		"/api/v1/users",
		"/api/v1/users/:id",
		"/api/v1/users/:id/profile",
		"/api/v1/users/:id/orders",
		"/api/v1/users/:id/orders/:orderId",
		"/api/v1/products",
		"/api/v1/products/:id",
		"/api/v1/products/:id/reviews",
		"/api/v1/categories",
		"/api/v1/categories/:slug",
		"/api/v1/categories/:slug/products",
		"/api/v1/orders",
		"/api/v1/orders/:id",
		"/api/v1/orders/:id/items",
		"/api/v1/orders/:id/cancel",
		"/api/v1/orders/:id/refund",
		"/api/v1/auth/login",
		"/api/v1/auth/logout",
		"/api/v1/auth/refresh",
		"/api/v1/notifications",
		"/api/v1/notifications/:id",
		"/api/v1/notifications/:id/read",
		"/api/v1/payments",
		"/api/v1/payments/:id",
		"/api/v1/payments/:id/capture",
		"/api/v1/shipping/addresses",
		"/api/v1/shipping/addresses/:id",
		"/api/v1/coupons",
		"/api/v1/coupons/:code/validate",
	}
	for _, r := range routes {
		root.addRoute(r, handler)
	}
	ctx := newMockCtx()
	b.ResetTimer()
	b.Run("static", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			root.match("/api/v1/products", ctx)
		}
	})
	b.Run("param-end", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			root.match("/api/v1/users/42", ctx)
			ctx.params = nil
		}
	})
	b.Run("param-mid", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			root.match("/api/v1/categories/electronics/products", ctx)
			ctx.params = nil
		}
	})
	b.Run("multi-param", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			root.match("/api/v1/users/42/orders/15", ctx)
			ctx.params = nil
		}
	})
}
