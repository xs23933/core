package core

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoverySuppressesHTTPAbortHandlerOnly(t *testing.T) {
	originalWriter := DefaultErrorWriter
	defer func() { DefaultErrorWriter = originalWriter }()

	var recoveryLog bytes.Buffer
	DefaultErrorWriter = &recoveryLog
	app := New(Options{"debug": true})
	app.GET("/abort", func(Ctx) error { panic(http.ErrAbortHandler) })
	app.GET("/abort-copy", func(Ctx) error { panic(errors.New(http.ErrAbortHandler.Error())) })
	app.GET("/boom", func(Ctx) error { panic("boom") })

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
	if recoveryLog.Len() != 0 {
		t.Fatalf("standard abort was logged as a panic: %s", recoveryLog.String())
	}
	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort-copy", nil))
	if recoveryLog.Len() != 0 {
		t.Fatalf("copied standard abort was logged as a panic: %s", recoveryLog.String())
	}

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	if !strings.Contains(recoveryLog.String(), "boom") {
		t.Fatalf("non-abort panic was not recovered: %s", recoveryLog.String())
	}
}
