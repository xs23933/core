package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestTimeoutPreservesClientAndParentDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(60 * time.Millisecond):
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	original := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, tc := range []struct {
		name            string
		timeout, parent time.Duration
		failed          bool
	}{
		{"请求超时", 10 * time.Millisecond, time.Second, true},
		{"父期限优先", time.Second, 10 * time.Millisecond, true},
		{"足够预算", time.Second, time.Second, false},
		{"零值默认", 0, time.Second, false},
		{"负值默认", -time.Second, time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := New(server.URL).Client(original).Timeout(tc.timeout).SetProxy("").UseCookie(true)
			if original.Timeout != time.Second || client.httpClient().CheckRedirect == nil || client.httpClient().Jar == nil {
				t.Fatal("超时配置丢失客户端设置或修改共享客户端")
			}
			ctx, cancel := context.WithTimeout(context.Background(), tc.parent)
			defer cancel()
			_, err := client.Get("/").Result(ctx)
			if tc.failed && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error=%v", err)
			}
			if !tc.failed && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTimeoutConcurrentConfigurationAndRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	client := New(server.URL)
	var tasks sync.WaitGroup
	for range 8 {
		tasks.Add(1)
		go func() {
			defer tasks.Done()
			for range 20 {
				client.Timeout(time.Second)
				if _, err := client.Get("/").Result(context.Background()); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	tasks.Wait()
}

func TestTimeoutSurvivesHTTPProxyConfiguration(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(80 * time.Millisecond):
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer proxy.Close()
	for _, tc := range []struct {
		timeout time.Duration
		failed  bool
	}{{10 * time.Millisecond, true}, {time.Second, false}, {0, false}} {
		client := New("http://provider.invalid").Timeout(tc.timeout).SetProxy(proxy.URL)
		_, err := client.Get("/balance").Result(context.Background())
		if (err != nil) != tc.failed {
			t.Fatalf("proxy timeout=%s error=%v", tc.timeout, err)
		}
	}
}
