package core

import (
	"fmt"
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
	root := &RouteNode{
		path:        "/",
		nType:       root,
		staticChild: make(map[string]*RouteNode),
	}
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
	root := &RouteNode{
		path:        "/",
		nType:       root,
		staticChild: make(map[string]*RouteNode),
	}
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
