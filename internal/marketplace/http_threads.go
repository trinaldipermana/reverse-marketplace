package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// NewThreadHandler serves buyer replies and public thread history for one
// configured mock platform. The platform is independent of the URL path.
func NewThreadHandler(store contracts.ThreadStore, platform string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mock/threads/{id}", func(w http.ResponseWriter, r *http.Request) {
		ref := domain.ThreadRef{Platform: platform, ThreadID: r.PathValue("id")}
		history, err := store.ThreadHistory(r.Context(), ref)
		if err != nil {
			writeThreadStoreError(w, err)
			return
		}
		messages := make([]threadMessageJSON, len(history))
		for i, message := range history {
			messages[i] = threadMessageResponse(message)
		}
		writeMockJSON(w, http.StatusOK, struct {
			Messages []threadMessageJSON `json:"messages"`
		}{Messages: messages})
	})
	mux.HandleFunc("POST /api/mock/threads/{id}/replies", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeMockError(w, http.StatusRequestEntityTooLarge, contracts.InvalidArgument, "Reply body exceeds 64 KiB")
			return
		}
		if err != nil || !utf8.Valid(body) {
			writeMockError(w, http.StatusBadRequest, contracts.InvalidArgument, "Reply body must be UTF-8 JSON")
			return
		}

		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		var request *buyerReplyJSON
		if err := decoder.Decode(&request); err != nil || request == nil {
			writeMockError(w, http.StatusBadRequest, contracts.InvalidArgument, "Invalid reply JSON")
			return
		}
		var extra json.RawMessage
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			writeMockError(w, http.StatusBadRequest, contracts.InvalidArgument, "Expected a single reply object")
			return
		}

		input := contracts.BuyerReplyInput{
			ExternalReplyID: request.ExternalReplyID, Text: request.Text, ReplyToMessageID: request.ReplyToMessageID,
		}
		if request.Location != nil {
			input.Location = domain.Location(*request.Location)
		}
		ref := domain.ThreadRef{Platform: platform, ThreadID: r.PathValue("id")}
		result, err := store.SubmitBuyerReply(r.Context(), ref, input)
		if err != nil {
			writeThreadStoreError(w, err)
			return
		}
		status := http.StatusCreated
		if result.Replayed {
			status = http.StatusOK
		}
		writeMockJSON(w, status, struct {
			Message threadMessageJSON `json:"message"`
			EventID string            `json:"event_id"`
		}{Message: threadMessageResponse(result.Event.Message), EventID: result.Event.ID})
	})
	return mux
}

type buyerReplyJSON struct {
	ExternalReplyID  string            `json:"external_reply_id"`
	Text             string            `json:"text"`
	ReplyToMessageID string            `json:"reply_to_message_id"`
	Location         *mockLocationJSON `json:"location"`
}

type threadMessageJSON struct {
	ID               string                  `json:"id"`
	Platform         string                  `json:"platform"`
	ExternalPostID   string                  `json:"external_post_id"`
	ExternalReplyID  string                  `json:"external_reply_id,omitempty"`
	BuyerID          string                  `json:"buyer_id"`
	ThreadID         string                  `json:"thread_id"`
	Direction        domain.MessageDirection `json:"direction"`
	Text             string                  `json:"text"`
	CreatedAt        time.Time               `json:"created_at"`
	ReplyToMessageID string                  `json:"reply_to_message_id,omitempty"`
	Location         *mockLocationJSON       `json:"location,omitempty"`
}

func threadMessageResponse(message domain.Message) threadMessageJSON {
	response := threadMessageJSON{
		ID: message.ID, Platform: message.Platform, ExternalPostID: message.ExternalPostID,
		ExternalReplyID: message.ExternalReplyID, BuyerID: message.BuyerID, ThreadID: message.ThreadID,
		Direction: message.Direction, Text: message.Text, CreatedAt: message.CreatedAt.UTC(),
		ReplyToMessageID: message.ReplyToMessageID,
	}
	if message.Location != (domain.Location{}) {
		location := mockLocationJSON(message.Location)
		response.Location = &location
	}
	return response
}

func writeThreadStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		writeMockError(w, http.StatusGatewayTimeout, contracts.DependencyUnavailable, "Thread request timed out")
		return
	}
	if errors.Is(err, context.Canceled) {
		writeMockError(w, http.StatusRequestTimeout, contracts.DependencyUnavailable, "Thread request canceled")
		return
	}
	var coded *contracts.Error
	if errors.As(err, &coded) {
		switch coded.Code {
		case contracts.InvalidArgument:
			writeMockError(w, http.StatusBadRequest, coded.Code, coded.Message)
			return
		case contracts.NotFound:
			writeMockError(w, http.StatusNotFound, coded.Code, "Thread not found")
			return
		case contracts.IdempotencyConflict:
			writeMockError(w, http.StatusConflict, coded.Code, coded.Message)
			return
		case contracts.DependencyUnavailable:
			writeMockError(w, http.StatusServiceUnavailable, coded.Code, "Thread store unavailable")
			return
		}
	}
	writeMockError(w, http.StatusInternalServerError, contracts.DependencyUnavailable, "Thread request failed")
}
