package demo_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/demo"
)

func TestDemoResetRequiresExplicitLocalAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name      string
		enabled   bool
		token     string
		remote    string
		want      int
		wantCalls int
	}{
		{name: "disabled", remote: "127.0.0.1:4000", token: "demo-secret", want: http.StatusNotFound},
		{name: "remote", enabled: true, remote: "192.0.2.1:4000", token: "demo-secret", want: http.StatusForbidden},
		{name: "unauthenticated", enabled: true, remote: "127.0.0.1:4000", want: http.StatusUnauthorized},
		{name: "wrong token", enabled: true, remote: "127.0.0.1:4000", token: "other", want: http.StatusUnauthorized},
		{name: "loopback", enabled: true, remote: "127.0.0.1:4000", token: "demo-secret", want: http.StatusNoContent, wantCalls: 1},
		{name: "IPv6 loopback", enabled: true, remote: "[::1]:4000", token: "demo-secret", want: http.StatusNoContent, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			calls := 0
			err := demo.RegisterReset(mux, demo.ResetConfig{
				Enabled: tc.enabled,
				Token:   "demo-secret",
				Reset: func(context.Context) error {
					calls++
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/demo/reset", nil)
			request.RemoteAddr = tc.remote
			request.Header.Set("X-Forwarded-For", "127.0.0.1")
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != tc.want || calls != tc.wantCalls {
				t.Fatalf("reset status = %d, backend calls = %d, want %d and %d", response.Code, calls, tc.want, tc.wantCalls)
			}
		})
	}
}

func TestDemoResetRefusesIncompleteConfiguration(t *testing.T) {
	for _, config := range []demo.ResetConfig{
		{Enabled: true, Reset: func(context.Context) error { return nil }},
		{Enabled: true, Token: "demo-secret"},
	} {
		mux := http.NewServeMux()
		if err := demo.RegisterReset(mux, config); err == nil {
			t.Fatal("registered reset without token or backend")
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/demo/reset", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("incomplete reset route returned %d, want 404", response.Code)
		}
	}
}

func TestDemoResetSurfacesFailedQuiesceOrSeed(t *testing.T) {
	mux := http.NewServeMux()
	err := demo.RegisterReset(mux, demo.ResetConfig{
		Enabled: true, Token: "demo-secret",
		Reset: func(context.Context) error { return errors.New("internal failure details") },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/demo/reset", nil)
	request.RemoteAddr = "127.0.0.1:4000"
	request.Header.Set("Authorization", "Bearer demo-secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "demo reset failed\n" {
		t.Fatalf("reset returned %d %q, want generic 503", response.Code, response.Body.String())
	}
}
