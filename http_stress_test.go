package core

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestServeHTTPConcurrentRequestIsolationStress(t *testing.T) {
	app := New(Options{"debug": false})
	app.GET("/stress/:id", func(c Ctx) error {
		return c.SendString(c.Params("id") + ":" + c.Query("value"))
	})

	const workers, requests = 32, 100
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for request := 0; request < requests; request++ {
				id := fmt.Sprintf("%d-%d", worker, request)
				rec := httptest.NewRecorder()
				app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stress/"+id+"?value="+id, nil))
				if rec.Code != http.StatusOK || rec.Body.String() != id+":"+id {
					t.Errorf("request %s: status=%d body=%q", id, rec.Code, rec.Body.String())
					return
				}
			}
		}(worker)
	}
	close(start)
	wg.Wait()
}
