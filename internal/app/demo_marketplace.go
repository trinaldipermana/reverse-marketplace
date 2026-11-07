package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace"
	"github.com/trinaldirizki/reverse-marketplace/internal/marketplace/mock"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
)

// demoFeed keeps discovery from seeing a newly stored post before its mock
// thread is available. The lock covers only in-memory operations.
type demoFeed struct {
	*mock.PostStore
	threads *mock.Threads
	mu      sync.RWMutex
}

func (f *demoFeed) CreatePost(ctx context.Context, post domain.Post, key string) (domain.Post, error) {
	if post.Platform != demoPlatform {
		return domain.Post{}, &contracts.Error{Code: contracts.InvalidArgument, Message: "The demo supports only the mock marketplace"}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	created, err := f.PostStore.CreatePost(ctx, post, key)
	if err != nil {
		return domain.Post{}, err
	}
	// Once saved, finish this local binding even if the caller disconnects.
	if err := f.threads.OpenThread(context.WithoutCancel(ctx), created.PostRef); err != nil {
		return domain.Post{}, err
	}
	return created, nil
}

func (f *demoFeed) GetPost(ctx context.Context, ref domain.PostRef) (domain.Post, error) {
	post, err := f.PostStore.GetPost(ctx, ref)
	if err == nil {
		post.Text = f.threads.RedactPromptText(post.Text)
	}
	return post, err
}

func (f *demoFeed) SearchPost(ctx context.Context, req contracts.SearchPostRequest) (contracts.SearchPostResult, error) {
	f.mu.RLock()
	result, err := f.PostStore.SearchPost(ctx, req)
	f.mu.RUnlock()
	if err != nil {
		return result, err
	}
	for i := range result.Posts {
		result.Posts[i].Text = f.threads.RedactPromptText(result.Posts[i].Text)
	}
	return result, nil
}

func demoReplyHistory(threads *mock.Threads) func(context.Context, domain.LeadRecord, agentruntime.Event) (P1ReplyHistory, error) {
	return func(ctx context.Context, lead domain.LeadRecord, event agentruntime.Event) (P1ReplyHistory, error) {
		ref := domain.ThreadRef{Platform: lead.Platform, ThreadID: lead.ThreadID}
		history, err := threads.LeadPromptHistory(ctx, ref, lead.ID, lead.SellerID)
		if err != nil {
			return P1ReplyHistory{}, err
		}
		messages := make([]llm.ChatMessage, 0, len(history))
		for _, message := range history {
			if message.Direction == domain.MessageAgent {
				messages = append(messages, llm.ChatMessage{Role: "assistant", Content: message.Text})
				continue
			}
			if message.Direction == domain.MessageBuyer {
				messages = append(messages, llm.ChatMessage{Role: "user", Content: message.Text})
				if message.ID == event.MessageID {
					return P1ReplyHistory{CurrentMessageID: message.ID, Messages: messages}, nil
				}
			}
		}
		return P1ReplyHistory{}, ErrP1ReplyHistory
	}
}

func demoRedirect(core *commerce.Service, sender *marketplace.AgentSender) agentruntime.ApprovedScopeRedirect {
	return func(ctx context.Context, event agentruntime.Event, actionID string) (contracts.AgentDeliveryResult, error) {
		lead, err := core.GetLead(ctx, event.LeadID)
		if err != nil {
			return contracts.AgentDeliveryResult{}, err
		}
		return sender.SendAgentMessage(ctx, contracts.AgentMessageRequest{
			ActionID: actionID,
			LeadID:   lead.ID,
			SellerID: lead.SellerID,
			Thread:   domain.ThreadRef{Platform: lead.Platform, ThreadID: lead.ThreadID},
			Channel:  contracts.AgentPublicReply,
			Text:     marketplace.AgentOffTopicRedirectText,
		})
	}
}

func maintainDemoRun(ctx context.Context, core *commerce.Service, threads *mock.Threads, intake *P1Runtime) {
	replies := time.NewTicker(250 * time.Millisecond)
	expiry := time.NewTicker(5 * time.Second)
	defer replies.Stop()
	defer expiry.Stop()

	local := newDemoIntake(intake, core.ExpireCheckouts)
	for {
		select {
		case <-ctx.Done():
			return
		case <-replies.C:
			// The marketplace retains each unsuccessful notification for retry.
			_ = threads.RelayBuyerReplies(ctx, local)
		case <-expiry.C:
			// P2 rechecks expiry on checkout operations and on the next pass.
			_, _ = core.ExpireCheckouts(ctx)
		}
	}
}

func newDemoIntake(intake contracts.BuyerReplyIntake, expire func(context.Context) (int, error)) contracts.BuyerReplyIntake {

	// Every reply stays pending in the marketplace until there is a durable
	// acknowledgment. Within this generation, avoid reprocessing accepted or
	// permanently unmatched replies on each pass. Transient failures stay retryable.
	type intakeResult struct {
		ack contracts.BuyerReplyAck
		err error
	}
	seen := make(map[string]intakeResult)
	return demoIntake(func(ctx context.Context, event contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error) {
		if result, ok := seen[event.ID]; ok {
			return result.ack, result.err
		}
		// GetLead itself does not expire checkouts. Apply expiry before
		// interpreting a reply as ineligible and caching that outcome.
		if _, err := expire(ctx); err != nil {
			return "", err
		}
		ack, err := intake.AcceptBuyerReply(ctx, event)
		if ack == contracts.BuyerReplyAcceptedVolatile || errors.Is(err, ErrP1ReplyUnassigned) ||
			errors.Is(err, ErrP1ReplyScope) || errors.Is(err, agentruntime.ErrLeadNotActive) {
			seen[event.ID] = intakeResult{ack: ack, err: err}
		}
		return ack, err
	})
}

type demoIntake func(context.Context, contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error)

func (f demoIntake) AcceptBuyerReply(ctx context.Context, event contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error) {
	return f(ctx, event)
}
