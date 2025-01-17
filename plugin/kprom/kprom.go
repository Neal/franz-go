// Package kprom provides prometheus plug-in metrics for a kgo client.
//
// This package tracks the following metrics under the following names,
// all metrics being counter vecs:
//
//     #{ns}_connects_total{node_id="#{node}"}
//     #{ns}_connect_errors_total{node_id="#{node}"}
//     #{ns}_write_errors_total{node_id="#{node}"}
//     #{ns}_write_bytes_total{node_id="#{node}"}
//     #{ns}_read_errors_total{node_id="#{node}"}
//     #{ns}_read_bytes_total{node_id="#{node}"}
//     #{ns}_produce_bytes_total{node_id="#{node}",topic="#{topic}"}
//     #{ns}_fetch_bytes_total{node_id="#{node}",topic="#{topic}"}
//
// This can be used in a client like so:
//
//     m := kprom.NewMetrics()
//     cl, err := kgo.NewClient(
//             kgo.WithHooks(m),
//             // ...other opts
//     )
//
// By default, metrics are installed under the a new prometheus registry, but
// this can be overridden with the Registry option.
//
// Note that seed brokers use broker IDs starting at math.MinInt32.
package kprom

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/twmb/franz-go/pkg/kgo"
)

var ( // interface checks to ensure we implement the hooks properly
	_ kgo.HookBrokerConnect       = new(Metrics)
	_ kgo.HookBrokerDisconnect    = new(Metrics)
	_ kgo.HookBrokerWrite         = new(Metrics)
	_ kgo.HookBrokerRead          = new(Metrics)
	_ kgo.HookProduceBatchWritten = new(Metrics)
	_ kgo.HookFetchBatchRead      = new(Metrics)
)

// Metrics provides prometheus metrics to a given registry.
type Metrics struct {
	cfg cfg

	connects    *prometheus.CounterVec
	connectErrs *prometheus.CounterVec
	disconnects *prometheus.CounterVec

	writeErrs  *prometheus.CounterVec
	writeBytes *prometheus.CounterVec

	readErrs  *prometheus.CounterVec
	readBytes *prometheus.CounterVec

	produceBytes *prometheus.CounterVec
	fetchBytes   *prometheus.CounterVec
}

// Registry returns the prometheus registry that metrics were added to.
//
// This is useful if you want the Metrics type to create its own registry for
// you to add additional metrics to.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.cfg.reg
}

// Handler returns an http.Handler providing prometheus metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.cfg.reg, m.cfg.handlerOpts)
}

type cfg struct {
	reg *prometheus.Registry

	handlerOpts  promhttp.HandlerOpts
	goCollectors bool
}

// Opt applies options to further tune how prometheus metrics are gathered or
// which metrics to use.
type Opt interface {
	apply(*cfg)
}

type opt struct{ fn func(*cfg) }

func (o opt) apply(c *cfg) { o.fn(c) }

// Registry sets the registry to add metrics to, rather than a new registry.
func Registry(reg *prometheus.Registry) Opt {
	return opt{func(c *cfg) { c.reg = reg }}
}

// GoCollectors adds the prometheus.NewProcessCollector and
// prometheus.NewGoCollector collectors the the Metric's registry.
func GoCollectors() Opt {
	return opt{func(c *cfg) { c.goCollectors = true }}
}

// HandlerOpts sets handler options to use if you wish you use the
// Metrics.Handler function.
//
// This is only useful if you both (a) do not want to provide your own registry
// and (b) want to override the default handler options.
func HandlerOpts(opts promhttp.HandlerOpts) Opt {
	return opt{func(c *cfg) { c.handlerOpts = opts }}
}

// NewMetrics returns a new Metrics that adds prometheus metrics to the
// registry under the given namespace.
func NewMetrics(namespace string, opts ...Opt) *Metrics {
	cfg := cfg{
		reg: prometheus.NewRegistry(),
	}
	for _, opt := range opts {
		opt.apply(&cfg)
	}

	if cfg.goCollectors {
		cfg.reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
		cfg.reg.MustRegister(prometheus.NewGoCollector())
	}

	factory := promauto.With(cfg.reg)

	return &Metrics{
		cfg: cfg,

		// connects and disconnects

		connects: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "connects_total",
			Help:      "Total number of connections opened, by broker",
		}, []string{"node_id"}),

		connectErrs: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "connect_errors_total",
			Help:      "Total number of connection errors, by broker",
		}, []string{"node_id"}),

		disconnects: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "disconnects_total",
			Help:      "Total number of connections closed, by broker",
		}, []string{"node_id"}),

		// write

		writeErrs: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "write_errors_total",
			Help:      "Total number of write errors, by broker",
		}, []string{"node_id"}),

		writeBytes: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "write_bytes_total",
			Help:      "Total number of bytes written, by broker",
		}, []string{"node_id"}),

		// read

		readErrs: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "read_errors_total",
			Help:      "Total number of read errors, by broker",
		}, []string{"node_id"}),

		readBytes: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "read_bytes_total",
			Help:      "Total number of bytes read, by broker",
		}, []string{"node_id"}),

		// produce & consume

		produceBytes: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "produce_bytes_total",
			Help:      "Total number of uncompressed bytes produced, by broker and topic",
		}, []string{"node_id", "topic"}),

		fetchBytes: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "fetch_bytes_total",
			Help:      "Total number of uncompressed bytes fetched, by broker and topic",
		}, []string{"node_id", "topic"}),
	}
}

func (m *Metrics) OnBrokerConnect(meta kgo.BrokerMetadata, _ time.Duration, _ net.Conn, err error) {
	node := strconv.Itoa(int(meta.NodeID))
	if err != nil {
		m.connectErrs.WithLabelValues(node).Inc()
		return
	}
	m.connects.WithLabelValues(node).Inc()
}

func (m *Metrics) OnBrokerDisconnect(meta kgo.BrokerMetadata, _ net.Conn) {
	node := strconv.Itoa(int(meta.NodeID))
	m.disconnects.WithLabelValues(node).Inc()
}

func (m *Metrics) OnBrokerWrite(meta kgo.BrokerMetadata, _ int16, bytesWritten int, _, _ time.Duration, err error) {
	node := strconv.Itoa(int(meta.NodeID))
	if err != nil {
		m.writeErrs.WithLabelValues(node).Inc()
		return
	}
	m.writeBytes.WithLabelValues(node).Add(float64(bytesWritten))
}

func (m *Metrics) OnBrokerRead(meta kgo.BrokerMetadata, _ int16, bytesRead int, _, _ time.Duration, err error) {
	node := strconv.Itoa(int(meta.NodeID))
	if err != nil {
		m.readErrs.WithLabelValues(node).Inc()
		return
	}
	m.readBytes.WithLabelValues(node).Add(float64(bytesRead))
}

func (m *Metrics) OnProduceBatchWritten(meta kgo.BrokerMetadata, topic string, _ int32, pbm kgo.ProduceBatchMetrics) {
	node := strconv.Itoa(int(meta.NodeID))
	m.produceBytes.WithLabelValues(node, topic).Add(float64(pbm.UncompressedBytes))
}

func (m *Metrics) OnFetchBatchRead(meta kgo.BrokerMetadata, topic string, _ int32, fbm kgo.FetchBatchMetrics) {
	node := strconv.Itoa(int(meta.NodeID))
	m.fetchBytes.WithLabelValues(node, topic).Add(float64(fbm.UncompressedBytes))
}
