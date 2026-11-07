package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Buyer drives the mock marketplace HTTP surface from outside the application.
type Buyer struct {
	BaseURL string
	Client  *http.Client
}

type Post struct {
	Platform       string    `json:"platform"`
	ExternalPostID string    `json:"external_post_id"`
	BuyerID        string    `json:"buyer_id"`
	ThreadID       string    `json:"thread_id"`
	Text           string    `json:"text"`
	Location       *Location `json:"location,omitempty"`
}

type Location struct {
	City      string   `json:"city,omitempty"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

type Reply struct {
	ExternalReplyID string `json:"external_reply_id"`
	Text            string `json:"text"`
}

// Publish keeps the idempotency key and request stable when retried with the
// same post and run time.
func (b Buyer) Publish(ctx context.Context, post Post, at time.Time) ([]byte, error) {
	if at.IsZero() {
		return nil, fmt.Errorf("a demo run time is required to publish a post")
	}
	payload := struct {
		Post
		CreatedAt time.Time `json:"created_at"`
	}{Post: post, CreatedAt: at.UTC()}
	return b.request(ctx, http.MethodPost, "/api/mock/posts", payload, "demo-post-"+post.ExternalPostID, "", http.StatusCreated)
}

// Reply uses the stable external reply ID. To address a seller in a shared
// public thread, supply the actual message ID shown in the thread response.
func (b Buyer) Reply(ctx context.Context, threadID string, reply Reply, replyTo string) ([]byte, error) {
	payload := struct {
		Reply
		ReplyToMessageID string `json:"reply_to_message_id,omitempty"`
	}{Reply: reply, ReplyToMessageID: replyTo}
	return b.request(ctx, http.MethodPost, threadPath(threadID)+"/replies", payload, "", "", http.StatusCreated, http.StatusOK)
}

func (b Buyer) Thread(ctx context.Context, threadID string) ([]byte, error) {
	return b.request(ctx, http.MethodGet, threadPath(threadID), nil, "", "", http.StatusOK)
}

func threadPath(threadID string) string {
	return "/api/mock/threads/" + url.PathEscape(threadID)
}

func (b Buyer) request(ctx context.Context, method, path string, payload any, replayKey, token string, want ...int) ([]byte, error) {
	var requestBody io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		requestBody = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(b.BaseURL, "/")+path, requestBody)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if replayKey != "" {
		request.Header.Set("Idempotency-Key", replayKey)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := b.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("%s %s returned more than %d bytes", method, path, maxBody)
	}
	for _, status := range want {
		if response.StatusCode == status {
			return body, nil
		}
	}
	return nil, fmt.Errorf("%s %s returned %s: %s", method, path, response.Status, strings.TrimSpace(string(body)))
}
