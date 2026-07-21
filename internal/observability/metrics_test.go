package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsHTTPHandler(t *testing.T) {
	m, err := NewMetrics(func() int { return 3 })
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}

	// Drive one request through the middleware so CounterVec/HistogramVec populate.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	m.Middleware(inner).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping", nil))

	rec := httptest.NewRecorder()
	m.HTTPHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	body, _ := io.ReadAll(rec.Body)
	for _, want := range []string{
		"puls_http_requests_total",
		"puls_http_request_duration_seconds",
		"puls_heartbeats_total 0",
		"puls_devices_connected 3",
		"puls_ws_messages_rejected_total 0",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics output missing %q\nfull output:\n%s", want, body)
		}
	}
}

func TestMetricsDevicesConnectedGauge(t *testing.T) {
	count := 0
	m, _ := NewMetrics(func() int { return count })

	gather := func() string {
		rec := httptest.NewRecorder()
		m.HTTPHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		b, _ := io.ReadAll(rec.Body)
		return string(b)
	}

	if !strings.Contains(gather(), "puls_devices_connected 0") {
		t.Error("expected puls_devices_connected 0")
	}
	count = 5
	if !strings.Contains(gather(), "puls_devices_connected 5") {
		t.Error("expected puls_devices_connected 5")
	}
}

func TestMetricsMiddlewareRecordsRequest(t *testing.T) {
	m, _ := NewMetrics(func() int { return 0 })

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := m.Middleware(inner)

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	body, _ := io.ReadAll(func() io.Reader {
		rec := httptest.NewRecorder()
		m.HTTPHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return rec.Body
	}())

	if !strings.Contains(string(body), `method="GET"`) {
		t.Error("expected method label in metrics output")
	}
	if !strings.Contains(string(body), `status="204"`) {
		t.Error("expected status=204 label in metrics output")
	}
}

func TestHeartbeatCounter(t *testing.T) {
	m, _ := NewMetrics(func() int { return 0 })
	m.IncHeartbeat()
	m.IncHeartbeat()

	rec := httptest.NewRecorder()
	m.HTTPHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)

	if !strings.Contains(string(body), "puls_heartbeats_total 2") {
		t.Errorf("expected puls_heartbeats_total 2 in:\n%s", body)
	}
}

func TestWSMessagesRejectedCounter(t *testing.T) {
	m, _ := NewMetrics(func() int { return 0 })
	m.IncWSMessageRejected()
	m.IncWSMessageRejected()
	m.IncWSMessageRejected()

	rec := httptest.NewRecorder()
	m.HTTPHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)

	if !strings.Contains(string(body), "puls_ws_messages_rejected_total 3") {
		t.Errorf("expected puls_ws_messages_rejected_total 3 in:\n%s", body)
	}
}

// TestMiddlewareBucketsUnrecognizedMethodsAsOther guards against
// puls_http_requests_total growing an unbounded number of time series from a
// client varying the HTTP method on each request. Only GET and POST are ever
// routed, so anything else — a real-but-unused method like PUT, or an
// arbitrary token a client can freely choose — must collapse to a single
// "other" label value rather than being recorded verbatim.
func TestMiddlewareBucketsUnrecognizedMethodsAsOther(t *testing.T) {
	m, err := NewMetrics(func() int { return 0 })
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := m.Middleware(next)

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, "ZZZZZZ"} {
		req := httptest.NewRequest(method, "/whatever", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	metricsRec := httptest.NewRecorder()
	m.HTTPHandler().ServeHTTP(metricsRec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(metricsRec.Body)

	if !strings.Contains(string(body), `method="GET"`) {
		t.Errorf("expected a method=\"GET\" sample, got:\n%s", body)
	}
	if !strings.Contains(string(body), `method="POST"`) {
		t.Errorf("expected a method=\"POST\" sample, got:\n%s", body)
	}
	if !strings.Contains(string(body), `method="other"`) {
		t.Errorf("expected unrecognized methods bucketed as method=\"other\", got:\n%s", body)
	}
	if strings.Contains(string(body), `method="PUT"`) {
		t.Errorf("PUT should be bucketed as \"other\", not recorded as its own label value:\n%s", body)
	}
	if strings.Contains(string(body), `method="ZZZZZZ"`) {
		t.Errorf("an arbitrary client-chosen method should be bucketed as \"other\", not recorded verbatim:\n%s", body)
	}
}
