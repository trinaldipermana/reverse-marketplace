package tools

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

var ErrMessageRender = errors.New("message cannot be rendered from trusted facts")

var modelDestination = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://|www\.|(?:https?|ftp|mailto|tel|bitcoin|lightning|ethereum|payto|upi):|(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}\b|\[[^\]]*\]\([^)]*\)|(?:href|src|action)\s*=)`)
var checkoutToken = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// RenderMessage resolves the two specified placeholders using caller-supplied,
// trusted current records. It produces a candidate only; it never delivers it.
// No rules or raw errors are accepted here. The caller must supply the lead's
// current offer/checkout and enforce remaining message/state/rate guardrails.
// Recognizable URL/destination syntax is rejected before substitution. This is
// not semantic validation of arbitrary promises, price claims or natural-language
// payment instructions; those require separate final content validation.
func RenderMessage(text string, lead domain.LeadRecord, checkoutOrigin string, now time.Time) (string, error) {
	if modelDestination.MatchString(text) {
		return "", ErrMessageRender
	}
	remaining := strings.ReplaceAll(strings.ReplaceAll(text, "{price}", ""), "{checkout_link}", "")
	if strings.ContainsAny(remaining, "{}") {
		return "", ErrMessageRender
	}
	needsPrice := strings.Contains(text, "{price}")
	needsCheckout := strings.Contains(text, "{checkout_link}")
	if needsPrice || needsCheckout {
		if !boundOffer(lead) {
			return "", ErrMessageRender
		}
	}
	if needsCheckout {
		checkout := lead.Checkout
		offer := lead.Offer
		if lead.State != domain.LeadReserved || checkout == nil || checkout.ID == "" ||
			checkout.LeadID != lead.ID || checkout.OfferID != offer.ID || checkout.ItemID != offer.ItemID ||
			checkout.Qty != offer.Qty || checkout.PriceIDR != offer.PriceIDR || checkout.TotalIDR != offer.PriceIDR ||
			checkout.Status != domain.CheckoutPending || now.IsZero() || !checkout.ExpiresAt.After(now) ||
			!ownedCheckoutURL(checkout.URL, checkoutOrigin) {
			return "", ErrMessageRender
		}
		text = strings.ReplaceAll(text, "{checkout_link}", checkout.URL)
	}
	if needsPrice {
		text = strings.ReplaceAll(text, "{price}", formatIDR(lead.Offer.PriceIDR))
	}
	return text, nil
}

func boundOffer(lead domain.LeadRecord) bool {
	offer := lead.Offer
	return lead.ID != "" && lead.SellerID != "" && lead.ItemID != "" && offer != nil && offer.ID != "" &&
		offer.LeadID == lead.ID && offer.ItemID == lead.ItemID && offer.Qty == 1 && offer.PriceIDR > 0
}

func ownedCheckoutURL(raw, origin string) bool {
	base, err := url.Parse(origin)
	if err != nil || !httpOrigin(base) || (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" {
		return false
	}
	link, err := url.Parse(raw)
	if err != nil || !httpOrigin(link) || !strings.EqualFold(base.Scheme, link.Scheme) || !strings.EqualFold(base.Host, link.Host) || link.RawQuery != "" || link.Fragment != "" {
		return false
	}
	token, ok := strings.CutPrefix(link.Path, "/checkout/")
	return ok && checkoutToken.MatchString(token)
}

func httpOrigin(u *url.URL) bool {
	if u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return false
	}
	// Match P4's ASCII/punycode/IP boundary. Unicode EqualFold can equate
	// distinct browser origins (for example sigma and final sigma); callers
	// must configure internationalized names in their ASCII punycode form.
	// url.Parse has already decoded percent-encoded hostname characters here.
	for _, char := range u.Hostname() {
		if char > 127 {
			return false
		}
	}
	return true
}

func formatIDR(price int64) string {
	digits := strconv.FormatInt(price, 10)
	var out strings.Builder
	out.WriteString("Rp")
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteByte('.')
		}
		out.WriteRune(digit)
	}
	return out.String()
}
