package marketplace_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/mock"
)

const validPostJSON = `{"platform":"mock","external_post_id":"post-1","buyer_id":"buyer-1","thread_id":"thread-1","text":"  Need a laptop  ","created_at":"2026-11-07T15:00:00+07:00","location":{"city":"Jakarta"}}`

func TestMockPostHTTP(t *testing.T) {
	store, err := mock.NewPostStore()
	if err != nil {
		t.Fatal(err)
	}
	handler := marketplace.NewPostHandler(store)

	t.Run("create and replay preserve canonical post", func(t *testing.T) {
		first := callPost(handler, "create-1", validPostJSON)
		if first.Code != http.StatusCreated {
			t.Fatalf("first status = %d: %s", first.Code, first.Body)
		}
		replayed := callPost(handler, "create-1", strings.Replace(validPostJSON, "15:00:00+07:00", "08:00:00Z", 1))
		if replayed.Code != first.Code || replayed.Body.String() != first.Body.String() {
			t.Fatalf("replay = %d %s, want %d %s", replayed.Code, replayed.Body, first.Code, first.Body)
		}
		var response struct {
			ExternalPostID string `json:"external_post_id"`
			CreatedAt      string `json:"created_at"`
			Text           string `json:"text"`
		}
		if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.ExternalPostID != "post-1" || response.CreatedAt != "2026-11-07T08:00:00Z" || response.Text != "  Need a laptop  " {
			t.Fatalf("unexpected response: %+v", response)
		}
		got, err := store.GetPost(context.Background(), domain.PostRef{Platform: "mock", ExternalPostID: "post-1"})
		if err != nil || got.BuyerID != "buyer-1" || got.ThreadID != "thread-1" || got.Location.City != "Jakarta" {
			t.Fatalf("stored post = %+v, %v", got, err)
		}
		changed := strings.Replace(validPostJSON, "Need a laptop", "Need a phone", 1)
		assertHTTPError(t, callPost(handler, "create-1", changed), http.StatusConflict, contracts.IdempotencyConflict)
	})

	t.Run("rejects malformed requests", func(t *testing.T) {
		assertHTTPError(t, callPost(handler, "", validPostJSON), http.StatusBadRequest, contracts.InvalidArgument)
		assertHTTPError(t, callPost(handler, "bad", validPostJSON+`{}`), http.StatusBadRequest, contracts.InvalidArgument)
		assertHTTPError(t, callPost(handler, "bad", strings.Replace(validPostJSON, "buyer_id", "unknown_field", 1)), http.StatusBadRequest, contracts.InvalidArgument)
		assertHTTPError(t, callPost(handler, "bad", strings.Repeat(" ", 70<<10)), http.StatusRequestEntityTooLarge, contracts.InvalidArgument)
	})

	t.Run("other methods and paths", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mock/posts", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET status = %d", rec.Code)
		}
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/elsewhere", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("unmatched route status = %d", rec.Code)
		}
	})

	t.Run("connector failure remains distinct and hides internal cause", func(t *testing.T) {
		down := marketplace.NewPostHandler(unavailablePostCreator{})
		rec := callPost(down, "create-1", validPostJSON)
		assertHTTPError(t, rec, http.StatusServiceUnavailable, contracts.DependencyUnavailable)
		if strings.Contains(rec.Body.String(), "private connection detail") {
			t.Fatalf("internal cause exposed in %q", rec.Body)
		}
	})
}

type unavailablePostCreator struct{}

func (unavailablePostCreator) CreatePost(context.Context, domain.Post, string) (domain.Post, error) {
	return domain.Post{}, &contracts.Error{
		Code: contracts.DependencyUnavailable, Message: "Post store unavailable",
		Retryable: true, Cause: errors.New("private connection detail"),
	}
}

func callPost(handler http.Handler, key, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/mock/posts", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertHTTPError(t *testing.T, response *httptest.ResponseRecorder, status int, code contracts.Code) {
	t.Helper()
	var payload struct {
		Code contracts.Code `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if response.Code != status || payload.Code != code {
		t.Fatalf("response = %d %s, want %d %s", response.Code, response.Body, status, code)
	}
}
