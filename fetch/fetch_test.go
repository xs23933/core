package fetch

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func fetchResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func fetchGzipResponse(status int, body string) *http.Response {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(body))
	_ = gz.Close()

	resp := fetchResponse(status, "")
	resp.Header.Set("Content-Encoding", "gzip")
	resp.Header.Set("Content-Type", "application/json")
	resp.Body = io.NopCloser(bytes.NewReader(buf.Bytes()))
	return resp
}

func testSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func TestFetchSendsMethodHeadersAndJSONBody(t *testing.T) {
	var seenMethod, seenPath, seenCommon, seenOnce, seenType, seenBody string

	client := New("https://api.example.com").
		Client(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			seenMethod = req.Method
			seenPath = req.URL.Path
			seenCommon = req.Header.Get("X-App")
			seenOnce = req.Header.Get("X-Trace-ID")
			seenType = req.Header.Get(headerContentType)
			seenBody = string(body)
			return fetchResponse(http.StatusOK, `{"ok":true}`), nil
		})}).
		Header("X-App", "core")

	var out struct {
		OK bool `json:"ok"`
	}
	if err := client.Post("/login").
		Header("X-Trace-ID", "trace-1").
		JSON(map[string]any{"user": "song"}).
		Do(context.Background(), &out); err != nil {
		t.Fatal(err)
	}

	if seenMethod != http.MethodPost || seenPath != "/login" {
		t.Fatalf("method/path = %s %s, want POST /login", seenMethod, seenPath)
	}
	if seenCommon != "core" || seenOnce != "trace-1" {
		t.Fatalf("headers common/once = %q/%q, want core/trace-1", seenCommon, seenOnce)
	}
	if seenType != mimeJSON {
		t.Fatalf("content-type = %q, want %q", seenType, mimeJSON)
	}
	if !strings.Contains(seenBody, `"user":"song"`) {
		t.Fatalf("body = %q, want JSON user", seenBody)
	}
	if !out.OK {
		t.Fatalf("decoded OK = false, want true")
	}
}

func TestFetchBeforeAndAfterHooks(t *testing.T) {
	client := New("https://api.example.com").
		Client(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("X-Sign"); got != testSHA256([]byte("payload")) {
				t.Fatalf("signature = %q, want body hash", got)
			}
			return fetchResponse(http.StatusOK, `{"data":{"name":"decoded"}}`), nil
		})}).
		Before(func(ctx context.Context, req *http.Request, body []byte) error {
			req.Header.Set("X-Sign", testSHA256(body))
			return nil
		}).
		After(func(ctx context.Context, resp *http.Response, body []byte) ([]byte, error) {
			return []byte(`{"name":"decoded"}`), nil
		})

	var out struct {
		Name string `json:"name"`
	}
	if err := client.Post("/hook").String("payload").Do(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "decoded" {
		t.Fatalf("decoded name = %q, want decoded", out.Name)
	}
}

func TestFetchCookieCanBeEnabledAndDisabled(t *testing.T) {
	var firstCookie, secondCookie string
	calls := 0

	client := New("https://api.example.com").
		Client(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				firstCookie = req.Header.Get("Cookie")
				resp := fetchResponse(http.StatusOK, `{}`)
				resp.Header.Set("Set-Cookie", "sid=abc; Path=/")
				return resp, nil
			}
			secondCookie = req.Header.Get("Cookie")
			return fetchResponse(http.StatusOK, `{}`), nil
		})}).
		UseCookie(true)

	if err := client.Get("/cookie").Do(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Get("/cookie").Do(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if firstCookie != "" {
		t.Fatalf("first cookie = %q, want empty", firstCookie)
	}
	if secondCookie != "sid=abc" {
		t.Fatalf("second cookie = %q, want sid=abc", secondCookie)
	}

	client.UseCookie(false)
	if err := client.Get("/cookie").Do(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if secondCookie != "" {
		t.Fatalf("cookie after disable = %q, want empty", secondCookie)
	}
}

func TestFetchReturnsStatusError(t *testing.T) {
	client := New("https://api.example.com").
		Client(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return fetchResponse(http.StatusBadRequest, `{"error":"bad"}`), nil
		})})

	err := client.Get("/bad").Do(context.Background(), nil)
	ferr, ok := err.(*FetchError)
	if !ok {
		t.Fatalf("error = %T, want *FetchError", err)
	}
	if ferr.StatusCode != http.StatusBadRequest || string(ferr.Body) != `{"error":"bad"}` {
		t.Fatalf("fetch error = %#v, want status/body", ferr)
	}
}

func TestFetchTransportErrorWithoutResponseDoesNotPanic(t *testing.T) {
	transportErr := errors.New("dial failed")
	client := New("https://api.example.com").
		Client(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, transportErr
		})})

	err := client.Get("/down").Do(context.Background(), nil)
	if !errors.Is(err, transportErr) {
		t.Fatalf("error = %v, want transport error", err)
	}
}

func TestFetchDebugInfoIncludesRequestAndResponse(t *testing.T) {
	var debugLog string
	client := New("https://api.example.com").
		Client(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			resp := fetchResponse(http.StatusCreated, `{"ok":true}`)
			resp.Header.Set("X-Result", "created")
			return resp, nil
		})}).
		Header("X-App", "core").
		Before(func(ctx context.Context, req *http.Request, body []byte) error {
			req.Header.Set("X-Sign", testSHA256(body))
			return nil
		}).
		Debug(true)
	client.debugLog = func(msg string) {
		debugLog = msg
	}

	var out struct {
		OK bool `json:"ok"`
	}
	res, err := client.Post("/debug").
		Header("X-Trace-ID", "trace-1").
		JSON(map[string]any{"user": "song"}).
		Result(context.Background(), &out)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusCreated || !out.OK {
		t.Fatalf("result = %d/%v, want 201/ok", res.StatusCode, out.OK)
	}

	for _, want := range []string{
		"POST https://api.example.com/debug",
		"Request Headers:",
		"X-App: core",
		"X-Trace-Id: trace-1",
		"X-Sign: " + testSHA256([]byte(`{"user":"song"}`)),
		`Request Body: {"user":"song"}`,
		"Response Status: 201 Created",
		"Response Headers:",
		"X-Result: created",
		`Response Body: {"ok":true}`,
	} {
		if !strings.Contains(debugLog, want) {
			t.Fatalf("debug log missing %q in:\n%s", want, debugLog)
		}
	}
}

func TestFetchDebugInfoIncludesTransportError(t *testing.T) {
	transportErr := errors.New("dial failed")
	var debugLog string
	client := New("https://api.example.com").
		Client(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, transportErr
		})}).
		Debug(true)
	client.debugLog = func(msg string) {
		debugLog = msg
	}

	_, err := client.Get("/down").Result(context.Background(), nil)
	if !errors.Is(err, transportErr) {
		t.Fatalf("error = %v, want transport error", err)
	}

	for _, want := range []string{
		"GET https://api.example.com/down",
		"Request Headers:",
		"Request Body:",
		"Error:",
		"dial failed",
	} {
		if !strings.Contains(debugLog, want) {
			t.Fatalf("debug log missing %q in:\n%s", want, debugLog)
		}
	}
	if strings.Contains(debugLog, "Response Status:") {
		t.Fatalf("debug log includes response for transport error:\n%s", debugLog)
	}
}

func TestFetchDecodesGzipResponseBeforeResultAndDebug(t *testing.T) {
	var debugLog string
	client := New("https://api.example.com").
		Client(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return fetchGzipResponse(http.StatusOK, `{"ok":true}`), nil
		})}).
		Debug(true)
	client.debugLog = func(msg string) {
		debugLog = msg
	}

	var out struct {
		OK bool `json:"ok"`
	}
	res, err := client.Get("/gzip").Result(context.Background(), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("decoded OK = false, want true")
	}
	if string(res.Body) != `{"ok":true}` {
		t.Fatalf("result body = %q, want decompressed JSON", string(res.Body))
	}
	if !strings.Contains(debugLog, `Response Body: {"ok":true}`) {
		t.Fatalf("debug log missing decompressed body:\n%s", debugLog)
	}
	if strings.Contains(debugLog, "�") {
		t.Fatalf("debug log contains compressed bytes:\n%s", debugLog)
	}
}

func TestFetchResultExposesResponseHeaders(t *testing.T) {
	client := New("https://api.example.com").
		Client(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			resp := fetchResponse(http.StatusOK, `{"ok":true}`)
			resp.Header.Set("X-Token", "token-123")
			return resp, nil
		})})

	var out struct {
		OK bool `json:"ok"`
	}
	res, err := client.Get("/token").Result(context.Background(), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("decoded OK = false, want true")
	}
	if got := res.Header.Get("X-Token"); got != "token-123" {
		t.Fatalf("x-token = %q, want token-123", got)
	}
	if res.StatusCode != http.StatusOK || string(res.Body) != `{"ok":true}` {
		t.Fatalf("result status/body = %d/%q, want 200/body", res.StatusCode, string(res.Body))
	}
}

func TestFetchResultCanReturnMetadataWithoutDecodeTarget(t *testing.T) {
	client := New("https://api.example.com").
		Client(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			resp := fetchResponse(http.StatusOK, `{"ok":true}`)
			resp.Header.Set("X-Token", "token-123")
			return resp, nil
		})})

	res, err := client.Get("/token").Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Header.Get("X-Token"); got != "token-123" {
		t.Fatalf("x-token = %q, want token-123", got)
	}
	if res.StatusCode != http.StatusOK || string(res.Body) != `{"ok":true}` {
		t.Fatalf("result status/body = %d/%q, want 200/body", res.StatusCode, string(res.Body))
	}
}

func TestFetchShortcutMethods(t *testing.T) {
	old := Default
	defer func() { Default = old }()

	var seen []string
	Default = New("https://api.example.com").
		Client(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			seen = append(seen, req.Method+" "+req.URL.Path+" "+string(body))
			resp := fetchResponse(http.StatusOK, `{"ok":true}`)
			resp.Header.Set("X-Token", "shortcut-token")
			return resp, nil
		})})

	var out struct {
		OK bool `json:"ok"`
	}
	res, err := Get("/users", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !out.OK || res.Header.Get("X-Token") != "shortcut-token" {
		t.Fatalf("get out/header = %#v/%q, want decoded/token", out, res.Header.Get("X-Token"))
	}
	if _, err := Post("/users", map[string]any{"name": "song"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := Put("/users/1", map[string]any{"name": "new"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := Delete("/users/1", &out); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"GET /users ",
		`POST /users {"name":"song"}`,
		`PUT /users/1 {"name":"new"}`,
		"DELETE /users/1 ",
	}
	if len(seen) != len(want) {
		t.Fatalf("seen len = %d, want %d: %#v", len(seen), len(want), seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("seen[%d] = %q, want %q", i, seen[i], want[i])
		}
	}
}

func TestFetchReusableConciseMethods(t *testing.T) {
	var seen []string
	client := New("https://api.example.com").
		Client(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			seen = append(seen, req.Method+" "+req.URL.Path+" "+string(body))
			return fetchResponse(http.StatusOK, `{"ok":true}`), nil
		})})

	var out struct {
		OK bool `json:"ok"`
	}
	if _, err := client.DoGet(context.Background(), "/items", &out); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DoPost(context.Background(), "/items", map[string]any{"name": "song"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DoPut(context.Background(), "/items/1", map[string]any{"name": "new"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DoDelete(context.Background(), "/items/1", &out); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"GET /items ",
		`POST /items {"name":"song"}`,
		`PUT /items/1 {"name":"new"}`,
		"DELETE /items/1 ",
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("seen[%d] = %q, want %q", i, seen[i], want[i])
		}
	}
}

func TestFetchSetProxySupportsHTTPWithAndWithoutCredentials(t *testing.T) {
	tests := []struct {
		name      string
		proxyURL  func(string) string
		wantAuth  string
		targetURL string
	}{
		{
			name:      "without credentials",
			proxyURL:  func(addr string) string { return "http://" + addr },
			targetURL: "http://example.com/proxy",
		},
		{
			name:      "with credentials",
			proxyURL:  func(addr string) string { return "http://UsEr:PaSs@" + addr },
			wantAuth:  "Basic " + base64.StdEncoding.EncodeToString([]byte("UsEr:PaSs")),
			targetURL: "http://example.com/proxy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if got := req.URL.String(); got != tt.targetURL {
					t.Fatalf("proxied URL = %q, want %q", got, tt.targetURL)
				}
				if got := req.Header.Get("Proxy-Authorization"); got != tt.wantAuth {
					t.Fatalf("proxy auth = %q, want %q", got, tt.wantAuth)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer proxyServer.Close()

			proxyAddr := strings.TrimPrefix(proxyServer.URL, "http://")
			client := New().SetProxy(tt.proxyURL(proxyAddr))

			var out struct {
				OK bool `json:"ok"`
			}
			if _, err := client.Get(tt.targetURL).Result(context.Background(), &out); err != nil {
				t.Fatal(err)
			}
			if !out.OK {
				t.Fatalf("decoded OK = false, want true")
			}
		})
	}
}

func TestFetchSetProxySupportsSocks5UsernamePassword(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

		if err := readSocks5AuthHandshake(conn, "UsEr", "PaSs"); err != nil {
			serverErr <- err
			return
		}
		if err := readSocks5ConnectRequest(conn); err != nil {
			serverErr <- err
			return
		}

		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		if err != nil {
			serverErr <- err
			return
		}
		if !strings.HasPrefix(line, "GET /proxy HTTP/1.1") {
			serverErr <- fmt.Errorf("request line = %q, want GET /proxy", line)
			return
		}
		for {
			line, err = reader.ReadString('\n')
			if err != nil {
				serverErr <- err
				return
			}
			if line == "\r\n" {
				break
			}
		}
		_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 11\r\n\r\n{\"ok\":true}")
		serverErr <- err
	}()

	client := New().SetProxy("socks5://UsEr:PaSs@" + listener.Addr().String())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var out struct {
		OK bool `json:"ok"`
	}
	if _, err := client.Get("http://example.com/proxy").Result(ctx, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("decoded OK = false, want true")
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestFetchSetProxySupportsSocks5WithoutCredentials(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

		if err := readSocks5NoAuthHandshake(conn); err != nil {
			serverErr <- err
			return
		}
		if err := readSocks5ConnectRequest(conn); err != nil {
			serverErr <- err
			return
		}

		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		if err != nil {
			serverErr <- err
			return
		}
		if !strings.HasPrefix(line, "GET /proxy HTTP/1.1") {
			serverErr <- fmt.Errorf("request line = %q, want GET /proxy", line)
			return
		}
		for {
			line, err = reader.ReadString('\n')
			if err != nil {
				serverErr <- err
				return
			}
			if line == "\r\n" {
				break
			}
		}
		_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 11\r\n\r\n{\"ok\":true}")
		serverErr <- err
	}()

	client := New().SetProxy("socks5://" + listener.Addr().String())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var out struct {
		OK bool `json:"ok"`
	}
	if _, err := client.Get("http://example.com/proxy").Result(ctx, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("decoded OK = false, want true")
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func readSocks5AuthHandshake(rw io.ReadWriter, wantUser, wantPassword string) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(rw, header); err != nil {
		return err
	}
	if header[0] != 0x05 {
		return fmt.Errorf("socks version = %d, want 5", header[0])
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(rw, methods); err != nil {
		return err
	}
	hasPasswordAuth := false
	for _, method := range methods {
		if method == 0x02 {
			hasPasswordAuth = true
			break
		}
	}
	if !hasPasswordAuth {
		return fmt.Errorf("auth methods = %v, want username/password", methods)
	}
	if _, err := rw.Write([]byte{0x05, 0x02}); err != nil {
		return err
	}

	if _, err := io.ReadFull(rw, header); err != nil {
		return err
	}
	if header[0] != 0x01 {
		return fmt.Errorf("auth version = %d, want 1", header[0])
	}
	username := make([]byte, int(header[1]))
	if _, err := io.ReadFull(rw, username); err != nil {
		return err
	}
	if _, err := io.ReadFull(rw, header[:1]); err != nil {
		return err
	}
	password := make([]byte, int(header[0]))
	if _, err := io.ReadFull(rw, password); err != nil {
		return err
	}
	if string(username) != wantUser || string(password) != wantPassword {
		return fmt.Errorf("credentials = %q/%q, want %q/%q", username, password, wantUser, wantPassword)
	}
	_, err := rw.Write([]byte{0x01, 0x00})
	return err
}

func readSocks5NoAuthHandshake(rw io.ReadWriter) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(rw, header); err != nil {
		return err
	}
	if header[0] != 0x05 {
		return fmt.Errorf("socks version = %d, want 5", header[0])
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(rw, methods); err != nil {
		return err
	}
	hasNoAuth := false
	for _, method := range methods {
		if method == 0x00 {
			hasNoAuth = true
			break
		}
	}
	if !hasNoAuth {
		return fmt.Errorf("auth methods = %v, want no-auth", methods)
	}
	_, err := rw.Write([]byte{0x05, 0x00})
	return err
}

func readSocks5ConnectRequest(rw io.ReadWriter) error {
	header := make([]byte, 4)
	if _, err := io.ReadFull(rw, header); err != nil {
		return err
	}
	if header[0] != 0x05 || header[1] != 0x01 || header[2] != 0x00 {
		return fmt.Errorf("connect header = %v, want version 5 connect", header)
	}
	switch header[3] {
	case 0x01:
		if _, err := io.CopyN(io.Discard, rw.(io.Reader), 4); err != nil {
			return err
		}
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(rw, length); err != nil {
			return err
		}
		if _, err := io.CopyN(io.Discard, rw.(io.Reader), int64(length[0])); err != nil {
			return err
		}
	case 0x04:
		if _, err := io.CopyN(io.Discard, rw.(io.Reader), 16); err != nil {
			return err
		}
	default:
		return fmt.Errorf("address type = %d, want IPv4, domain, or IPv6", header[3])
	}
	if _, err := io.CopyN(io.Discard, rw.(io.Reader), 2); err != nil {
		return err
	}
	_, err := rw.Write([]byte{0x05, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	return err
}
