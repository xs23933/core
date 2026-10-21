package core

import (
	"net/http/httptest"
	"testing"
)

// A handler error must not append an unrelated error payload after a response
// has been committed (which would corrupt encoded, binary, or streamed bodies).
func TestHandlerErrorAfterWriteDoesNotAppendBody(t *testing.T) {
	app := New()
	app.GET("/", func(c Ctx) error {
		if err := c.SendString("partial"); err != nil {
			return err
		}
		return NewError(500, "failed later")
	})
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || rec.Body.String() != "partial" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}
