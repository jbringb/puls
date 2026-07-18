package observability

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the custom Prometheus registry and all application collectors.
type Metrics struct {
	reg                     *prometheus.Registry
	httpRequestsTotal       *prometheus.CounterVec
	httpRequestDuration     *prometheus.HistogramVec
	HeartbeatsTotal         prometheus.Counter
	WSMessagesRejectedTotal prometheus.Counter
}

// NewMetrics creates a fresh isolated Prometheus registry and registers the
// standard set of Puls collectors. connectedDevices is called on each scrape
// to read the live WebSocket connection count from the hub without importing it.
func NewMetrics(connectedDevices func() int) (*Metrics, error) {
	reg := prometheus.NewRegistry()

	httpReqTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "puls_http_requests_total",
		Help: "Total HTTP requests partitioned by method, route, and status.",
	}, []string{"method", "route", "status"})

	httpReqDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "puls_http_request_duration_seconds",
		Help:    "HTTP request latency in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})

	heartbeatsTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "puls_heartbeats_total",
		Help: "Total heartbeat messages received from devices.",
	})

	wsMessagesRejectedTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "puls_ws_messages_rejected_total",
		Help: "Total WebSocket messages rejected because a device exceeded its per-device rate limit.",
	})

	devicesConnected := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "puls_devices_connected",
		Help: "Current number of devices with an active WebSocket connection.",
	}, func() float64 { return float64(connectedDevices()) })

	for _, c := range []prometheus.Collector{
		httpReqTotal, httpReqDuration, heartbeatsTotal, devicesConnected, wsMessagesRejectedTotal,
	} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("observability: register metric: %w", err)
		}
	}

	return &Metrics{
		reg:                     reg,
		httpRequestsTotal:       httpReqTotal,
		httpRequestDuration:     httpReqDuration,
		HeartbeatsTotal:         heartbeatsTotal,
		WSMessagesRejectedTotal: wsMessagesRejectedTotal,
	}, nil
}

// IncHeartbeat increments puls_heartbeats_total.
func (m *Metrics) IncHeartbeat() { m.HeartbeatsTotal.Inc() }

// IncWSMessageRejected increments puls_ws_messages_rejected_total.
func (m *Metrics) IncWSMessageRejected() { m.WSMessagesRejectedTotal.Inc() }

// HTTPHandler returns a Prometheus scrape handler scoped to this registry.
func (m *Metrics) HTTPHandler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}

// Middleware records puls_http_requests_total and puls_http_request_duration_seconds.
// next must eventually reach the ServeMux so r.Pattern is populated (set by
// the ServeMux on the request pointer during its own dispatch, before this
// middleware's deferred read runs) — but layers between this and the mux are
// fine as long as they don't swallow r or fork it. It must also sit outside
// any panic-recovery layer: recovery needs to catch a panic and write the
// real status code before this middleware's deferred read sees it, or the
// read observes whatever zero-value status was captured before the panic
// unwound past it.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		cw := &captureWriter{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			m.httpRequestsTotal.With(prometheus.Labels{
				"method": r.Method,
				"route":  route,
				"status": strconv.Itoa(cw.status),
			}).Inc()
			m.httpRequestDuration.With(prometheus.Labels{
				"method": r.Method,
				"route":  route,
			}).Observe(time.Since(start).Seconds())
		}()

		next.ServeHTTP(cw, r)
	})
}

type captureWriter struct {
	http.ResponseWriter
	status int
}

func (w *captureWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *captureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
