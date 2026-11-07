// Package marketplacetest provides local fixtures for marketplace consumers.
package marketplacetest

import (
	"context"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/mock"
)

type Conversation struct {
	Posts   *mock.PostStore
	Threads *mock.Threads
}

// NewConversation opens a thread for one seeded post with an optional test clock.
func NewConversation(ctx context.Context, post domain.Post, now func() time.Time) (*Conversation, error) {
	posts, err := mock.NewPostStore(post)
	if err != nil {
		return nil, err
	}
	threads := mock.NewThreads(posts, now)
	if err := threads.OpenThread(ctx, post.PostRef); err != nil {
		return nil, err
	}
	return &Conversation{Posts: posts, Threads: threads}, nil
}

// SendStub lets consumer tests observe a send or return an injected result
// without creating a buyer-visible message.
type SendStub struct {
	OnSend func(context.Context, domain.Message) error
}

func (s SendStub) SendAgentMessage(ctx context.Context, message domain.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.OnSend != nil {
		return s.OnSend(ctx, message)
	}
	return nil
}
