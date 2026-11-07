package commerce

import (
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// effectiveMinimum applies both effective price limits using exact integer
// arithmetic. Splitting the product avoids overflowing int64 for large prices.
func effectiveMinimum(listPriceIDR int64, rules domain.Rules) (int64, error) {
	if listPriceIDR <= 0 || rules.FloorPriceIDR < 0 || rules.MaxDiscountBPS < 0 || rules.MaxDiscountBPS > 10000 {
		return 0, &contracts.Error{Code: contracts.InvalidState, Message: "item pricing policy is invalid"}
	}
	factor := int64(10000 - rules.MaxDiscountBPS)
	discounted := (listPriceIDR/10000)*factor + ((listPriceIDR%10000)*factor+9999)/10000
	if rules.FloorPriceIDR > discounted {
		return rules.FloorPriceIDR, nil
	}
	return discounted, nil
}

// MinimumOfferPrice is the same authoritative integer calculation used by
// offers/checkout, exposed for discovery budget eligibility without exposing
// seller limits to the model or duplicating rounding logic.
func MinimumOfferPrice(listPriceIDR int64, rules domain.Rules) (int64, error) {
	return effectiveMinimum(listPriceIDR, rules)
}
