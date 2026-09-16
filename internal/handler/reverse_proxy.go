package handler

import (
	"log/slog"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
)

// ReverseProxy reverse-proxies requests under BasePath+prefix to an in-cluster
// address, stripping prefix first — the Go-code equivalent of the mesh's
// `rewrite: uri: /`. This lets a direct pod port-forward (which bypasses
// Istio entirely) reach a backend that isn't otherwise browser-reachable,
// through the same host:port as orbital itself. Used for both Ratel's UI
// (prefix "/dgraph") and DGraph Alpha's query API (prefix "/dgraph-alpha").
type ReverseProxy struct {
	prefix string
	target *url.URL
	proxy  *httputil.ReverseProxy
	logger *slog.Logger
}

// NewReverseProxy builds a ReverseProxy forwarding requests under prefix to
// internalURL.
func NewReverseProxy(prefix, internalURL string, logger *slog.Logger) (*ReverseProxy, error) {
	target, err := url.Parse(internalURL)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelError)
	return &ReverseProxy{prefix: prefix, target: target, proxy: proxy, logger: logger}, nil
}

// Handle strips the configured prefix from the request path and forwards
// everything else (method, headers, body) to the target as-is.
func (h *ReverseProxy) Handle(c echo.Context) error {
	req := c.Request()
	path := req.URL.Path
	if i := strings.Index(path, h.prefix); i >= 0 {
		req.URL.Path = path[i+len(h.prefix):]
	}
	if req.URL.Path == "" {
		req.URL.Path = "/"
	}
	h.proxy.ServeHTTP(c.Response(), req)
	return nil
}
