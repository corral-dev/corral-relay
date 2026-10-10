package metrics

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

var (
	HostsOnline    atomic.Int64
	Channels       atomic.Int64
	BytesForwarded atomic.Uint64
	AuthFailures   atomic.Uint64
)

func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "# HELP corral_relay_hosts_online 当前在线开发机\n# TYPE corral_relay_hosts_online gauge\ncorral_relay_hosts_online %d\n", HostsOnline.Load())
		_, _ = fmt.Fprintf(w, "# HELP corral_relay_channels 当前设备通道\n# TYPE corral_relay_channels gauge\ncorral_relay_channels %d\n", Channels.Load())
		_, _ = fmt.Fprintf(w, "# HELP corral_relay_bytes_forwarded_total 转发字节\n# TYPE corral_relay_bytes_forwarded_total counter\ncorral_relay_bytes_forwarded_total %d\n", BytesForwarded.Load())
		_, _ = fmt.Fprintf(w, "# HELP corral_relay_auth_failures_total 鉴权失败\n# TYPE corral_relay_auth_failures_total counter\ncorral_relay_auth_failures_total %d\n", AuthFailures.Load())
	})
}
