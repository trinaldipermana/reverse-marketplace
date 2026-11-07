package commerce

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

// RecordOffer records immutable one-unit terms; it does not reserve or deliver.
// Checks occur under the write transaction against current policy and stock.
func (s *Service) RecordOffer(ctx context.Context, leadID, itemID string, priceIDR int64, qty int, actionKey string) (domain.Offer, error) {
	if !validIdentity(leadID) || !validIdentity(itemID) || strings.TrimSpace(actionKey) == "" || len(actionKey) > 200 || priceIDR <= 0 || qty != 1 {
		return domain.Offer{}, commerceProblem(contracts.InvalidArgument, "invalid offer arguments")
	}
	request, err := json.Marshal(struct {
		LeadID, ItemID string
		PriceIDR       int64
		Qty            int
	}{leadID, itemID, priceIDR, qty})
	if err != nil {
		return domain.Offer{}, err
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return domain.Offer{}, err
	}
	defer tx.Rollback()
	lead, err := s.leadTx(ctx, tx, leadID)
	if err != nil {
		return domain.Offer{}, err
	}
	previous, replayed, err := replayAction(ctx, tx, lead.SellerID, actionKey, "record_offer", request)
	if err != nil {
		return domain.Offer{}, err
	}
	if replayed {
		var offer domain.Offer
		if err = json.Unmarshal(previous, &offer); err != nil {
			return domain.Offer{}, err
		}
		return offer, tx.Commit()
	}
	if lead.State != domain.LeadActive {
		return domain.Offer{}, commerceProblem(contracts.InvalidState, "lead is not active for an offer")
	}
	item, err := s.itemTx(ctx, tx, lead.SellerID, itemID)
	if err != nil {
		return domain.Offer{}, err
	}
	if !item.Active {
		return domain.Offer{}, commerceProblem(contracts.InvalidState, "item is inactive")
	}
	if item.AvailableStock < qty {
		return domain.Offer{}, commerceProblem(contracts.OutOfStock, "item is no longer available")
	}
	rules, err := effectiveRulesTx(ctx, tx, lead.SellerID, itemID)
	if err != nil {
		return domain.Offer{}, err
	}
	minimum, err := effectiveMinimum(item.ListPriceIDR, rules)
	if err != nil {
		return domain.Offer{}, err
	}
	if priceIDR < minimum {
		return domain.Offer{}, commerceProblem(contracts.PriceBelowMinimum, "offer is below the seller's minimum")
	}
	now := s.now()
	offer := domain.Offer{ID: s.id(), LeadID: leadID, ItemID: itemID, PriceIDR: priceIDR, Qty: qty, CreatedAt: time.UnixMilli(now.UnixMilli()).UTC()}
	_, err = tx.ExecContext(ctx, `INSERT INTO offers(id,seller_id,lead_id,item_id,price_idr,qty,policy_version,item_version,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, offer.ID, lead.SellerID, leadID, itemID, priceIDR, qty, rules.Version, item.Version, now.UnixMilli())
	if err != nil {
		return domain.Offer{}, err
	}
	// P1 verifies current lead item == current offer item when building context.
	_, err = tx.ExecContext(ctx, `UPDATE leads SET item_id=?,version=version+1,updated_at=? WHERE id=? AND state='active'`, itemID, now.UnixMilli(), leadID)
	if err != nil {
		return domain.Offer{}, err
	}
	result, err := json.Marshal(offer)
	if err != nil {
		return domain.Offer{}, err
	}
	if err = s.saveAction(ctx, tx, lead.SellerID, actionKey, "record_offer", request, result); err != nil {
		return domain.Offer{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Offer{}, err
	}
	return offer, nil
}
