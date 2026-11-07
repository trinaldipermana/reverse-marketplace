package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// PostCreator is the write dependency of the opt-in mock post route.
type PostCreator interface {
	CreatePost(ctx context.Context, post domain.Post, replayKey string) (domain.Post, error)
}

// NewPostHandler handles POST /api/mock/posts for a mock marketplace instance.
func NewPostHandler(posts PostCreator) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/mock/posts", func(w http.ResponseWriter, r *http.Request) {
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || strings.TrimSpace(keys[0]) == "" {
			writeMockError(w, http.StatusBadRequest, contracts.InvalidArgument, "One Idempotency-Key header is required")
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeMockError(w, http.StatusRequestEntityTooLarge, contracts.InvalidArgument, "Post body exceeds 64 KiB")
			return
		}
		if err != nil || !utf8.Valid(body) {
			writeMockError(w, http.StatusBadRequest, contracts.InvalidArgument, "Post body must be UTF-8 JSON")
			return
		}

		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		var request postJSON
		if err := decoder.Decode(&request); err != nil {
			writeMockError(w, http.StatusBadRequest, contracts.InvalidArgument, "Invalid post JSON")
			return
		}
		var extra json.RawMessage
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			writeMockError(w, http.StatusBadRequest, contracts.InvalidArgument, "Expected a single post object")
			return
		}

		created, err := posts.CreatePost(r.Context(), request.post(), keys[0])
		if err != nil {
			writePostCreateFailure(w, err)
			return
		}
		writeMockJSON(w, http.StatusCreated, postResponse(created))
	})
	return mux
}

type postJSON struct {
	Platform       string            `json:"platform"`
	ExternalPostID string            `json:"external_post_id"`
	BuyerID        string            `json:"buyer_id"`
	ThreadID       string            `json:"thread_id"`
	Text           string            `json:"text"`
	CreatedAt      time.Time         `json:"created_at"`
	Location       *mockLocationJSON `json:"location,omitempty"`
}

type mockLocationJSON struct {
	City      string   `json:"city,omitempty"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

func (p postJSON) post() domain.Post {
	post := domain.Post{
		PostRef: domain.PostRef{Platform: p.Platform, ExternalPostID: p.ExternalPostID},
		BuyerID: p.BuyerID, ThreadID: p.ThreadID, Text: p.Text, CreatedAt: p.CreatedAt,
	}
	if p.Location != nil {
		post.Location = domain.Location(*p.Location)
	}
	return post
}

func postResponse(post domain.Post) postJSON {
	response := postJSON{
		Platform: post.Platform, ExternalPostID: post.ExternalPostID,
		BuyerID: post.BuyerID, ThreadID: post.ThreadID, Text: post.Text, CreatedAt: post.CreatedAt.UTC(),
	}
	if post.Location != (domain.Location{}) {
		location := mockLocationJSON(post.Location)
		response.Location = &location
	}
	return response
}

func writePostCreateFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		writeMockError(w, http.StatusGatewayTimeout, contracts.DependencyUnavailable, "Post creation timed out")
		return
	}
	if errors.Is(err, context.Canceled) {
		writeMockError(w, http.StatusRequestTimeout, contracts.DependencyUnavailable, "Post creation canceled")
		return
	}
	var coded *contracts.Error
	if errors.As(err, &coded) {
		switch coded.Code {
		case contracts.InvalidArgument:
			writeMockError(w, http.StatusBadRequest, coded.Code, coded.Message)
			return
		case contracts.IdempotencyConflict:
			writeMockError(w, http.StatusConflict, coded.Code, coded.Message)
			return
		case contracts.DependencyUnavailable:
			writeMockError(w, http.StatusServiceUnavailable, coded.Code, "Post store unavailable")
			return
		}
	}
	writeMockError(w, http.StatusInternalServerError, contracts.DependencyUnavailable, "Post creation failed")
}

func writeMockError(w http.ResponseWriter, status int, code contracts.Code, message string) {
	writeMockJSON(w, status, struct {
		Code    contracts.Code `json:"code"`
		Message string         `json:"message"`
	}{Code: code, Message: message})
}

func writeMockJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
