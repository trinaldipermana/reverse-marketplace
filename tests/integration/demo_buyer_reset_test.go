package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/demo"
)

// The marketplace endpoints below are HTTP test doubles. This test exercises
// fixture loading, operator reset authorization, and the buyer transport.
func TestDemoPurchaseFixtureTransportAfterReset(t *testing.T) {
	scenario, err := demo.LoadScenario(os.DirFS("../.."), "purchase")
	if err != nil {
		t.Fatal(err)
	}

	published := false
	mux := http.NewServeMux()
	err = demo.RegisterReset(mux, demo.ResetConfig{
		Enabled: true, Token: "local-test",
		Reset: func(context.Context) error {
			published = false
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("POST /api/mock/posts", func(w http.ResponseWriter, r *http.Request) {
		if published {
			http.Error(w, "expected fixture reset before republishing", http.StatusConflict)
			return
		}
		published = true
		w.WriteHeader(http.StatusCreated)
		_, _ = io.Copy(w, r.Body)
	})
	mux.HandleFunc("GET /api/mock/threads/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !published || r.PathValue("id") != scenario.Post.ThreadID {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"messages":[]}`)
	})
	buyer := demo.Buyer{
		BaseURL: "http://127.0.0.1:8080",
		Client:  &http.Client{Transport: inProcessHTTP{mux}},
	}
	ctx := context.Background()
	at := time.Date(2026, time.November, 7, 8, 0, 0, 0, time.UTC)
	var firstPost string
	for run := range 2 {
		if err := buyer.Reset(ctx, "local-test"); err != nil {
			t.Fatalf("reset %d: %v", run, err)
		}
		body, err := buyer.Publish(ctx, scenario.Post, at)
		if err != nil {
			t.Fatalf("publish %d: %v", run, err)
		}
		if run == 0 {
			firstPost = string(body)
		} else if firstPost != string(body) {
			t.Fatalf("same fixture and run time gave different requests: %q vs %q", firstPost, body)
		}
		if body, err = buyer.Thread(ctx, scenario.Post.ThreadID); err != nil || !json.Valid(body) {
			t.Fatalf("read thread %d: %s, %v", run, body, err)
		}
	}
}

func TestDemoFixturesKeepBuyerActionsDistinct(t *testing.T) {
	files := os.DirFS("../..")
	want := map[string]map[string]string{
		"purchase":   {"counteroffer": "5,4", "accept": "5,4"},
		"exception":  {"below-floor": "5,1"},
		"non_buying": {},
	}
	identities := make(map[string]bool)
	for name, steps := range want {
		scenario, err := demo.LoadScenario(files, name)
		if err != nil {
			t.Fatal(err)
		}
		post := scenario.Post
		if post.Location == nil || post.Location.City != "Jakarta" || !strings.Contains(strings.ToLower(post.Text), "laptop") {
			t.Fatalf("%s is missing the Jakarta laptop fixture: %+v", name, post)
		}
		if len(scenario.Replies) != len(steps) {
			t.Fatalf("%s has %d replies, want %d", name, len(scenario.Replies), len(steps))
		}
		for _, id := range []string{post.ExternalPostID, post.BuyerID, post.ThreadID} {
			if id == "" || identities[id] {
				t.Fatalf("%s has an empty or shared identity %q", name, id)
			}
			identities[id] = true
		}
		for step, price := range steps {
			reply := scenario.Replies[step]
			if !strings.Contains(reply.Text, price) || reply.ExternalReplyID == "" || identities[reply.ExternalReplyID] {
				t.Fatalf("%s/%s has invalid reply fixture %+v", name, step, reply)
			}
			identities[reply.ExternalReplyID] = true
		}
	}
}

// inProcessHTTP exercises the same handler routing without a test listener.
type inProcessHTTP struct {
	handler http.Handler
}

func (h inProcessHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.RemoteAddr = "127.0.0.1:4000"
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, r)
	return response.Result(), nil
}
