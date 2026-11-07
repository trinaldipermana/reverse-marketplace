package discovery

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
)

// Handler serves the existing frontend route synchronously. Seller selection is
// demo context in the body, not production authentication. No polling is needed.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/matches/search", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil || !utf8.Valid(body) {
			discoveryHTTPError(w, discoveryError(contracts.InvalidArgument, "invalid discovery request"))
			return
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(body, &fields); err != nil || len(fields) != 1 {
			discoveryHTTPError(w, discoveryError(contracts.InvalidArgument, "request must contain sellerId only"))
			return
		}
		raw, ok := fields["sellerId"]
		var seller string
		if !ok || json.Unmarshal(raw, &seller) != nil || seller == "" || strings.TrimSpace(seller) != seller || len(seller) > 200 {
			discoveryHTTPError(w, discoveryError(contracts.InvalidArgument, "sellerId is required"))
			return
		}
		out, err := s.Discover(r.Context(), seller)
		if err != nil {
			discoveryHTTPError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(out)
	})
	return mux
}
func discoveryHTTPError(w http.ResponseWriter, err error) {
	status := 503
	code := contracts.DependencyUnavailable
	message := "discovery service unavailable"
	retryable := true
	var failure *contracts.Error
	if errors.As(err, &failure) {
		code = failure.Code
		message = failure.Message
		retryable = failure.Retryable
		switch code {
		case contracts.InvalidArgument:
			status = 400
		case contracts.NotFound:
			status = 404
		case contracts.InvalidState, contracts.IdempotencyConflict:
			status = 409
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "retryable": retryable}})
}
