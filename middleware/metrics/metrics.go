package metrics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xs23933/core/v3"
)

type Config struct {
	NormalizePath bool
}

var DefaultConfig = Config{
	NormalizePath: true,
}

type Metrics struct {
	cfg     Config
	records sync.Map // map[string]*record
}

type record struct {
	count     atomic.Uint64
	latencyNs atomic.Uint64
}

func New(conf ...Config) (*Metrics, core.HandlerFunc) {
	cfg := DefaultConfig
	if len(conf) > 0 {
		cfg = conf[0]
	}
	m := &Metrics{cfg: cfg}
	return m, m.Middleware()
}

func (m *Metrics) Middleware() core.HandlerFunc {
	return func(c core.Ctx) error {
		start := time.Now()
		err := c.Next()

		method := c.Method()
		path := c.Path()
		if m.cfg.NormalizePath {
			path = normalizePath(path)
		}
		status := c.GetStatus()

		key := metricKey(method, path, status)
		val, _ := m.records.LoadOrStore(key, &record{})
		rec := val.(*record)
		rec.count.Add(1)
		rec.latencyNs.Add(uint64(time.Since(start).Nanoseconds()))
		return err
	}
}

func (m *Metrics) Handler() core.HandlerFunc {
	return func(c core.Ctx) error {
		c.SetHeader(core.HeaderContentType, "text/plain; version=0.0.4; charset=utf-8")
		return c.SendString(m.RenderPrometheus())
	}
}

func (m *Metrics) RenderPrometheus() string {
	type row struct {
		method string
		path   string
		status int
		count  uint64
		sumNs  uint64
	}
	rows := make([]row, 0, 32)

	m.records.Range(func(k, v any) bool {
		method, path, status, ok := parseMetricKey(k.(string))
		if !ok {
			return true
		}
		rec := v.(*record)
		rows = append(rows, row{
			method: method,
			path:   path,
			status: status,
			count:  rec.count.Load(),
			sumNs:  rec.latencyNs.Load(),
		})
		return true
	})

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].method != rows[j].method {
			return rows[i].method < rows[j].method
		}
		if rows[i].path != rows[j].path {
			return rows[i].path < rows[j].path
		}
		return rows[i].status < rows[j].status
	})

	var b strings.Builder
	b.WriteString("# HELP core_http_requests_total Total number of HTTP requests.\n")
	b.WriteString("# TYPE core_http_requests_total counter\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf(
			`core_http_requests_total{method=%q,path=%q,status=%q} %d`+"\n",
			escapeLabel(r.method),
			escapeLabel(r.path),
			strconv.Itoa(r.status),
			r.count,
		))
	}

	b.WriteString("# HELP core_http_request_duration_seconds_sum Total request duration in seconds.\n")
	b.WriteString("# TYPE core_http_request_duration_seconds_sum counter\n")
	for _, r := range rows {
		sumSec := float64(r.sumNs) / float64(time.Second)
		b.WriteString(fmt.Sprintf(
			`core_http_request_duration_seconds_sum{method=%q,path=%q,status=%q} %.9f`+"\n",
			escapeLabel(r.method),
			escapeLabel(r.path),
			strconv.Itoa(r.status),
			sumSec,
		))
	}

	b.WriteString("# HELP core_http_request_duration_seconds_count Total duration sample count.\n")
	b.WriteString("# TYPE core_http_request_duration_seconds_count counter\n")
	for _, r := range rows {
		fmt.Fprintf(&b, `core_http_request_duration_seconds_count{method=%q,path=%q,status=%q} %d`+"\n",
			escapeLabel(r.method),
			escapeLabel(r.path),
			strconv.Itoa(r.status),
			r.count)
	}
	return b.String()
}

func Mount(app *core.Core, path string, m *Metrics) {
	if path == "" {
		path = "/metrics"
	}
	app.GET(path, m.Handler())
}

func metricKey(method, path string, status int) string {
	return method + "|" + path + "|" + strconv.Itoa(status)
}

func parseMetricKey(key string) (method, path string, status int, ok bool) {
	parts := strings.Split(key, "|")
	if len(parts) != 3 {
		return "", "", 0, false
	}
	st, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", "", 0, false
	}
	return parts[0], parts[1], st, true
}

func normalizePath(path string) string {
	if path == "" {
		return "/"
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		p := parts[i]
		if p == "" {
			continue
		}
		if isNumeric(p) || len(p) >= 16 {
			parts[i] = ":id"
		}
	}
	out := strings.Join(parts, "/")
	if out == "" {
		return "/"
	}
	return out
}

func isNumeric(v string) bool {
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	return v != ""
}

func escapeLabel(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "\"", "\\\"")
	v = strings.ReplaceAll(v, "\n", "\\n")
	return v
}
