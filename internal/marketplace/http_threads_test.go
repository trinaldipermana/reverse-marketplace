package marketplace_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/mock"
)

func TestMockThreadHTTP(t *testing.T) {
	post := domain.Post{
		PostRef: domain.PostRef{Platform: "mock", ExternalPostID: "post-1"},
		BuyerID: "buyer-1", ThreadID: "thread-1",
	}
	threads := mock.NewThreads(threadPostLookup{post}, func() time.Time {
		return time.Date(2026, 11, 7, 4, 0, 0, 0, time.UTC)
	})
	if err := threads.OpenThread(context.Background(), post.PostRef); err != nil {
		t.Fatal(err)
	}
	handler := marketplace.NewThreadHandler(threads, "mock")
	const path = "/api/mock/threads/thread-1"
	const input = `{"external_reply_id":"reply-1","text":"Can you send a photo?","location":{"city":"Jakarta"}}`

	read := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, httptest.NewRequest(method, target, strings.NewReader(body)))
		return resp
	}

	initial := read(http.MethodGet, path, "")
	if initial.Code != http.StatusOK || strings.TrimSpace(initial.Body.String()) != `{"messages":[]}` {
		t.Fatalf("empty history = %d %s", initial.Code, initial.Body.String())
	}
	first := read(http.MethodPost, path+"/replies", input)
	replay := read(http.MethodPost, path+"/replies", input)
	if first.Code != http.StatusCreated || replay.Code != http.StatusOK || first.Body.String() != replay.Body.String() {
		t.Fatalf("first = %d %s; replay = %d %s", first.Code, first.Body.String(), replay.Code, replay.Body.String())
	}
	var result struct {
		EventID string         `json:"event_id"`
		Message map[string]any `json:"message"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"platform": "mock", "external_post_id": "post-1", "external_reply_id": "reply-1",
		"buyer_id": "buyer-1", "thread_id": "thread-1", "direction": "buyer",
		"text": "Can you send a photo?", "created_at": "2026-11-07T04:00:00Z",
	}
	for key, value := range want {
		if result.Message[key] != value {
			t.Errorf("message[%q] = %v, want %v", key, result.Message[key], value)
		}
	}
	location, ok := result.Message["location"].(map[string]any)
	if !ok || location["city"] != "Jakarta" || result.EventID == "" || result.Message["id"] == "" {
		t.Fatalf("result = %+v", result)
	}

	history := read(http.MethodGet, path, "")
	var thread struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(history.Body.Bytes(), &thread); err != nil {
		t.Fatal(err)
	}
	if history.Code != http.StatusOK || len(thread.Messages) != 1 || thread.Messages[0]["id"] != result.Message["id"] {
		t.Fatalf("history = %d %s", history.Code, history.Body.String())
	}
	if other := read(http.MethodGet, "/api/mock/threads/missing", ""); other.Code != http.StatusNotFound {
		t.Fatalf("missing thread = %d %s", other.Code, other.Body.String())
	}
	for _, test := range []struct {
		input string
		code  int
	}{
		{`{"external_reply_id":"reply-1","text":"changed"}`, http.StatusConflict},
		{`{"external_reply_id":"reply-1","text":" "}`, http.StatusBadRequest},
		{`{"external_reply_id":"reply-1","text":"hello","buyer_id":"forged"}`, http.StatusBadRequest},
		{`{"external_reply_id":"reply-1","text":"hello","location":{"latitude":101}}`, http.StatusBadRequest},
		{input + input, http.StatusBadRequest},
		{`null`, http.StatusBadRequest},
	} {
		got := read(http.MethodPost, path+"/replies", test.input)
		if got.Code != test.code {
			t.Errorf("reply for %q = %d %s, want %d", test.input, got.Code, got.Body.String(), test.code)
		}
	}
}

func TestMockThreadHTTPDoesNotExposeRouting(t *testing.T) {
	message := domain.Message{
		ID: "message-1", PostRef: domain.PostRef{Platform: "mock", ExternalPostID: "post-1"},
		BuyerID: "buyer-1", ThreadID: "thread-1", Direction: domain.MessageAgent,
		LeadID: "private-lead", SellerID: "private-seller", Text: "Yes, it is available.",
	}
	handler := marketplace.NewThreadHandler(historyOnly{message: message}, "mock")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/api/mock/threads/thread-1", nil))
	body := resp.Body.String()
	if resp.Code != http.StatusOK || !strings.Contains(body, message.Text) {
		t.Fatalf("history = %d %s", resp.Code, body)
	}
	for _, secret := range []string{"private-lead", "private-seller", "lead_id", "seller_id"} {
		if strings.Contains(body, secret) {
			t.Fatalf("history leaked %q in %s", secret, body)
		}
	}
}

type threadPostLookup struct{ post domain.Post }

func (p threadPostLookup) GetPost(_ context.Context, ref domain.PostRef) (domain.Post, error) {
	if p.post.PostRef != ref {
		return domain.Post{}, &contracts.Error{Code: contracts.NotFound, Message: "post not found"}
	}
	return p.post, nil
}

type historyOnly struct {
	contracts.ThreadStore
	message domain.Message
}

func (h historyOnly) ThreadHistory(_ context.Context, ref domain.ThreadRef) ([]domain.Message, error) {
	if ref.Platform != h.message.Platform || ref.ThreadID != h.message.ThreadID {
		return nil, &contracts.Error{Code: contracts.NotFound, Message: "thread not found"}
	}
	return []domain.Message{h.message}, nil
}
