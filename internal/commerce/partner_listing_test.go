package commerce

import (
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
	"testing"
)

func TestPartnerListingValidatesTrustedTerms(t *testing.T) {
	const destination = "https://partner.example/listing/thinkpad?ref=seller%2Bdemo&campaign=hackathon"
	base := domain.Item{ID: "item", SellerID: "seller", Name: "ThinkPad", Active: true, AvailableStock: 1, ListPriceIDR: 5800000, Attributes: map[string]string{"partner_listing_url": destination}}
	got, err := PartnerListingFromItem(base, "seller", "item", []string{"partner.example"})
	if err != nil || got.URL != destination || got.PriceIDR != 5800000 {
		t.Fatalf("%+v %v", got, err)
	}
	for _, raw := range []string{"", "http://partner.example/item", "https://partner.example.evil.test/item", "https://partner.example@evil.test/item", "https://user@partner.example/item", "https://partner.example:444/item", "https://partner.example/item#fragment", " https://partner.example/item"} {
		t.Run(raw, func(t *testing.T) {
			item := base
			item.Attributes = map[string]string{"partner_listing_url": raw}
			if _, err := PartnerListingFromItem(item, "seller", "item", []string{"partner.example"}); err == nil {
				t.Fatal("accepted invalid destination")
			}
		})
	}
	if _, err = PartnerListingFromItem(base, "other", "item", []string{"partner.example"}); err == nil {
		t.Fatal("accepted wrong seller")
	}
	if _, err = PartnerListingFromItem(base, "seller", "other", []string{"partner.example"}); err == nil {
		t.Fatal("accepted wrong item")
	}
	if _, err = PartnerListingFromItem(base, "seller", "item", nil); err == nil {
		t.Fatal("accepted unapproved host")
	}
	base.AvailableStock = 0
	if _, err = PartnerListingFromItem(base, "seller", "item", []string{"partner.example"}); err == nil {
		t.Fatal("accepted unavailable stock")
	}
}
