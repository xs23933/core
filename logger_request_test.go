package core

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestNewDoesNotLogRequestsByDefault(t *testing.T) {
	restoreLoggerGlobals(t)

	app := New(Options{"debug": true, "colorful": false})
	var output bytes.Buffer
	golog = log.New(&output, "", 0)
	forceColor = false
	isTerm = false

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if output.Len() != 0 {
		t.Fatalf("log = %q, want no request log by default", output.String())
	}
}

func TestExplicitLoggerLogsNotFoundRequests(t *testing.T) {
	restoreLoggerGlobals(t)

	app := New(Options{"debug": false})
	var output bytes.Buffer
	logout = &output
	golog = log.New(&output, "", 0)
	forceColor = false
	isTerm = false
	app.Use(Logger())

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if !strings.Contains(output.String(), "[E] 404 GET /api/v1") {
		t.Fatalf("log = %q, want explicit Logger to record the 404 request", output.String())
	}
}

func TestExplicitLoggerLogsReturnedErrors(t *testing.T) {
	restoreLoggerGlobals(t)

	tests := []struct {
		name       string
		path       string
		err        error
		wantStatus int
	}{
		{name: "framework error", path: "/bad", err: NewError(http.StatusBadRequest, "bad request"), wantStatus: http.StatusBadRequest},
		{name: "plain error", path: "/fail", err: errors.New("failed"), wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := New(Options{"debug": false})
			var output bytes.Buffer
			app.Use(Logger(LoggerConfig{Debug: true, Output: &output}))
			app.GET(tt.path, func(Ctx) error {
				return tt.err
			})

			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			statusLog := "[E] " + strconv.Itoa(tt.wantStatus) + " GET " + tt.path
			if !strings.Contains(output.String(), statusLog) {
				t.Fatalf("log = %q, want it to contain %q", output.String(), statusLog)
			}
		})
	}
}

func TestRequestLoggerUsesErrorLevelForHTTPFailures(t *testing.T) {
	restoreLoggerGlobals(t)

	tests := []struct {
		name       string
		method     string
		path       string
		register   func(*Core)
		wantStatus int
	}{
		{
			name:       "unmatched route",
			method:     http.MethodGet,
			path:       "/api/v1",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "unsupported method",
			method:     "BREW",
			path:       "/api/v1",
			wantStatus: http.StatusNotFound,
		},
		{
			name:   "matched server error",
			method: http.MethodGet,
			path:   "/fail",
			register: func(app *Core) {
				app.GET("/fail", func(c Ctx) error {
					return c.SendStatus(http.StatusInternalServerError, "failed")
				})
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := New(Options{"debug": false})
			var output bytes.Buffer
			app.Use(Logger(LoggerConfig{
				App:    app,
				Debug:  true,
				Output: &output,
			}))
			if tt.register != nil {
				tt.register(app)
			}

			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusNotFound && rec.Body.String() != ErrNotFound.Error() {
				t.Fatalf("body = %q, want %q", rec.Body.String(), ErrNotFound.Error())
			}
			statusLog := "[E] " + strconv.Itoa(tt.wantStatus) + " " + tt.method + " " + tt.path
			if !strings.Contains(output.String(), statusLog) {
				t.Fatalf("log = %q, want it to contain %q", output.String(), statusLog)
			}
		})
	}
}

func TestRequestLoggerSuppressesAccessLogsWhenDebugIsDisabled(t *testing.T) {
	restoreLoggerGlobals(t)

	app := New(Options{"debug": false})
	var output bytes.Buffer
	app.Use(Logger(LoggerConfig{
		App:    app,
		Debug:  false,
		Output: &output,
	}))

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if output.Len() != 0 {
		t.Fatalf("log = %q, want no access log", output.String())
	}
}

func restoreLoggerGlobals(t *testing.T) {
	t.Helper()
	previousLogout := logout
	previousGoLog := golog
	previousIsTerm := isTerm
	previousForceColor := forceColor
	t.Cleanup(func() {
		logout = previousLogout
		golog = previousGoLog
		isTerm = previousIsTerm
		forceColor = previousForceColor
	})
}
