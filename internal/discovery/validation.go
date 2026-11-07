package discovery

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/trinaldirizki/reverse-marketplace/internal/commerce"
	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

type eligibleCandidate struct {
	Match      ItemMatch
	Item       domain.Item
	DistanceKM float64
	PartnerURL string
}

func validDiscoveryIdentity(id string) bool {
	return id != "" && strings.TrimSpace(id) == id && len(id) <= 200 && utf8.ValidString(id)
}
func validPost(post domain.Post) bool {
	return validDiscoveryIdentity(post.Platform) && validDiscoveryIdentity(post.ExternalPostID) && validDiscoveryIdentity(post.BuyerID) && validDiscoveryIdentity(post.ThreadID) && strings.TrimSpace(post.Text) != "" && len(post.Text) <= 64<<10 && utf8.ValidString(post.Text) && !post.CreatedAt.IsZero()
}
func validIntent(intent domain.Intent) bool {
	if math.IsNaN(intent.Confidence) || math.IsInf(intent.Confidence, 0) || intent.Confidence < 0 || intent.Confidence > 1 {
		return false
	}
	if !intent.IsBuyingRequest {
		return true
	}
	return strings.TrimSpace(intent.ItemQuery) != "" && utf8.ValidString(intent.ItemQuery) && len(intent.ItemQuery) <= 2000 && len(intent.Requirements) <= 50
}
func businessRejection(err error) bool {
	var failure *contracts.Error
	if !errors.As(err, &failure) {
		return false
	}
	switch failure.Code {
	case contracts.NotFound, contracts.InvalidArgument, contracts.InvalidState, contracts.OutOfStock, contracts.PriceBelowMinimum:
		return true
	}
	return false
}
func (s *Service) validateCandidate(ctx context.Context, seller string, post domain.Post, matched PostMatch, candidate ItemMatch, allowed map[string]bool) (eligibleCandidate, string, error) {
	out := eligibleCandidate{Match: candidate, DistanceKM: math.Inf(1)}
	if !allowed[candidate.ItemID] {
		return out, "UNKNOWN_ITEM", nil
	}
	if math.IsNaN(candidate.Score) || math.IsInf(candidate.Score, 0) || candidate.Score < 0 || candidate.Score > 1 || strings.TrimSpace(candidate.Reason) == "" || len(candidate.Reason) > 2000 || !utf8.ValidString(candidate.Reason) {
		return out, "INVALID_MATCH", nil
	}
	if candidate.Score < s.opts.MatchThreshold {
		return out, "MATCH_THRESHOLD", nil
	}
	if matched.Intent.Qty != 1 {
		return out, "QUANTITY", nil
	}
	item, err := s.commerce.GetItem(ctx, seller, candidate.ItemID)
	if err != nil {
		if businessRejection(err) {
			return out, "ITEM_UNAVAILABLE", nil
		}
		return out, "", err
	}
	if item.ID != candidate.ItemID || item.SellerID != seller {
		return out, "SELLER_SCOPE", nil
	}
	if !item.Active || item.AvailableStock < 1 {
		return out, "NO_STOCK", nil
	}
	out.Item = item
	if !requirementsMatch(matched.Intent.Requirements, item) {
		return out, "ATTRIBUTES", nil
	}
	rules, err := s.commerce.GetRules(ctx, seller, item.ID)
	if err != nil {
		if businessRejection(err) {
			return out, "POLICY", nil
		}
		return out, "", err
	}
	minimum, err := commerce.MinimumOfferPrice(item.ListPriceIDR, rules)
	if err != nil {
		return out, "POLICY", nil
	}
	if s.opts.FixedPrice {
		listing, listingErr := commerce.PartnerListingFromItem(item, seller, item.ID, s.opts.PartnerHosts)
		if listingErr != nil {
			return out, "PARTNER_LISTING", nil
		}
		minimum = listing.PriceIDR
		out.PartnerURL = listing.URL
	}
	if matched.Intent.BudgetIDR != nil {
		if *matched.Intent.BudgetIDR <= 0 {
			return out, "BUDGET", nil
		}
		if !strings.EqualFold(strings.TrimSpace(matched.BudgetCurrency), "IDR") {
			return out, "CURRENCY", nil
		}
		if minimum > *matched.Intent.BudgetIDR {
			return out, "BUDGET", nil
		}
	}
	if matched.Intent.NeededBy != nil && !matched.Intent.NeededBy.After(s.opts.Now()) {
		return out, "EXPIRED_REQUEST", nil
	}
	distance, reason := locationMatch(post.Location, matched.Intent.Location, item.Location, s.opts.MaxDistanceKM)
	if reason != "" {
		return out, reason, nil
	}
	out.DistanceKM = distance
	return out, "", nil
}
func requirementsMatch(requirements []domain.Requirement, item domain.Item) bool {
	attrs := make(map[string]string, len(item.Attributes))
	for k, v := range item.Attributes {
		attrs[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	for _, requirement := range requirements {
		key := strings.ToLower(strings.TrimSpace(requirement.Name))
		wanted := strings.TrimSpace(requirement.Value)
		if key == "" || wanted == "" {
			return false
		}
		operator := "equal"
		attribute := key
		if strings.HasSuffix(key, "_min") {
			operator = "min"
			attribute = strings.TrimSuffix(key, "_min")
		}
		if strings.HasSuffix(key, "_max") {
			operator = "max"
			attribute = strings.TrimSuffix(key, "_max")
		}
		actual, ok := attrs[attribute]
		if !ok {
			return false
		}
		if operator == "equal" {
			if !strings.EqualFold(actual, wanted) {
				return false
			}
			continue
		}
		actualNumber, err1 := strconv.ParseFloat(actual, 64)
		wantedNumber, err2 := strconv.ParseFloat(wanted, 64)
		if err1 != nil || err2 != nil || math.IsNaN(actualNumber) || math.IsInf(actualNumber, 0) || math.IsNaN(wantedNumber) || math.IsInf(wantedNumber, 0) {
			return false
		}
		if operator == "min" && actualNumber < wantedNumber || operator == "max" && actualNumber > wantedNumber {
			return false
		}
	}
	return true
}
func knownCoordinates(location domain.Location) bool {
	if location.Latitude == nil || location.Longitude == nil {
		return false
	}
	lat, lon := *location.Latitude, *location.Longitude
	return !math.IsNaN(lat) && !math.IsNaN(lon) && !math.IsInf(lat, 0) && !math.IsInf(lon, 0) && math.Abs(lat) <= 90 && math.Abs(lon) <= 180
}
func normalCity(city string) string { return strings.ToLower(strings.Join(strings.Fields(city), " ")) }
func locationMatch(canonical, extracted, item domain.Location, maxDistance float64) (float64, string) {
	city := normalCity(canonical.City)
	textCity := normalCity(extracted.City)
	if city != "" && textCity != "" && city != textCity {
		return math.Inf(1), "LOCATION_CONFLICT"
	}
	// Only source coordinates are trusted for distance. Model-inferred coordinates
	// are never a substitute for unknown marketplace geometry.
	if knownCoordinates(canonical) && knownCoordinates(item) {
		rad := math.Pi / 180
		lat1, lat2 := *canonical.Latitude*rad, *item.Latitude*rad
		dlat, dlon := lat2-lat1, (*item.Longitude-*canonical.Longitude)*rad
		a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(lat1)*math.Cos(lat2)*math.Sin(dlon/2)*math.Sin(dlon/2)
		a = math.Max(0, math.Min(1, a))
		distance := 6371 * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
		if distance > maxDistance {
			return distance, "DISTANCE"
		}
		return distance, ""
	}
	if city == "" {
		city = textCity
	}
	itemCity := normalCity(item.City)
	if city == "" || itemCity == "" {
		return math.Inf(1), "NEEDS_LOCATION"
	}
	if city != itemCity {
		return math.Inf(1), "LOCATION"
	}
	// City equality is the documented mock fallback, not an invented 0km distance.
	return math.Inf(1), ""
}
