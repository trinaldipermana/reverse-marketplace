package marketplacetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func TestConversationFixture(t *testing.T) {
	ctx := context.Background()
	post := domain.Post{
		PostRef: domain.PostRef{Platform: "mock", ExternalPostID: "post-1"},
		BuyerID: "buyer-1", ThreadID: "thread-1", Text: "Need a used laptop",
		CreatedAt: time.Date(2026, 11, 7, 4, 0, 0, 0, time.UTC),
	}
	fixture, err := NewConversation(ctx, post, nil)
	if err != nil {
		t.Fatal(err)
	}
	if found, err := fixture.Posts.GetPost(ctx, post.PostRef); err != nil || found != post {
		t.Fatalf("post = %+v, %v", found, err)
	}

	thread := domain.ThreadRef{Platform: post.Platform, ThreadID: post.ThreadID}
	reply, err := fixture.Threads.SubmitBuyerReply(ctx, thread, contracts.BuyerReplyInput{
		ExternalReplyID: "reply-1", Text: "Do you have 16 GB RAM?",
	})
	if err != nil {
		t.Fatal(err)
	}
	history, err := fixture.Threads.ThreadHistory(ctx, thread)
	if err != nil || len(history) != 1 || history[0].ID != reply.Event.Message.ID {
		t.Fatalf("history = %+v, %v", history, err)
	}

	deliveryError := errors.New("fixture send failed")
	outbound := domain.Message{PostRef: post.PostRef, ThreadID: post.ThreadID, Direction: domain.MessageAgent, Text: "Yes."}
	sends := 0
	sender := SendStub{OnSend: func(_ context.Context, message domain.Message) error {
		sends++
		if message != outbound {
			t.Errorf("sent message = %+v, want %+v", message, outbound)
		}
		return deliveryError
	}}
	if err := sender.SendAgentMessage(ctx, outbound); !errors.Is(err, deliveryError) || sends != 1 {
		t.Fatalf("send = %v, calls = %d", err, sends)
	}
	history, err = fixture.Threads.ThreadHistory(ctx, thread)
	if err != nil || len(history) != 1 || history[0].Direction != domain.MessageBuyer {
		t.Fatalf("stub unexpectedly published a message, history = %+v, %v", history, err)
	}
}
