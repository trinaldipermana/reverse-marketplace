package demo_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/demo"
)

func TestBuyerPublishesStablePostRequest(t *testing.T) {
	type request struct {
		method, path, key, contentType, body string
	}
	var requests []request
	client := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		requests = append(requests, request{r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"), r.Header.Get("Content-Type"), string(body)})
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"external_post_id":"post-1"}`)
	}))

	at := time.Date(2026, time.November, 7, 12, 0, 0, 0, time.FixedZone("WIB", 7*60*60))
	buyer := demo.Buyer{BaseURL: "http://127.0.0.1:8080", Client: client}
	post := demo.Post{
		Platform: "mock", ExternalPostID: "post-1", BuyerID: "buyer-1", ThreadID: "thread-1",
		Text: "Need a used laptop for coding", Location: &demo.Location{City: "Jakarta"},
	}
	for range 2 {
		body, err := buyer.Publish(context.Background(), post, at)
		if err != nil || string(body) != `{"external_post_id":"post-1"}` {
			t.Fatalf("publish response = %s, %v", body, err)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("marketplace requests = %d, want 2", len(requests))
	}
	got, replay := requests[0], requests[1]
	if got != replay {
		t.Fatalf("retry changed marketplace request: %+v, %+v", got, replay)
	}
	if got.method != http.MethodPost || got.path != "/api/mock/posts" || got.key != "demo-post-post-1" || got.contentType != "application/json" {
		t.Fatalf("unexpected request metadata: %+v", got)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(got.body), &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"platform": "mock", "external_post_id": "post-1", "buyer_id": "buyer-1", "thread_id": "thread-1",
		"text": "Need a used laptop for coding", "created_at": "2026-11-07T05:00:00Z",
		"location": map[string]any{"city": "Jakarta"},
	}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("marketplace post = %#v, want %#v", payload, want)
	}
}

func TestBuyerReportsMarketplaceFailure(t *testing.T) {
	client := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "marketplace unavailable", http.StatusServiceUnavailable)
	}))

	buyer := demo.Buyer{BaseURL: "http://127.0.0.1:8080", Client: client}
	_, err := buyer.Thread(context.Background(), "thread-1")
	if err == nil || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "marketplace unavailable") {
		t.Fatalf("thread error = %v, want marketplace status and response", err)
	}
}

func TestBuyerRepliesUsingMarketplaceIdentity(t *testing.T) {
	for _, replyTo := range []string{"", "agent-message-1"} {
		t.Run("reply_to_"+replyTo, func(t *testing.T) {
			client := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/mock/threads/thread-1/replies" {
					t.Errorf("reply request = %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Idempotency-Key") != "" {
					t.Errorf("reply should use its external identity")
				}
				var got map[string]any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				want := map[string]any{"external_reply_id": "reply-1", "text": "Rp5,4 juta bisa?"}
				if replyTo != "" {
					want["reply_to_message_id"] = replyTo
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("reply = %#v, want %#v", got, want)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"message":{"id":"message-1"},"event_id":"event-1"}`)
			}))

			buyer := demo.Buyer{BaseURL: "http://127.0.0.1:8080", Client: client}
			body, err := buyer.Reply(context.Background(), "thread-1", demo.Reply{ExternalReplyID: "reply-1", Text: "Rp5,4 juta bisa?"}, replyTo)
			if err != nil || !strings.Contains(string(body), `"event_id":"event-1"`) {
				t.Fatalf("reply response = %s, %v", body, err)
			}
		})
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func handlerClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response.Result(), nil
	})}
}
