package server

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Desktop LAN sharing.
//
// A peer counts as "local network" when it is a private or link-local address
// (IPv4 RFC 1918, 169.254/16, IPv6 fc00::/7, fe80::/10) or lies inside a subnet
// assigned to one of this computer's active, non point-to-point interfaces.
// The subnet rule covers IPv6 global addresses from the same /64 (clients that
// reach the gateway through a Bonjour/.local name often pick IPv6) and LANs
// that use non-RFC 1918 IPv4 ranges. Tunnel interfaces (proxy TUN, VPN) are
// point-to-point and therefore never widen the accepted range.
//
// Only the inference routes are reachable from the LAN; every route still
// enforces the inference API key. A small in-memory log of recent LAN requests
// (peer, route, status) is kept so the owner can see on this computer whether
// another device's calls arrive at all and why they fail. The log is readable
// only from loopback with the API key.

const lanAccessLogSize = 30

// LANAccessEntry is one recorded request from a non-loopback peer.
type LANAccessEntry struct {
	Time   time.Time `json:"time"`
	Peer   string    `json:"peer"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Status int       `json:"status"`
	Result string    `json:"result"` // ok, unauthorized, failed, blocked_path, blocked_source
}

type lanAccessLog struct {
	mu      sync.Mutex
	entries []LANAccessEntry // oldest first, at most lanAccessLogSize
	totals  map[string]int
}

func (l *lanAccessLog) record(e LANAccessEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.totals == nil {
		l.totals = map[string]int{}
	}
	l.totals[e.Result]++
	if len(l.entries) >= lanAccessLogSize {
		copy(l.entries, l.entries[1:])
		l.entries = l.entries[:len(l.entries)-1]
	}
	l.entries = append(l.entries, e)
}

func (l *lanAccessLog) snapshot() ([]LANAccessEntry, map[string]int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entries := make([]LANAccessEntry, 0, len(l.entries))
	for i := len(l.entries) - 1; i >= 0; i-- { // newest first
		entries = append(entries, l.entries[i])
	}
	totals := map[string]int{"ok": 0, "unauthorized": 0, "failed": 0, "blocked_path": 0, "blocked_source": 0}
	for k, v := range l.totals {
		totals[k] = v
	}
	return entries, totals
}

// lanRouteOpen lists the only routes reachable from another device.
func lanRouteOpen(r *http.Request) bool {
	switch r.Method + " " + r.URL.Path {
	case "GET /v1/models", "HEAD /v1/models", "POST /v1/chat/completions", "POST /v1/responses", "POST /v1/messages":
		return true
	}
	return false
}

// lanPeerAllowed reports whether a non-loopback peer belongs to the local network.
func lanPeerAllowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	return onLocalSubnet(ip)
}

// localSubnetPrefixes is replaceable in tests.
var localSubnetPrefixes = interfaceSubnetPrefixes

var localSubnetCache struct {
	sync.Mutex
	at       time.Time
	prefixes []netip.Prefix
}

func onLocalSubnet(ip netip.Addr) bool {
	localSubnetCache.Lock()
	if time.Since(localSubnetCache.at) > 10*time.Second {
		localSubnetCache.prefixes = localSubnetPrefixes()
		localSubnetCache.at = time.Now()
	}
	prefixes := localSubnetCache.prefixes
	localSubnetCache.Unlock()
	addr := ip.WithZone("")
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// interfaceSubnetPrefixes returns the subnets of active broadcast-style
// interfaces. Loopback and point-to-point (TUN/VPN) interfaces are skipped, and
// overly broad prefixes are ignored so a misconfigured mask cannot open the
// gateway to the internet.
func interfaceSubnetPrefixes() []netip.Prefix {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ones, bits := ipn.Mask.Size()
			if addr.Is4In6() && bits == 128 {
				ones -= 96
			}
			addr = addr.Unmap()
			if ones < 0 || addr.Is4() && ones < 16 || addr.Is6() && ones < 48 {
				continue
			}
			if p, err := addr.Prefix(ones); err == nil {
				out = append(out, p)
			}
		}
	}
	return out
}

// lanStatusWriter captures the response status for the LAN log while keeping
// streaming (Flush) working.
type lanStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *lanStatusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *lanStatusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *lanStatusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if w.status == 0 {
			w.status = http.StatusOK
		}
		f.Flush()
	}
}

func (w *lanStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func lanResult(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "ok"
	case status == http.StatusUnauthorized:
		return "unauthorized"
	default:
		return "failed"
	}
}

// lanLogPath keeps the recorded path short and printable.
func lanLogPath(p string) string {
	if len(p) > 96 {
		p = p[:96] + "…"
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, p)
}

func (h *Handler) recordLAN(peer netip.Addr, r *http.Request, status int, result string) {
	h.lanLog.record(LANAccessEntry{
		Time:   time.Now().UTC(),
		Peer:   peer.Unmap().String(),
		Method: lanLogPath(r.Method),
		Path:   lanLogPath(r.URL.Path),
		Status: status,
		Result: result,
	})
}

// lanAccess serves the recent LAN request log (loopback only, API key required).
func (h *Handler) lanAccess(w http.ResponseWriter, r *http.Request) {
	entries, totals := h.lanLog.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"lan_enabled": h.cfg.DesktopLAN,
		"entries":     entries,
		"totals":      totals,
	})
}
