package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouteCaseInsensitive(t *testing.T) {
	for _, registered := range []string{"transferIn", "transferin"} {
		t.Run(registered, func(t *testing.T) {
			app := New(Options{"case-sensitive": false})
			app.Use("/API", func(c Ctx) error {
				c.Set("case_middleware", "yes")
				return c.Next()
			})
			app.POST("/Api/"+registered+"/:walletId", func(c Ctx) error {
				return c.SendString(c.Params("walletId") + "|" + c.Query("ref") + "|" + c.Path() + "|" + c.GetString("case_middleware"))
			})
			for _, path := range []string{"/api/transferIn/AbC123", "/API/transferin/AbC123", "/Api/TRANSFERIN/AbC123"} {
				rec := httptest.NewRecorder()
				app.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path+"?ref=XyZ", nil))
				if rec.Code != http.StatusOK || rec.Body.String() != "AbC123|XyZ|"+path+"|yes" {
					t.Errorf("%s: status=%d body=%q", path, rec.Code, rec.Body.String())
				}
			}
			app.RemoveHandle([]string{http.MethodPost}, "/API/TRANSFERIN/:walletId")
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/transferIn/AbC123", nil))
			if rec.Code != http.StatusNotFound {
				t.Errorf("removed route status = %d", rec.Code)
			}
		})
	}
}

func TestRouteCaseSensitiveCompatibility(t *testing.T) {
	for _, options := range []Options{nil, {"case-sensitive": true}} {
		app := New(options)
		app.GET("/transferIn", func(c Ctx) error { return c.SendString("camel") })
		app.GET("/transferin", func(c Ctx) error { return c.SendString("lower") })
		for path, want := range map[string]string{"/transferIn": "camel", "/transferin": "lower"} {
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK || rec.Body.String() != want {
				t.Errorf("%s: status=%d body=%q", path, rec.Code, rec.Body.String())
			}
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/TRANSFERIN", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("case sensitive route status=%d", rec.Code)
		}
	}
}

func TestRouteCaseInsensitiveTreeBoundaries(t *testing.T) {
	app := New(Options{"case-sensitive": false})
	group := app.Group("/Api")
	group.GET("/Files/*filePath", func(c Ctx) error { return c.SendString(c.Params("filePath")) })
	group.GET("/Users/:userId/Profile", func(c Ctx) error { return c.SendString(c.Params("userId")) })
	group.GET("/Maybe/:userId?", func(c Ctx) error { return c.SendString("optional:" + c.Params("userId")) })
	group.GET("/Users/New/Profile", func(c Ctx) error { return c.SendString("static") })
	group.ALL("/TransferIn", func(c Ctx) error { return c.SendString("first") })
	group.ALL("/transferin", func(c Ctx) error { return c.SendString("replacement") })
	for path, want := range map[string]string{
		"/API/FILES/AbC/文档.txt":  "AbC/文档.txt",
		"/API/USERS/AbC/PROFILE": "AbC",
		"/api/users/NEW/profile": "static",
		"/API/MAYBE":             "optional:",
		"/API/MAYBE/XyZ":         "optional:XyZ",
		"/API/TRANSFERIN":        "replacement",
	} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s: status=%d body=%q, want %q", path, rec.Code, rec.Body.String(), want)
		}
	}
	app.RemoveHandle([]string{MethodAll}, "/API/TRANSFERIN")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(method, "/api/transferin", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("removed ALL route %s: status=%d", method, rec.Code)
		}
	}
}
