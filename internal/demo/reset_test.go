package demo_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestBuyerResetRejectsNonOriginURLsBeforeSendingToken(t *testing.T) {
	for _, baseURL := range []string{
		"http://localhost:8080/intercept?foo=x",
		"http://localhost:8080/intercept",
		"http://localhost:8080/%2f",
		"http://localhost:8080/?foo=x",
		"http://localhost:8080/?",
		"http://localhost:8080/#ignored",
		"http://localhost:8080/#",
		"http://user@localhost:8080",
		"ftp://localhost:8080",
		"//localhost:8080",
		"http://outside.localhost:8765",
	} {
		t.Run(baseURL, func(t *testing.T) {
			client := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("invalid reset origin sent a request to %s", r.URL)
				w.WriteHeader(http.StatusNoContent)
			}))
			buyer := demo.Buyer{BaseURL: baseURL, Client: client}
			if err := buyer.Reset(context.Background(), "demo-secret"); err == nil {
				t.Fatal("invalid reset origin accepted")
			}
		})
	}
}

func TestBuyerResetAcceptsLoopbackHTTPOrigins(t *testing.T) {
	for _, baseURL := range []string{
		"http://localhost:8080",
		"http://127.0.0.1:8080/",
		"https://[::1]:8080/",
	} {
		t.Run(baseURL, func(t *testing.T) {
			requests := 0
			client := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodPost || r.URL.RequestURI() != "/api/demo/reset" {
					t.Errorf("reset request = %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer demo-secret" {
					t.Error("reset request missing operator authorization")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			buyer := demo.Buyer{BaseURL: baseURL, Client: client}
			if err := buyer.Reset(context.Background(), "demo-secret"); err != nil {
				t.Fatal(err)
			}
			if requests != 1 {
				t.Fatalf("reset made %d requests, want 1", requests)
			}
		})
	}
}

func TestBuyerResetRefusesRedirects(t *testing.T) {
	for _, name := range []string{"caller client", "default client"} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			client := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Host != "localhost:8080" || r.URL.Path != "/api/demo/reset" {
					t.Errorf("reset followed redirect to %s, bearer present: %t", r.URL, r.Header.Get("Authorization") != "")
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Location", "http://outside.localhost:8765/intercept")
				w.WriteHeader(http.StatusTemporaryRedirect)
			}))
			buyer := demo.Buyer{BaseURL: "http://localhost:8080", Client: client}
			if name == "default client" {
				original := http.DefaultClient
				http.DefaultClient = client
				t.Cleanup(func() { http.DefaultClient = original })
				buyer.Client = nil
			}

			err := buyer.Reset(context.Background(), "demo-secret")
			if err == nil || !strings.Contains(err.Error(), "307") || requests != 1 {
				t.Fatalf("redirecting reset error = %v, requests = %d, want 307 and one request", err, requests)
			}
		})
	}
}
