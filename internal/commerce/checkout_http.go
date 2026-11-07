package commerce

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
)

// CheckoutHandler exposes only opaque-token mock checkout APIs. P3 owns the
// /checkout/{token} page; a token holder needs no seller-context header.
func (s *Service) CheckoutHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/checkouts/{token}", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.GetCheckout(r.Context(), r.PathValue("token"))
		checkoutRespond(w, out, err)
	})
	mux.HandleFunc("POST /api/checkouts/{token}/pay", func(w http.ResponseWriter, r *http.Request) {
		if !checkoutEmptyBody(w, r) {
			return
		}
		out, err := s.PayCheckout(r.Context(), r.PathValue("token"), r.Header.Get("Idempotency-Key"))
		checkoutRespond(w, out, err)
	})
	return mux
}

func checkoutEmptyBody(w http.ResponseWriter, r *http.Request) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	var body map[string]json.RawMessage
	err := decoder.Decode(&body)
	if err == io.EOF {
		return true
	}
	if err != nil || body == nil || len(body) != 0 {
		checkoutWriteError(w, &contracts.Error{Code: contracts.InvalidArgument, Message: "payment accepts an empty JSON object only"})
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		checkoutWriteError(w, &contracts.Error{Code: contracts.InvalidArgument, Message: "exactly one JSON object is required"})
		return false
	}
	return true
}

func checkoutRespond(w http.ResponseWriter, data any, err error) {
	if err != nil {
		checkoutWriteError(w, err)
		return
	}
	checkoutWriteJSON(w, http.StatusOK, map[string]any{"data": data})
}
func checkoutWriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func checkoutWriteError(w http.ResponseWriter, err error) {
	code, message, retryable := contracts.DependencyUnavailable, "checkout service unavailable", true
	status := http.StatusServiceUnavailable
	var business *contracts.Error
	if errors.As(err, &business) {
		code, message, retryable = business.Code, business.Message, business.Retryable
		switch code {
		case contracts.InvalidArgument:
			status = http.StatusBadRequest
		case contracts.NotFound:
			status = http.StatusNotFound
		case contracts.InvalidState, contracts.OutOfStock, contracts.PriceBelowMinimum, contracts.OfferSuperseded, contracts.CheckoutExpired, contracts.VersionConflict, contracts.IdempotencyConflict:
			status = http.StatusConflict
		}
	}
	checkoutWriteJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "retryable": retryable}})
}
