package mock

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

const promptURLSlash = `(?:/|\\/|%2f)`

// Checkout tokens are URL-safe. Match the checkout paths with an optional
// HTTP authority, including common escaped forms of copied links.
var promptCheckoutURL = regexp.MustCompile(
	`(?i)(?:(?:https?(?::|%3a))?` + promptURLSlash + `{2}(?:\[[0-9a-f:.]+\]|[a-z0-9.-]+)(?::[0-9]+)?)?` +
		promptURLSlash + `(?:checkout|api` + promptURLSlash + `checkouts)` + promptURLSlash +
		`(?:[a-z0-9_-]|%[0-9a-f]{2})+(?:[?#][^\s<>"'` + "`" + `()\[\],;]*)?`,
)

// LeadPromptHistory returns a copy of the canonical thread's oldest-first
// messages for this exact lead and seller, with checkout bearer links removed.
func (s *Threads) LeadPromptHistory(ctx context.Context, ref domain.ThreadRef, leadID, sellerID string) ([]domain.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validMarketplaceIdentifier(leadID) || !validMarketplaceIdentifier(sellerID) {
		return nil, invalidReplyArgument("Lead and seller ID are required")
	}
	messages, err := s.ThreadHistory(ctx, ref)
	if err != nil {
		return nil, err
	}
	messages = slices.DeleteFunc(messages, func(message domain.Message) bool {
		return message.LeadID != leadID || message.SellerID != sellerID ||
			(message.Direction == domain.MessageBuyer && message.ReplyToMessageID == "")
	})
	redact := s.promptRedactor()
	for i := range messages {
		messages[i].Text = redact(messages[i].Text)
	}
	return messages, nil
}

// RedactPromptText removes checkout links and copies of previously published
// bearer tokens from a standalone post before it reaches a model.
func (s *Threads) RedactPromptText(text string) string {
	return s.promptRedactor()(text)
}

func (s *Threads) promptRedactor() func(string) string {
	s.mu.RLock()
	var tokens []string
	for actionID := range s.receipts {
		draft := s.drafts[actionID]
		if draft.Request.CheckoutID != "" {
			_, token, found := strings.Cut(draft.RenderedText, "/checkout/")
			if found && token != "" {
				tokens = append(tokens, token)
			}
		}
	}
	s.mu.RUnlock()
	slices.SortFunc(tokens, func(a, b string) int { return len(b) - len(a) })
	replacements := make([]string, 0, 2*len(tokens))
	for _, token := range tokens {
		replacements = append(replacements, token, "[checkout token redacted]")
	}
	redactToken := strings.NewReplacer(replacements...)
	return func(text string) string {
		text = promptCheckoutURL.ReplaceAllStringFunc(text, func(link string) string {
			trimmed := strings.TrimRight(link, ".:!?")
			return "[checkout link redacted]" + link[len(trimmed):]
		})
		return redactToken.Replace(text)
	}
}
