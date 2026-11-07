package domain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func TestPrivateCommerceFieldsAreNotSerialized(t *testing.T) {
	values := []any{
		domain.Rules{FloorPriceIDR: 5_400_000, MaxDiscountBPS: 1000, MessageCap: 10},
		domain.LeadRecord{Checkout: &domain.Checkout{URL: "https://checkout.test/private-token"}},
	}
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"FloorPriceIDR", "MaxDiscountBPS", "5400000", "private-token"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("private field %q serialized in %s", secret, data)
			}
		}
	}
}
