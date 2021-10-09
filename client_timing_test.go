package core

import (
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

func BenchmarkClientGetEndToEnd1Inmemory(b *testing.B) {
	benchmarkClientGetEndToEndInmemory(b, 1)
}

func BenchmarkClientGetEndToEnd10Inmemory(b *testing.B) {
	benchmarkClientGetEndToEndInmemory(b, 10)
}

func BenchmarkClientGetEndToEnd100Inmemory(b *testing.B) {
	benchmarkClientGetEndToEndInmemory(b, 100)
}

func BenchmarkClientGetEndToEnd1000Inmemory(b *testing.B) {
	benchmarkClientGetEndToEndInmemory(b, 1000)
}

func BenchmarkClientGetEndToEnd10KInmemory(b *testing.B) {
	benchmarkClientGetEndToEndInmemory(b, 10000)
}

func benchmarkClientGetEndToEndInmemory(b *testing.B, parallelism int) {
	ln := NewInmemoryListener()

	requestURI := "/foo"
	ch := make(chan struct{})
	go func() {
		core := New()
		core.Get("/foo", func(c *Ctx) {
			c.SendString(requestURI)
		})
		if err := core.Serve(ln); err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			b.Errorf("error when serving requests: %s", err)
		}
		close(ch)
	}()

	c := &http.Client{
		Transport: &http.Transport{
			Dial:                func(_, _ string) (net.Conn, error) { return ln.Dial() },
			MaxIdleConnsPerHost: parallelism * runtime.GOMAXPROCS(-1),
		},
	}

	url := fmt.Sprintf("http://unused.host%s", requestURI)
	b.SetParallelism(parallelism)
	b.RunParallel(func(p *testing.PB) {
		for p.Next() {
			resp, err := c.Get(url)
			if err != nil {
				b.Fatalf("unexpected error: %s", err)
			}
			if resp.StatusCode != http.StatusOK {
				b.Fatalf("unexpected status code: %d. Expecting %d", resp.StatusCode, http.StatusOK)
			}
			body, err := ioutil.ReadAll(resp.Body)
			resp.Body.Close()

			if err != nil {
				b.Fatalf("unexpected error when reading response body: %s", err)
			}
			if string(body) != requestURI {
				b.Fatalf("unexpected response %q. Expecting %q", body, requestURI)
			}
		}
	})
	ln.Close()
	select {
	case <-ch:
	case <-time.After(time.Second):
		b.Fatalf("server wasn't stopped")
	}
}
