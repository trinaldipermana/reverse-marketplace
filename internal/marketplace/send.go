package marketplace

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// Exact Go-owned templates keep buyer-facing terms independent of model prose.
const (
	AgentAcknowledgmentText     = "Thanks for your request."
	AgentOfferText              = "I can offer this item for {price}."
	AgentCheckoutText           = "Checkout for {price}: {checkout_link}"
	AgentOffTopicRedirectText   = "I can help with product fit, price, stock, or checkout."
	AgentFixedPriceDeclineText  = "Sorry, I can't lower the listed price."
	AgentFixedPriceOutreachText = "I found a match at a fixed price of {listing_price}."
	AgentPartnerListingText     = "Here is the partner listing: {partner_listing_link}"
)

// AgentCommerceFacts is the read-only subset of P2's commerce boundary needed
// to validate a draft. Ownership and commercial state remain with P2.
type AgentCommerceFacts interface {
	GetLead(ctx context.Context, leadID string) (domain.LeadRecord, error)
	GetItem(ctx context.Context, sellerID, itemID string) (domain.Item, error)
	GetRules(ctx context.Context, sellerID, itemID string) (domain.Rules, error)
}

// AgentSender validates and delivers a model's proposed text using trusted
// runtime scope, P2 facts, a P4 outbox, and a marketplace connector.
type AgentSender struct {
	commerce           AgentCommerceFacts
	outbox             contracts.AgentOutbox
	publish            contracts.AgentMessagePublisher
	origin             *url.URL
	now                func() time.Time
	partnerHosts       []string
	fixedPriceCheckout bool
}

func NewAgentSender(commerce AgentCommerceFacts, outbox contracts.AgentOutbox, publish contracts.AgentMessagePublisher, checkoutOrigin string, now func() time.Time) (*AgentSender, error) {
	sender, err := newAgentSender(commerce, outbox, publish, now)
	if err != nil {
		return nil, err
	}
	origin, err := url.Parse(checkoutOrigin)
	if err != nil || !checkoutHTTPURL(origin) || (origin.Path != "" && origin.Path != "/") || origin.String() != checkoutOrigin {
		return nil, &contracts.Error{Code: contracts.InvalidArgument, Message: "A valid checkout origin is required"}
	}
	sender.origin = origin
	return sender, nil
}

// NewFixedPriceCheckoutAgentSender uses the internal checkout path with a
// fixed listing price. Runtime still owns establishing buyer acceptance.
func NewFixedPriceCheckoutAgentSender(commerce AgentCommerceFacts, outbox contracts.AgentOutbox, publish contracts.AgentMessagePublisher, checkoutOrigin string, now func() time.Time) (*AgentSender, error) {
	sender, err := NewAgentSender(commerce, outbox, publish, checkoutOrigin, now)
	if err != nil {
		return nil, err
	}
	sender.fixedPriceCheckout = true
	return sender, nil
}

// NewFixedPriceAgentSender enables delivery of the matched partner listing.
// The same operator-configured hosts must be supplied to fixed-price discovery.
func NewFixedPriceAgentSender(commerce AgentCommerceFacts, outbox contracts.AgentOutbox, publish contracts.AgentMessagePublisher, approvedHosts []string, now func() time.Time) (*AgentSender, error) {
	sender, err := newAgentSender(commerce, outbox, publish, now)
	if err != nil {
		return nil, err
	}
	if len(approvedHosts) == 0 {
		return nil, &contracts.Error{Code: contracts.InvalidArgument, Message: "Approved partner hosts are required"}
	}
	sender.partnerHosts = slices.Clone(approvedHosts)
	return sender, nil
}

func newAgentSender(commerce AgentCommerceFacts, outbox contracts.AgentOutbox, publish contracts.AgentMessagePublisher, now func() time.Time) (*AgentSender, error) {
	if commerce == nil || outbox == nil || publish == nil {
		return nil, &contracts.Error{Code: contracts.DependencyUnavailable, Message: "Agent delivery is not configured"}
	}
	if now == nil {
		now = time.Now
	}
	return &AgentSender{commerce: commerce, outbox: outbox, publish: publish, now: now}, nil
}

// SendAgentMessage returns a receipt only after publication. A pending action
// may be retried with the same request. A delivered action is reconciled
// before any current policy, lead state, or checkout deadline is checked.
func (s *AgentSender) SendAgentMessage(ctx context.Context, req contracts.AgentMessageRequest) (contracts.AgentDeliveryResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AgentDeliveryResult{}, err
	}
	if !validAgentID(req.ActionID) || !validAgentID(req.LeadID) || !validAgentID(req.SellerID) ||
		!validAgentID(req.Thread.Platform) || !validAgentID(req.Thread.ThreadID) {
		return agentRejection(contracts.InvalidArgument, "Action, lead, seller, and thread scope are required")
	}
	if result, err := s.reconcileAgentMessage(ctx, req); err != nil || result.Status == contracts.AgentDeliveryDelivered {
		return result, err
	}

	publication, err := s.prepare(ctx, req)
	if err != nil {
		return s.reconcileAgentFailure(ctx, req, err, "Unable to verify current delivery facts")
	}
	if err := s.outbox.SaveAgentDraft(ctx, publication.Draft); err != nil {
		return s.reconcileAgentFailure(ctx, req, err, "Unable to save the pending message")
	}
	receipt, err := s.publish.PublishAgentMessage(ctx, publication)
	if err != nil {
		result, err := agentFailure(err, "Unable to confirm message publication")
		if result.Status != contracts.AgentDeliveryRejected {
			result.Status = contracts.AgentDeliveryPending
		}
		return result, err
	}
	return confirmedAgentMessage(req.ActionID, receipt)
}

func (s *AgentSender) reconcileAgentMessage(ctx context.Context, req contracts.AgentMessageRequest) (contracts.AgentDeliveryResult, error) {
	stored, found, err := s.outbox.FindAgentDraft(ctx, req.ActionID)
	if err != nil {
		return agentFailure(err, "Unable to read the delivery outbox")
	}
	if !found {
		return contracts.AgentDeliveryResult{}, nil
	}
	if stored.Request != req {
		return agentRejection(contracts.IdempotencyConflict, "Action ID is already bound to another request")
	}
	receipt, delivered, err := s.publish.FindAgentReceipt(ctx, req.ActionID)
	if err != nil {
		return agentFailure(err, "Unable to reconcile the delivery receipt")
	}
	if delivered {
		return confirmedAgentMessage(req.ActionID, receipt)
	}
	return contracts.AgentDeliveryResult{}, nil
}

func (s *AgentSender) reconcileAgentFailure(ctx context.Context, req contracts.AgentMessageRequest, cause error, message string) (contracts.AgentDeliveryResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AgentDeliveryResult{}, err
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return agentFailure(cause, message)
	}
	// Another caller may have published this action while this caller checked
	// current facts or tried to stage an obsolete draft.
	if result, err := s.reconcileAgentMessage(ctx, req); err != nil || result.Status == contracts.AgentDeliveryDelivered {
		return result, err
	}
	return agentFailure(cause, message)
}

func (s *AgentSender) prepare(ctx context.Context, req contracts.AgentMessageRequest) (contracts.AgentPublication, error) {
	var publication contracts.AgentPublication
	if req.Channel != contracts.AgentPublicReply && req.Channel != contracts.AgentDirectMessage || !s.publish.SupportsAgentChannel(req.Channel) {
		return publication, &contracts.Error{Code: contracts.AgentChannelUnsupported, Message: "Agent delivery channel is unsupported"}
	}
	if !s.acceptsTemplate(req.Text) {
		return publication, &contracts.Error{Code: contracts.InvalidArgument, Message: "Agent text must match a supported message template"}
	}
	lead, err := s.commerce.GetLead(ctx, req.LeadID)
	if err != nil {
		return publication, err
	}
	if lead.ID != req.LeadID || lead.SellerID != req.SellerID || lead.Platform != req.Thread.Platform || lead.ThreadID != req.Thread.ThreadID ||
		!validAgentID(lead.PostID) || !validAgentID(lead.BuyerID) || !validAgentID(lead.ItemID) {
		return publication, &contracts.Error{Code: contracts.InvalidArgument, Message: "Delivery scope does not match the lead"}
	}
	checkout := req.Text == AgentCheckoutText
	if checkout && (lead.State != domain.LeadReserved || req.CheckoutID == "") || !checkout && (lead.State != domain.LeadActive || req.CheckoutID != "") {
		return publication, &contracts.Error{Code: contracts.InvalidState, Message: "Lead state does not permit this message"}
	}
	item, err := s.commerce.GetItem(ctx, lead.SellerID, lead.ItemID)
	if err != nil {
		return publication, err
	}
	if item.ID != lead.ItemID || item.SellerID != lead.SellerID || !item.Active || item.ListPriceIDR <= 0 {
		return publication, &contracts.Error{Code: contracts.InvalidState, Message: "Lead item is unavailable"}
	}
	rules, err := s.commerce.GetRules(ctx, lead.SellerID, lead.ItemID)
	if err != nil {
		return publication, err
	}
	if rules.MessageCap < 1 || rules.RateLimitPerMinute < 1 || rules.FloorPriceIDR < 0 || rules.MaxDiscountBPS < 0 || rules.MaxDiscountBPS > 10000 {
		return publication, &contracts.Error{Code: contracts.InvalidState, Message: "Seller delivery policy does not permit this message"}
	}
	publication = contracts.AgentPublication{
		MessageCap: rules.MessageCap, RateLimitPerMinute: rules.RateLimitPerMinute,
		Draft: contracts.AgentDeliveryDraft{
			Request: req, BuyerID: lead.BuyerID, RenderedText: req.Text,
			Post: domain.PostRef{Platform: lead.Platform, ExternalPostID: lead.PostID},
		},
	}
	if req.Text == AgentAcknowledgmentText || req.Text == AgentOffTopicRedirectText || req.Text == AgentFixedPriceDeclineText {
		return publication, nil
	}
	if s.partnerHosts != nil {
		listing, err := commerce.PartnerListingFromItem(item, lead.SellerID, lead.ItemID, s.partnerHosts)
		if err != nil {
			return contracts.AgentPublication{}, err
		}
		if strings.ContainsFunc(listing.URL, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return contracts.AgentPublication{}, &contracts.Error{Code: contracts.InvalidArgument, Message: "Listing URL contains whitespace or control characters"}
		}
		switch req.Text {
		case AgentFixedPriceOutreachText:
			publication.Draft.RenderedText = strings.Replace(req.Text, "{listing_price}", "Rp "+strconv.FormatInt(listing.PriceIDR, 10), 1)
		case AgentPartnerListingText:
			publication.Draft.RenderedText = strings.Replace(req.Text, "{partner_listing_link}", listing.URL, 1)
		}
		return publication, nil
	}
	offer := lead.Offer
	if offer == nil || !validAgentID(offer.ID) || offer.LeadID != lead.ID || offer.ItemID != item.ID || offer.Qty != 1 || offer.PriceIDR <= 0 {
		return publication, &contracts.Error{Code: contracts.InvalidState, Message: "A valid stored offer is required"}
	}
	if s.fixedPriceCheckout && offer.PriceIDR != item.ListPriceIDR {
		return publication, &contracts.Error{Code: contracts.InvalidState, Message: "Stored offer must match the listing price"}
	}
	if offer.PriceIDR < effectiveAgentPriceFloor(item.ListPriceIDR, rules) {
		return publication, &contracts.Error{Code: contracts.PriceBelowMinimum, Message: "Stored offer does not meet current pricing policy"}
	}
	if !checkout && item.AvailableStock < offer.Qty {
		return publication, &contracts.Error{Code: contracts.OutOfStock, Message: "Offered item is unavailable"}
	}
	publication.Draft.OfferID = offer.ID
	publication.Draft.RenderedText = strings.Replace(req.Text, "{price}", "Rp "+strconv.FormatInt(offer.PriceIDR, 10), 1)
	if checkout {
		current := lead.Checkout
		if current == nil || current.ID != req.CheckoutID || current.LeadID != lead.ID || current.OfferID != offer.ID ||
			current.ItemID != offer.ItemID || current.PriceIDR != offer.PriceIDR || current.TotalIDR != offer.PriceIDR || current.Qty != offer.Qty {
			return publication, &contracts.Error{Code: contracts.InvalidState, Message: "A matching checkout from this turn is required"}
		}
		if current.Status == domain.CheckoutExpired || !current.ExpiresAt.After(s.now()) {
			return publication, &contracts.Error{Code: contracts.CheckoutExpired, Message: "Checkout has expired"}
		}
		if current.Status != domain.CheckoutPending || !s.validCheckoutLink(current.URL) {
			return publication, &contracts.Error{Code: contracts.InvalidState, Message: "Checkout is unavailable for delivery"}
		}
		publication.Draft.ExpiresAt = current.ExpiresAt.UTC()
		publication.Draft.RenderedText = strings.Replace(publication.Draft.RenderedText, "{checkout_link}", current.URL, 1)
	}
	return publication, nil
}

func (s *AgentSender) acceptsTemplate(text string) bool {
	if text == AgentAcknowledgmentText || text == AgentOffTopicRedirectText {
		return true
	}
	if s.partnerHosts != nil {
		return text == AgentFixedPriceOutreachText || text == AgentPartnerListingText
	}
	return text == AgentOfferText || text == AgentCheckoutText || (s.fixedPriceCheckout && text == AgentFixedPriceDeclineText)
}

func effectiveAgentPriceFloor(listPrice int64, rules domain.Rules) int64 {
	// Split before multiplying so the basis-point calculation cannot overflow.
	const basisPoints = 10000
	retained := int64(basisPoints - rules.MaxDiscountBPS)
	minimum := listPrice/basisPoints*retained + (listPrice%basisPoints*retained+basisPoints-1)/basisPoints
	return max(rules.FloorPriceIDR, minimum)
}

func (s *AgentSender) validCheckoutLink(raw string) bool {
	link, err := url.Parse(raw)
	if err != nil || !checkoutHTTPURL(link) || link.String() != raw || link.Scheme != s.origin.Scheme || !strings.EqualFold(link.Host, s.origin.Host) {
		return false
	}
	token, ok := strings.CutPrefix(link.Path, "/checkout/")
	return ok && token != "" && strings.IndexFunc(token, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) == -1
}

func checkoutHTTPURL(link *url.URL) bool {
	return (link.Scheme == "https" || link.Scheme == "http") &&
		link.Opaque == "" && link.User == nil && link.RawPath == "" && link.RawQuery == "" && !link.ForceQuery && link.Fragment == "" &&
		validCheckoutHost(link)
}

// Restrict host comparison to ASCII so Unicode case folding cannot equate
// names that the browser sends to different origins.
func validCheckoutHost(link *url.URL) bool {
	if strings.HasSuffix(link.Host, ":") {
		return false
	}
	if port := link.Port(); port != "" {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return false
		}
	}
	host := link.Hostname()
	if strings.HasPrefix(link.Host, "[") {
		ip, err := netip.ParseAddr(host)
		return err == nil && ip.Is6() && ip.Zone() == ""
	}
	host = strings.TrimSuffix(host, ".")
	if len(host) > 253 {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' || strings.IndexFunc(label, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-')
		}) != -1 {
			return false
		}
	}
	return true
}

func validAgentID(id string) bool {
	return id != "" && strings.TrimSpace(id) == id && utf8.ValidString(id) && strings.IndexFunc(id, unicode.IsControl) == -1
}

func confirmedAgentMessage(actionID string, receipt contracts.AgentDeliveryReceipt) (contracts.AgentDeliveryResult, error) {
	if receipt.ActionID != actionID || !validAgentID(receipt.MessageID) || receipt.PublishedAt.IsZero() {
		return contracts.AgentDeliveryResult{Status: contracts.AgentDeliveryPending}, &contracts.Error{
			Code: contracts.DependencyUnavailable, Message: "Unable to confirm message publication", Retryable: true,
		}
	}
	return contracts.AgentDeliveryResult{Status: contracts.AgentDeliveryDelivered, Receipt: receipt}, nil
}

func agentRejection(code contracts.Code, message string) (contracts.AgentDeliveryResult, error) {
	return contracts.AgentDeliveryResult{Status: contracts.AgentDeliveryRejected}, &contracts.Error{Code: code, Message: message}
}

func agentFailure(err error, message string) (contracts.AgentDeliveryResult, error) {
	switch {
	case errors.Is(err, context.Canceled):
		return contracts.AgentDeliveryResult{}, context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return contracts.AgentDeliveryResult{}, context.DeadlineExceeded
	}
	var serviceErr *contracts.Error
	if errors.As(err, &serviceErr) {
		if serviceErr.Code != contracts.DependencyUnavailable && !serviceErr.Retryable {
			return contracts.AgentDeliveryResult{Status: contracts.AgentDeliveryRejected}, serviceErr
		}
		return contracts.AgentDeliveryResult{}, serviceErr
	}
	return contracts.AgentDeliveryResult{}, &contracts.Error{
		Code: contracts.DependencyUnavailable, Message: message, Retryable: true, Cause: err,
	}
}
