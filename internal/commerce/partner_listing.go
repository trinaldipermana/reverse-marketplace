package commerce

import (
	"context"
	"net/url"
	"strings"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// PartnerListing contains public fixed-price terms from seller-controlled inventory.
// It is a read, not an offer, reservation, checkout or delivery receipt.
type PartnerListing struct {
	SellerID string `json:"sellerId"`
	ItemID   string `json:"itemId"`
	Name     string `json:"name"`
	PriceIDR int64  `json:"priceIDR"`
	URL      string `json:"url"`
}

// PartnerListingFromItem accepts only exact approved HTTPS hostnames. The caller
// configures hosts; buyer/model text must never supply them or the destination.
// URL is returned byte-for-byte, preserving existing partner query parameters.
func PartnerListingFromItem(item domain.Item, sellerID, itemID string, approvedHosts []string) (PartnerListing, error) {
	fail := func(message string) (PartnerListing, error) {
		return PartnerListing{}, &contracts.Error{Code: contracts.InvalidArgument, Message: message}
	}
	if sellerID == "" || itemID == "" || item.SellerID != sellerID || item.ID != itemID {
		return fail("Listing does not belong to this seller and item")
	}
	if !item.Active || item.AvailableStock < 1 || item.ListPriceIDR <= 0 {
		return fail("Listing is unavailable")
	}
	raw := item.Attributes["partner_listing_url"]
	u, err := url.Parse(raw)
	if err != nil || raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\t") || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Port() != "" && u.Port() != "443" {
		return fail("Listing requires an approved HTTPS partner URL")
	}
	approved := false
	for _, host := range approvedHosts {
		if host != "" && strings.EqualFold(u.Hostname(), host) {
			approved = true
			break
		}
	}
	if !approved {
		return fail("Listing partner host is not approved")
	}
	return PartnerListing{SellerID: sellerID, ItemID: itemID, Name: item.Name, PriceIDR: item.ListPriceIDR, URL: raw}, nil
}

func (s *Service) GetPartnerListing(ctx context.Context, sellerID, itemID string, approvedHosts []string) (PartnerListing, error) {
	item, err := s.GetItem(ctx, sellerID, itemID)
	if err != nil {
		return PartnerListing{}, err
	}
	return PartnerListingFromItem(item, sellerID, itemID, approvedHosts)
}
