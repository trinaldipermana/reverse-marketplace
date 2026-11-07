package discovery

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func matchView(seller string, post domain.Post, chosen eligibleCandidate, lead domain.LeadRecord, status string) MatchView {
	name := post.BuyerID
	initials := ""
	if name != "" {
		r, _ := utf8.DecodeRuneInString(name)
		initials = strings.ToUpper(string(r))
	}
	availability := "Unavailable"
	if chosen.Item.AvailableStock > 0 {
		availability = "Available now"
	}
	return MatchView{MatchID: lead.ID, LeadID: lead.ID, ThreadID: post.ThreadID, MatchScore: int(math.Round(chosen.Match.Score * 100)), MatchReasons: []string{chosen.Match.Reason}, SellerID: seller, NotificationStatus: status, LeadState: lead.State,
		Buyer:   BuyerView{ID: post.BuyerID, Name: name, Initials: initials, Source: post.Platform, Post: post.Text, PostedAt: post.CreatedAt.UTC().Format(time.RFC3339)},
		Product: ProductView{ID: chosen.Item.ID, Name: chosen.Item.Name, Condition: chosen.Item.Attributes["condition"], Category: chosen.Item.Attributes["category"], Price: formatIDR(chosen.Item.ListPriceIDR), PriceIDR: chosen.Item.ListPriceIDR, Availability: availability, Marketplace: "Seller inventory", URL: ""},
	}
}
func formatIDR(amount int64) string {
	digits := strconv.FormatInt(amount, 10)
	var out strings.Builder
	out.WriteString("Rp")
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteByte('.')
		}
		out.WriteRune(r)
	}
	return out.String()
}
