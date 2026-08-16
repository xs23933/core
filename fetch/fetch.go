package fetch

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/xs23933/core/v3"
	xproxy "golang.org/x/net/proxy"
)

type FetchBeforeHook func(context.Context, *http.Request, []byte) error
type FetchAfterHook func(context.Context, *http.Response, []byte) ([]byte, error)

const (
	headerContentType = "Content-Type"
	mimeJSON          = "application/json; charset=utf-8"
)

// Fetch is a reusable HTTP client with a fetch-like request builder.
//
// A Fetch instance owns shared configuration such as base URL, common headers,
// hooks, cookie behavior, and the underlying *http.Client. Each Get/Post/Method
// call creates an independent FetchRequest, so per-request headers, query
// values, and body data do not leak into later requests.
type Fetch struct {
	baseURL  string
	mu       sync.Mutex
	client   atomic.Value // *http.Client
	headers  atomic.Value // http.Header, copy-on-write
	before   atomic.Value // []FetchBeforeHook, copy-on-write
	after    atomic.Value // []FetchAfterHook, copy-on-write
	debug    atomic.Bool
	debugLog func(string)
}

// FetchRequest is one request built from a reusable Fetch client.
//
// Use request-level Header/Headers for headers that should only apply to this
// request. Use Fetch.Header/Fetch.Headers for headers that should be sent with
// every request from the client.
type FetchRequest struct {
	fetch   *Fetch
	method  string
	path    string
	headers http.Header
	query   url.Values
	body    []byte
	err     error
}

// FetchResult contains the response metadata and the final response body.
//
// Body is transparently gzip-decoded when needed and then passed through all
// After hooks. Header is cloned from the http.Response so callers can safely
// read values such as X-Token after Do/Result returns.
type FetchResult struct {
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
}

// FetchError is returned when the response status is outside the 2xx range.
//
// Header and Body are still populated so callers can inspect API error details
// and response headers, for example a refreshed token returned with a 401.
type FetchError struct {
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
}

func (e *FetchError) Error() string {
	if len(e.Body) == 0 {
		return fmt.Sprintf("%s", e.Status)
	}
	return fmt.Sprintf("%s: %s", e.Status, string(e.Body))
}

// New creates a reusable Fetch client.
//
// The optional baseURL is prepended to relative request paths. Cookie storage is
// disabled by default; call UseCookie(true) to enable a cookie jar.
func New(baseURL ...string) *Fetch {
	f := &Fetch{}
	if len(baseURL) > 0 {
		f.baseURL = strings.TrimRight(baseURL[0], "/")
	}
	f.client.Store(&http.Client{Timeout: 30 * time.Second})
	f.storeHeaders(make(http.Header))
	f.storeBefore([]FetchBeforeHook{})
	f.storeAfter([]FetchAfterHook{})
	f.debugLog = defaultFetchDebugLog
	return f
}

// NewFetch is an alias of New kept for callers who prefer the explicit name.
func NewFetch(baseURL ...string) *Fetch {
	return New(baseURL...)
}

// Default is used by the package-level Get/Post/Put/Delete helpers.
//
// Applications that need shared headers, hooks, cookies, or a custom transport
// can replace or configure this value during setup.
var Default = New()

// Get sends a GET request with Default and decodes the response into out.
func Get(path string, out any) (*FetchResult, error) {
	return Default.DoGet(context.Background(), path, out)
}

// Post sends a POST request with Default.
//
// The params value is encoded by type: []byte uses a raw body, string uses a
// string body, nil sends no body, and all other values are JSON encoded.
func Post(path string, params any, out any) (*FetchResult, error) {
	return Default.DoPost(context.Background(), path, params, out)
}

// Put sends a PUT request with Default.
//
// The params value is encoded by type: []byte uses a raw body, string uses a
// string body, nil sends no body, and all other values are JSON encoded.
func Put(path string, params any, out any) (*FetchResult, error) {
	return Default.DoPut(context.Background(), path, params, out)
}

// Delete sends a DELETE request with Default.
func Delete(path string, out any) (*FetchResult, error) {
	return Default.DoDelete(context.Background(), path, out)
}

// Del is an alias of Delete.
func Del(path string, out any) (*FetchResult, error) {
	return Delete(path, out)
}

// Client sets the underlying HTTP client.
//
// Passing nil resets to a default client with a 30 second timeout. If you need
// cookies, call UseCookie(true) after Client unless your custom client already
// has the desired Jar.
func (f *Fetch) Client(client *http.Client) *Fetch {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	f.client.Store(client)
	return f
}

// SetProxy configures an HTTP, HTTPS, or SOCKS5 proxy for this Fetch client.
//
// SOCKS5 proxy URLs may include username/password credentials, for example:
// socks5://user:password@127.0.0.1:1080. Passing an empty string disables the
// proxy by replacing the transport with a direct default transport.
func (f *Fetch) SetProxy(proxyURL string) *Fetch {
	if err := f.setProxy(proxyURL); err != nil {
		core.Erro("fetch: set proxy %q failed: %v", proxyURL, err)
	}
	return f
}

func (f *Fetch) setProxy(proxyURL string) error {
	transport := defaultFetchTransport()
	rawProxy := strings.TrimSpace(proxyURL)
	if rawProxy == "" {
		f.storeTransport(transport)
		return nil
	}

	px, err := url.Parse(rawProxy)
	if err != nil {
		return err
	}
	if px.Host == "" {
		return fmt.Errorf("proxy host is empty")
	}

	switch strings.ToLower(px.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(px)
	case "socks5":
		auth := socks5ProxyAuth(px)
		dialer, err := xproxy.SOCKS5("tcp", px.Host, auth, xproxy.Direct)
		if err != nil {
			return err
		}
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if d, ok := dialer.(xproxy.ContextDialer); ok {
				return d.DialContext(ctx, network, address)
			}
			return dialWithContext(ctx, dialer, network, address)
		}
	default:
		return fmt.Errorf("unsupported proxy scheme %q", px.Scheme)
	}

	f.storeTransport(transport)
	return nil
}

func socks5ProxyAuth(px *url.URL) *xproxy.Auth {
	if px == nil || px.User == nil {
		return nil
	}
	password, _ := px.User.Password()
	return &xproxy.Auth{
		User:     px.User.Username(),
		Password: password,
	}
}

func defaultFetchTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		next := transport.Clone()
		next.Proxy = nil
		return next
	}
	return &http.Transport{}
}

func (f *Fetch) storeTransport(transport http.RoundTripper) {
	f.mu.Lock()
	defer f.mu.Unlock()

	client := f.httpClient()
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	next := *client
	next.Transport = transport
	f.client.Store(&next)
}

func dialWithContext(ctx context.Context, dialer xproxy.Dialer, network, address string) (net.Conn, error) {
	type dialResult struct {
		conn net.Conn
		err  error
	}
	done := make(chan dialResult, 1)
	go func() {
		conn, err := dialer.Dial(network, address)
		if conn != nil && ctx.Err() != nil {
			_ = conn.Close()
		}
		done <- dialResult{conn: conn, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-done:
		return result.conn, result.err
	}
}

// UseCookie enables or disables cookie persistence for this Fetch client.
//
// Enabling creates an in-memory cookie jar when the current client has none.
// Disabling clears the client's Jar so later requests do not send stored cookies.
func (f *Fetch) UseCookie(enabled bool) *Fetch {
	f.mu.Lock()
	defer f.mu.Unlock()

	client := f.httpClient()
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if enabled {
		if client.Jar == nil {
			jar, _ := cookiejar.New(nil)
			client.Jar = jar
		}
	} else {
		client.Jar = nil
	}
	f.client.Store(client)
	return f
}

// Debug enables or disables verbose request/response logging for this Fetch client.
//
// When enabled, each request prints method, URL, final request headers, request
// body, response status, response headers, response body, and any returned error.
func (f *Fetch) Debug(enabled bool) *Fetch {
	f.debug.Store(enabled)
	return f
}

// Header sets a common header sent with every request from this Fetch client.
func (f *Fetch) Header(key, value string) *Fetch {
	f.mu.Lock()
	defer f.mu.Unlock()

	headers := cloneHeader(f.loadHeaders())
	headers.Set(key, value)
	f.storeHeaders(headers)
	return f
}

// Headers sets common headers sent with every request from this Fetch client.
func (f *Fetch) Headers(headers map[string]string) *Fetch {
	f.mu.Lock()
	defer f.mu.Unlock()

	next := cloneHeader(f.loadHeaders())
	for k, v := range headers {
		next.Set(k, v)
	}
	f.storeHeaders(next)
	return f
}

// Before appends a request hook that runs after the request is built and before
// it is sent.
//
// The hook receives the outgoing request and the raw body bytes. Typical uses
// include hash signatures, auth headers, request tracing, and timestamp headers.
func (f *Fetch) Before(hook FetchBeforeHook) *Fetch {
	if hook == nil {
		return f
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	hooks := append([]FetchBeforeHook(nil), f.loadBefore()...)
	hooks = append(hooks, hook)
	f.storeBefore(hooks)
	return f
}

// After appends a response hook that runs after the response body is read.
//
// The hook receives the response metadata and body, and returns the body that
// should be decoded or returned. Typical uses include decrypting, decompressing,
// or unwrapping an API envelope.
func (f *Fetch) After(hook FetchAfterHook) *Fetch {
	if hook == nil {
		return f
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	hooks := append([]FetchAfterHook(nil), f.loadAfter()...)
	hooks = append(hooks, hook)
	f.storeAfter(hooks)
	return f
}

// Method starts a request with an arbitrary HTTP method and path.
func (f *Fetch) Method(method, path string) *FetchRequest {
	return &FetchRequest{
		fetch:   f,
		method:  strings.ToUpper(method),
		path:    path,
		headers: make(http.Header),
		query:   make(url.Values),
	}
}

// Get starts a GET request.
func (f *Fetch) Get(path string) *FetchRequest {
	return f.Method(http.MethodGet, path)
}

// Post starts a POST request.
func (f *Fetch) Post(path string) *FetchRequest {
	return f.Method(http.MethodPost, path)
}

// Put starts a PUT request.
func (f *Fetch) Put(path string) *FetchRequest {
	return f.Method(http.MethodPut, path)
}

// Delete starts a DELETE request.
func (f *Fetch) Delete(path string) *FetchRequest {
	return f.Method(http.MethodDelete, path)
}

// Patch starts a PATCH request.
func (f *Fetch) Patch(path string) *FetchRequest {
	return f.Method(http.MethodPatch, path)
}

// DoGet sends a GET request and returns the response metadata.
func (f *Fetch) DoGet(ctx context.Context, path string, out any) (*FetchResult, error) {
	return f.Get(path).Result(ctx, out)
}

// DoPost sends a POST request and returns the response metadata.
//
// The params value is encoded by type: []byte uses a raw body, string uses a
// string body, nil sends no body, and all other values are JSON encoded.
func (f *Fetch) DoPost(ctx context.Context, path string, params any, out any) (*FetchResult, error) {
	return fetchWithParams(f.Post(path), ctx, params, out)
}

// DoPut sends a PUT request and returns the response metadata.
//
// The params value is encoded by type: []byte uses a raw body, string uses a
// string body, nil sends no body, and all other values are JSON encoded.
func (f *Fetch) DoPut(ctx context.Context, path string, params any, out any) (*FetchResult, error) {
	return fetchWithParams(f.Put(path), ctx, params, out)
}

// DoDelete sends a DELETE request and returns the response metadata.
func (f *Fetch) DoDelete(ctx context.Context, path string, out any) (*FetchResult, error) {
	return f.Delete(path).Result(ctx, out)
}

// Header sets a header for this request only.
//
// Request headers override common headers with the same key.
func (r *FetchRequest) Header(key, value string) *FetchRequest {
	r.headers.Set(key, value)
	return r
}

// Headers sets headers for this request only.
func (r *FetchRequest) Headers(headers map[string]string) *FetchRequest {
	for k, v := range headers {
		r.headers.Set(k, v)
	}
	return r
}

// Query sets one query parameter for this request.
func (r *FetchRequest) Query(key string, value any) *FetchRequest {
	r.query.Set(key, fmt.Sprint(value))
	return r
}

// Body sets the raw request body.
func (r *FetchRequest) Body(body []byte) *FetchRequest {
	r.body = append(r.body[:0], body...)
	return r
}

// String sets the request body from a string.
func (r *FetchRequest) String(body string) *FetchRequest {
	return r.Body([]byte(body))
}

// JSON marshals v as the request body and sets Content-Type to application/json.
func (r *FetchRequest) JSON(v any) *FetchRequest {
	body, err := sonic.Marshal(v)
	if err != nil {
		r.body = nil
		r.err = err
		r.Header(headerContentType, mimeJSON)
		return r
	}
	r.body = body
	r.Header(headerContentType, mimeJSON)
	return r
}

// Do sends the request and decodes the response body into out.
//
// It is the convenience form of Result when callers only need decoded data. Use
// Result when response metadata is needed, such as reading X-Token from headers.
func (r *FetchRequest) Do(ctx context.Context, out any) error {
	_, err := r.Result(ctx, out)
	return err
}

// Result sends the request and returns response metadata.
//
// Passing an optional out decodes the response body into it. Supported out
// values are nil, *[]byte, *string, or a JSON target such as a struct/map
// pointer. Omitting out skips decode and returns only FetchResult. The returned
// Body is the final body after all After hooks.
func (r *FetchRequest) Result(ctx context.Context, out ...any) (*FetchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(out) > 1 {
		return nil, fmt.Errorf("fetch: Result accepts at most one decode target")
	}
	if r.method == "" {
		r.method = http.MethodGet
	}
	if r.err != nil {
		return nil, r.err
	}

	reqURL, err := r.url()
	if err != nil {
		return nil, err
	}
	body := append([]byte(nil), r.body...)
	req, err := http.NewRequestWithContext(ctx, r.method, reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vals := range r.fetch.loadHeaders() {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	for k, vals := range r.headers {
		req.Header.Del(k)
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	for _, hook := range r.fetch.loadBefore() {
		if err := hook(ctx, req, body); err != nil {
			r.fetch.logDebug(req, body, nil, nil, err)
			return nil, err
		}
	}
	core.D("%s %s", r.method, reqURL)
	resp, err := r.fetch.httpClient().Do(req)
	if err != nil {
		if resp != nil {
			core.D("%s %s(%d): %v", r.method, reqURL, resp.StatusCode, err)
			resp.Body.Close()
		} else {
			core.D("%s %s: %v", r.method, reqURL, err)
		}
		r.fetch.logDebug(req, body, resp, nil, err)
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := readResponseBody(resp)
	if err != nil {
		core.D("failed to read response body: %v", err)
		r.fetch.logDebug(req, body, resp, nil, err)
		return nil, err
	}
	for _, hook := range r.fetch.loadAfter() {
		respBody, err = hook(ctx, resp, respBody)
		if err != nil {
			core.D("err to hook response: %v", err)
			r.fetch.logDebug(req, body, resp, respBody, err)
			return nil, err
		}
	}
	result := &FetchResult{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Header:     cloneHeader(resp.Header),
		Body:       append([]byte(nil), respBody...),
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		err := &FetchError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Header:     result.Header,
			Body:       result.Body,
		}
		r.fetch.logDebug(req, body, resp, respBody, err)
		return result, err
	}
	if len(out) == 1 {
		if err := decodeFetchBody(respBody, out[0]); err != nil {
			core.D("decode Fetch Body err: %v", err)
			r.fetch.logDebug(req, body, resp, respBody, err)
			return result, err
		}
	}
	r.fetch.logDebug(req, body, resp, respBody, nil)
	return result, nil
}

func (r *FetchRequest) url() (string, error) {
	raw := r.path
	if raw == "" {
		raw = "/"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if !u.IsAbs() && r.fetch.baseURL != "" {
		base, err := url.Parse(r.fetch.baseURL)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(raw, "/") {
			base.Path = strings.TrimRight(base.Path, "/") + raw
		} else {
			base.Path = strings.TrimRight(base.Path, "/") + "/" + raw
		}
		base.RawQuery = u.RawQuery
		u = base
	}
	q := u.Query()
	for k, vals := range r.query {
		q.Del(k)
		for _, v := range vals {
			q.Add(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (f *Fetch) httpClient() *http.Client {
	if client, ok := f.client.Load().(*http.Client); ok && client != nil {
		return client
	}
	return http.DefaultClient
}

func (f *Fetch) loadHeaders() http.Header {
	if headers, ok := f.headers.Load().(http.Header); ok && headers != nil {
		return headers
	}
	return make(http.Header)
}

func (f *Fetch) storeHeaders(headers http.Header) {
	f.headers.Store(headers)
}

func (f *Fetch) loadBefore() []FetchBeforeHook {
	if hooks, ok := f.before.Load().([]FetchBeforeHook); ok {
		return hooks
	}
	return nil
}

func (f *Fetch) storeBefore(hooks []FetchBeforeHook) {
	f.before.Store(hooks)
}

func (f *Fetch) loadAfter() []FetchAfterHook {
	if hooks, ok := f.after.Load().([]FetchAfterHook); ok {
		return hooks
	}
	return nil
}

func (f *Fetch) storeAfter(hooks []FetchAfterHook) {
	f.after.Store(hooks)
}

func readResponseBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	reader := io.Reader(resp.Body)
	if !resp.Uncompressed && hasContentEncoding(resp.Header, "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	}
	return io.ReadAll(reader)
}

func hasContentEncoding(headers http.Header, encoding string) bool {
	for _, value := range headers.Values("Content-Encoding") {
		for part := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), encoding) {
				return true
			}
		}
	}
	return false
}

func (f *Fetch) logDebug(req *http.Request, reqBody []byte, resp *http.Response, respBody []byte, err error) {
	if !f.debug.Load() {
		return
	}
	log := f.debugLog
	if log == nil {
		log = defaultFetchDebugLog
	}
	log(formatFetchDebug(req, reqBody, resp, respBody, err))
}

func defaultFetchDebugLog(msg string) {
	core.Log("%s", msg)
}

func formatFetchDebug(req *http.Request, reqBody []byte, resp *http.Response, respBody []byte, err error) string {
	var b strings.Builder
	b.WriteString("[fetch debug]\n")
	if req != nil {
		b.WriteString("Request: ")
		b.WriteString(req.Method)
		b.WriteByte(' ')
		b.WriteString(req.URL.String())
		b.WriteByte('\n')
		b.WriteString("Request Headers:\n")
		writeHeaderDebug(&b, req.Header)
		b.WriteString("Request Body: ")
		b.Write(reqBody)
		b.WriteByte('\n')
		b.WriteByte('\n')
	}
	if resp != nil {
		b.WriteString("Response Status: ")
		b.WriteString(formatResponseStatus(resp))
		b.WriteByte('\n')
		b.WriteString("Response Headers:\n")
		writeHeaderDebug(&b, resp.Header)
		b.WriteString("Response Body: ")
		b.Write(respBody)
		b.WriteByte('\n')
		b.WriteByte('\n')
	}
	if err != nil {
		b.WriteString("Error: ")
		b.WriteString(err.Error())
		b.WriteByte('\n')
	}
	return b.String()
}

func formatResponseStatus(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	statusText := strings.TrimSpace(resp.Status)
	codeText := fmt.Sprint(resp.StatusCode)
	if statusText == "" || statusText == codeText {
		if text := http.StatusText(resp.StatusCode); text != "" {
			return codeText + " " + text
		}
		return codeText
	}
	if strings.HasPrefix(statusText, codeText+" ") {
		return statusText
	}
	return codeText + " " + statusText
}

func writeHeaderDebug(b *strings.Builder, headers http.Header) {
	if len(headers) == 0 {
		return
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range headers[k] {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteByte('\n')
		}
	}
}

func cloneHeader(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	for k, vals := range src {
		dst[k] = append([]string(nil), vals...)
	}
	return dst
}

func decodeFetchBody(body []byte, out any) error {
	if out == nil {
		return nil
	}
	switch v := out.(type) {
	case *[]byte:
		*v = append((*v)[:0], body...)
		return nil
	case *string:
		*v = string(body)
		return nil
	default:
		if len(body) == 0 {
			return nil
		}
		return sonic.Unmarshal(body, out)
	}
}

func fetchWithParams(req *FetchRequest, ctx context.Context, params any, out any) (*FetchResult, error) {
	switch v := params.(type) {
	case nil:
	case []byte:
		req.Body(v)
	case string:
		req.String(v)
	default:
		req.JSON(v)
	}
	return req.Result(ctx, out)
}
