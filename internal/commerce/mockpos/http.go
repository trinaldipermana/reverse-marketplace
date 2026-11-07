package mockpos

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
)

// Handler exposes product routes only. The shared app can mount it under /api/;
// it must coordinate route registration with other owners. X-Seller-ID is a
// local-demo context selector, not production authentication.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/inventory", func(w http.ResponseWriter, r *http.Request) {
		limit := 20
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil {
				writeError(w, problem(contracts.InvalidArgument, "invalid limit"))
				return
			}
		}
		out, err := s.List(r.Context(), r.Header.Get("X-Seller-ID"), r.URL.Query().Get("cursor"), limit)
		respond(w, http.StatusOK, out, err)
	})
	mux.HandleFunc("GET /api/inventory/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Get(r.Context(), r.Header.Get("X-Seller-ID"), r.PathValue("id"))
		respond(w, http.StatusOK, out, err)
	})
	mux.HandleFunc("POST /api/inventory", func(w http.ResponseWriter, r *http.Request) {
		var in CreateItem
		if !decode(w, r, &in) {
			return
		}
		out, err := s.Create(r.Context(), r.Header.Get("X-Seller-ID"), r.Header.Get("Idempotency-Key"), in)
		respond(w, http.StatusCreated, out, err)
	})
	mux.HandleFunc("PATCH /api/inventory/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in PatchItem
		if !decode(w, r, &in) {
			return
		}
		out, err := s.Patch(r.Context(), r.Header.Get("X-Seller-ID"), r.PathValue("id"), r.Header.Get("Idempotency-Key"), in)
		respond(w, http.StatusOK, out, err)
	})
	mux.HandleFunc("POST /api/inventory/{id}/stock-adjustments", func(w http.ResponseWriter, r *http.Request) {
		var in AdjustStock
		if !decode(w, r, &in) {
			return
		}
		out, err := s.Adjust(r.Context(), r.Header.Get("X-Seller-ID"), r.PathValue("id"), r.Header.Get("Idempotency-Key"), in)
		respond(w, http.StatusOK, out, err)
	})
	mux.HandleFunc("GET /api/seller/policy", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.GetPolicy(r.Context(), r.Header.Get("X-Seller-ID"))
		respond(w, http.StatusOK, out, err)
	})
	mux.HandleFunc("PATCH /api/seller/policy", func(w http.ResponseWriter, r *http.Request) {
		var in PatchPolicy
		if !decode(w, r, &in) {
			return
		}
		out, err := s.PatchPolicy(r.Context(), r.Header.Get("X-Seller-ID"), r.Header.Get("Idempotency-Key"), in)
		respond(w, http.StatusOK, out, err)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("X-Seller-ID")) == "" {
			writeError(w, problem(contracts.InvalidArgument, "X-Seller-ID is required"))
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		writeError(w, problem(contracts.InvalidArgument, "invalid JSON request"))
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeError(w, problem(contracts.InvalidArgument, "exactly one JSON object is required"))
		return false
	}
	return true
}
func respond(w http.ResponseWriter, status int, data any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, status, map[string]any{"data": data})
}
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func writeError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	code := contracts.DependencyUnavailable
	message := "inventory service unavailable"
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
		case contracts.InvalidState, contracts.VersionConflict, contracts.IdempotencyConflict:
			status = 409
		default:
			status = 503
		}
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "retryable": retryable}})
}
