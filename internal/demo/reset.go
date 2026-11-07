package demo

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

const resetPath = "/api/demo/reset"

type ResetConfig struct {
	Enabled bool
	Token   string

	// Reset must quiesce or fence existing work before replacing demo state
	// and reseeding it atomically. It owns synchronization with every writer
	// and returns success only when a new run can safely start.
	Reset func(context.Context) error
}

// RegisterReset installs the route only for a deliberately configured demo
// on a loopback-bound listener. It uses the direct peer address, so callers
// must keep the route off reverse proxies and public listeners.
func RegisterReset(mux *http.ServeMux, config ResetConfig) error {
	if !config.Enabled {
		return nil
	}
	if config.Reset == nil || config.Token == "" || strings.ContainsAny(config.Token, " \t\r\n") {
		return errors.New("demo reset requires a backend and a nonempty token without whitespace")
	}
	want := sha256.Sum256([]byte("Bearer " + config.Token))
	mux.HandleFunc("POST "+resetPath, func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || !isLoopbackIP(host) {
			http.Error(w, "demo reset requires a loopback connection", http.StatusForbidden)
			return
		}
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "demo reset requires authorization", http.StatusUnauthorized)
			return
		}
		if err := config.Reset(r.Context()); err != nil {
			http.Error(w, "demo reset failed", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return nil
}

// Reset requests a fresh local demo run using the operator token.
func (b Buyer) Reset(ctx context.Context, token string) error {
	base, err := url.Parse(b.BaseURL)
	if err != nil || (base.Hostname() != "localhost" && !isLoopbackIP(base.Hostname())) {
		return errors.New("demo reset requires a loopback base URL")
	}
	if token == "" {
		return errors.New("demo reset requires an operator token")
	}
	_, err = b.request(ctx, http.MethodPost, resetPath, nil, "", token, http.StatusNoContent)
	return err
}

func isLoopbackIP(host string) bool {
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}
