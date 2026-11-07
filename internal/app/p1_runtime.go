package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"github.com/trinaldirizki/reverse-marketplace/internal/llm"
	agentruntime "github.com/trinaldirizki/reverse-marketplace/internal/runtime"
	agenttools "github.com/trinaldirizki/reverse-marketplace/internal/runtime/tools"
)

// P1ReplyHistory is an application binding, not a new P4 service contract.
// The trusted loader must return ordered, lead/seller-scoped, token-redacted
// user/assistant messages ending in CurrentMessageID's buyer request. Public
// ThreadHistory must not be passed through unchanged.
type P1ReplyHistory struct {
	CurrentMessageID string
	Messages         []llm.ChatMessage
}

type P1RuntimeConfig struct {
	Commerce      contracts.Commerce
	Posts         contracts.PostLookup
	Model         agentruntime.TurnModel
	Send          agenttools.AgentSendFunc
	SendOptions   agenttools.SendMessageOptions
	QueueCapacity int
	Limits        agentruntime.LoopLimits
	Instructions  func(context.Context, domain.LeadRecord) (string, error)
	ReplyHistory  func(context.Context, domain.LeadRecord, agentruntime.Event) (P1ReplyHistory, error)
	Classify      agentruntime.ScopeClassifier
	Redirect      agentruntime.ApprovedScopeRedirect
	Recorder      agentruntime.TurnRecorder
}

var (
	ErrP1ReplyUnassigned = errors.New("buyer reply has no unambiguous lead target")
	ErrP1ReplyScope      = errors.New("marketplace and lead identity do not match")
	ErrP1ReplyHistory    = errors.New("safe current buyer history is unavailable")
	ErrP1Dependency      = errors.New("runtime input dependency is unavailable")
)

// P1Runtime assembles only the P1 runtime slice. The shared app owner supplies
// existing services, P2 instruction/scope assets and approved P4 history/redirect
// bindings, mounts their handlers and starts Run with the server lifecycle.
// It creates no DB, route, durable acknowledgment or background goroutine.
type P1Runtime struct {
	config P1RuntimeConfig
	worker *agentruntime.Worker
	guard  *agentruntime.ScopeGuard
}

var _ contracts.BuyerReplyIntake = (*P1Runtime)(nil)

// NewP1Runtime always installs ScopeGuard. Missing classification assets fail
// closed, rather than exposing an unguarded TurnRunner. ReplyHistory may be nil
// for initial outreach only; buyer-reply execution then fails closed.
func NewP1Runtime(config P1RuntimeConfig) (*P1Runtime, error) {
	if config.Commerce == nil || config.Posts == nil || config.Send == nil || config.Instructions == nil || strings.TrimSpace(config.SendOptions.CheckoutOrigin) == "" {
		return nil, errors.New("runtime assembly requires commerce, posts, sender, instructions and checkout origin")
	}
	if config.Limits.Timeout == 0 {
		config.Limits.Timeout = 60 * time.Second
	}
	factory := func(_ context.Context, loaded *agentruntime.LeadContext, metadata agentruntime.TurnMetadata) ([]agentruntime.Tool, error) {
		tools, err := agenttools.NewCommerceTools(config.Commerce, &loaded.Lead, metadata.ActionKey)
		if err != nil {
			return nil, err
		}
		send, err := agenttools.NewSendMessageTool(config.Send, &loaded.Lead, metadata.ActionKey, config.SendOptions)
		if err != nil {
			return nil, err
		}
		return append(tools, send), nil
	}
	runner, err := agentruntime.NewTurnRunner(config.Commerce, config.Model, factory, config.Limits, config.Recorder)
	if err != nil {
		return nil, err
	}
	guard, err := agentruntime.NewScopeGuard(config.Classify, runner.RunTurn, config.Redirect)
	if err != nil {
		return nil, err
	}
	assembled := &P1Runtime{config: config, guard: guard}
	assembled.worker, err = agentruntime.NewWorker(config.QueueCapacity, assembled.runEvent)
	if err != nil {
		return nil, err
	}
	return assembled, nil
}

// Run is the explicit shared-app lifecycle hook; cancel ctx and wait for it.
func (r *P1Runtime) Run(ctx context.Context) error { return r.worker.Run(ctx) }
func (r *P1Runtime) Result(eventID string) (agentruntime.EventResult, bool) {
	return r.worker.Result(eventID)
}

// OnLeadDiscovered is directly compatible with P2's existing LeadNotifier.
// Nil means volatile queue acceptance, not buyer delivery.
func (r *P1Runtime) OnLeadDiscovered(ctx context.Context, eventID, leadID string) error {
	return r.worker.OnLeadDiscovered(ctx, eventID, leadID)
}

// AcceptBuyerReply uses only P4's explicit reply-to-agent routing hint. Missing
// or ambiguous targets remain unassigned; it never fans out to seller leads.
func (r *P1Runtime) AcceptBuyerReply(ctx context.Context, event contracts.BuyerReplyEvent) (contracts.BuyerReplyAck, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	message := event.Message
	if strings.TrimSpace(message.LeadID) == "" || strings.TrimSpace(message.SellerID) == "" {
		return "", ErrP1ReplyUnassigned
	}
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(message.ID) == "" || message.Direction != domain.MessageBuyer || strings.TrimSpace(message.Text) == "" {
		return "", ErrP1ReplyScope
	}
	lead, err := r.config.Commerce.GetLead(ctx, message.LeadID)
	if err != nil {
		return "", p1DependencyError(ctx, err)
	}
	if lead.ID != message.LeadID || lead.SellerID != message.SellerID || lead.Platform != message.Platform || lead.PostID != message.ExternalPostID || lead.ThreadID != message.ThreadID || lead.BuyerID != message.BuyerID {
		return "", ErrP1ReplyScope
	}
	if lead.State != domain.LeadActive {
		return "", agentruntime.ErrLeadNotActive
	}
	if _, err := r.canonicalPost(ctx, lead); err != nil {
		return "", err
	}
	err = r.worker.Enqueue(ctx, agentruntime.Event{ID: event.ID, LeadID: lead.ID, Trigger: agentruntime.TriggerBuyerReply, MessageID: message.ID})
	if err != nil {
		return "", err
	}
	return contracts.BuyerReplyAcceptedVolatile, nil
}

func (r *P1Runtime) runEvent(ctx context.Context, event agentruntime.Event) error {
	ctx, cancel := context.WithTimeout(ctx, r.config.Limits.Timeout)
	defer cancel()
	lead, err := r.config.Commerce.GetLead(ctx, event.LeadID)
	if err != nil {
		return p1DependencyError(ctx, err)
	}
	if lead.ID != event.LeadID {
		return ErrP1ReplyScope
	}
	if lead.State != domain.LeadActive {
		return agentruntime.ErrLeadNotActive
	}
	post, err := r.canonicalPost(ctx, lead)
	if err != nil {
		return err
	}
	history := []llm.ChatMessage{{Role: "user", Content: post.Text}}
	trigger := "Process the initial seller outreach for this saved buyer request."
	switch event.Trigger {
	case agentruntime.TriggerLeadDiscovered:
	case agentruntime.TriggerBuyerReply:
		if r.config.ReplyHistory == nil || strings.TrimSpace(event.MessageID) == "" {
			return ErrP1ReplyHistory
		}
		snapshot, err := r.config.ReplyHistory(ctx, lead, event)
		if err != nil {
			return p1DependencyError(ctx, err)
		}
		if snapshot.CurrentMessageID != event.MessageID || len(snapshot.Messages) == 0 {
			return ErrP1ReplyHistory
		}
		last := snapshot.Messages[len(snapshot.Messages)-1]
		if last.Role != "user" || strings.TrimSpace(last.Content) == "" {
			return ErrP1ReplyHistory
		}
		history = append(history, snapshot.Messages...)
		trigger = "Process the current saved buyer reply."
	default:
		return ErrP1ReplyScope
	}
	instructions, err := r.config.Instructions(ctx, lead)
	if err != nil {
		return p1DependencyError(ctx, err)
	}
	_, err = r.guard.RunTurn(ctx, agentruntime.TurnInput{Event: event, Instructions: instructions, TriggerInstruction: trigger, History: history})
	return err
}

func (r *P1Runtime) canonicalPost(ctx context.Context, lead domain.LeadRecord) (domain.Post, error) {
	ref := domain.PostRef{Platform: lead.Platform, ExternalPostID: lead.PostID}
	post, err := r.config.Posts.GetPost(ctx, ref)
	if err != nil {
		return domain.Post{}, p1DependencyError(ctx, err)
	}
	if post.PostRef != ref || post.BuyerID != lead.BuyerID || post.ThreadID != lead.ThreadID || strings.TrimSpace(post.Text) == "" {
		return domain.Post{}, ErrP1ReplyScope
	}
	return post, nil
}

func p1DependencyError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrP1Dependency
}
