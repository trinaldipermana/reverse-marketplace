package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestReplyCommandUsesObservedMessageID(t *testing.T) {
	t.Setenv("DEMO_BASE_URL", "http://127.0.0.1:8080")
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/mock/threads/thread-demo-purchase/replies" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["reply_to_message_id"] != "agent-message-1" || payload["external_reply_id"] != "reply-demo-purchase-counteroffer" {
			t.Errorf("unexpected reply payload %+v", payload)
		}
		w := httptest.NewRecorder()
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"event_id":"event-1"}`)
		return w.Result(), nil
	})}
	var output bytes.Buffer
	err := run(context.Background(), []string{"reply", "purchase", "counteroffer", "agent-message-1"}, os.DirFS("../../../.."), client, &output)
	if err != nil || output.String() != "{\"event_id\":\"event-1\"}\n" {
		t.Fatalf("reply output = %q, error = %v", output.String(), err)
	}
}

func TestPostCommandRequiresStableRunTime(t *testing.T) {
	t.Setenv("DEMO_RUN_AT", "")
	err := run(context.Background(), []string{"post", "purchase"}, os.DirFS("../../../.."), nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "DEMO_RUN_AT") {
		t.Fatalf("post error = %v, want timestamp requirement", err)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
