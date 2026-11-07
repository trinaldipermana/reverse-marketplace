package commerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/contracts"
	"github.com/trinaldirizki/reverse-marketplace/internal/domain"
)

func currentOfferTx(ctx context.Context, tx *sql.Tx, leadID string) (*domain.Offer, error) {
	var offer domain.Offer
	var createdAt int64
	err := tx.QueryRowContext(ctx, `SELECT id,lead_id,item_id,price_idr,qty,created_at FROM offers WHERE lead_id=? ORDER BY seq DESC LIMIT 1`, leadID).
		Scan(&offer.ID, &offer.LeadID, &offer.ItemID, &offer.PriceIDR, &offer.Qty, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	offer.CreatedAt = time.UnixMilli(createdAt).UTC()
	return &offer, nil
}

func (s *Service) leadTx(ctx context.Context, tx *sql.Tx, leadID string) (domain.LeadRecord, error) {
	var lead domain.LeadRecord
	var intent string
	err := tx.QueryRowContext(ctx, `SELECT id,seller_id,platform,post_id,buyer_id,thread_id,item_id,state,intent_json,version FROM leads WHERE id=?`, leadID).
		Scan(&lead.ID, &lead.SellerID, &lead.Platform, &lead.PostID, &lead.BuyerID, &lead.ThreadID, &lead.ItemID, &lead.State, &intent, &lead.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return lead, commerceProblem(contracts.NotFound, "lead not found")
	}
	if err != nil {
		return lead, err
	}
	if err = json.Unmarshal([]byte(intent), &lead.Intent); err != nil {
		return domain.LeadRecord{}, err
	}
	if lead.Offer, err = currentOfferTx(ctx, tx, leadID); err != nil {
		return domain.LeadRecord{}, err
	}
	if lead.Checkout, err = s.checkoutForLead(ctx, tx, leadID); err != nil {
		return domain.LeadRecord{}, err
	}
	return lead, nil
}

// GetLead is an internal trusted-runtime lookup. Seller-facing callers use
// GetSellerLead or ListLeads so another seller's identity cannot escape scope.
func (s *Service) GetLead(ctx context.Context, leadID string) (domain.LeadRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.LeadRecord{}, err
	}
	defer tx.Rollback()
	lead, err := s.leadTx(ctx, tx, leadID)
	if err != nil {
		return domain.LeadRecord{}, err
	}
	return lead, tx.Commit()
}

func (s *Service) GetSellerLead(ctx context.Context, sellerID, leadID string) (domain.LeadRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.LeadRecord{}, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM leads WHERE seller_id=? AND id=?`, sellerID, leadID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.LeadRecord{}, commerceProblem(contracts.NotFound, "lead not found")
	}
	if err != nil {
		return domain.LeadRecord{}, err
	}
	lead, err := s.leadTx(ctx, tx, id)
	if err != nil {
		return domain.LeadRecord{}, err
	}
	return lead, tx.Commit()
}

func (s *Service) GetItem(ctx context.Context, sellerID, itemID string) (domain.Item, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Item{}, err
	}
	defer tx.Rollback()
	item, err := s.itemTx(ctx, tx, sellerID, itemID)
	if err != nil {
		return domain.Item{}, err
	}
	return item, tx.Commit()
}

func (s *Service) GetRules(ctx context.Context, sellerID, itemID string) (domain.Rules, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Rules{}, err
	}
	defer tx.Rollback()
	rules, err := effectiveRulesTx(ctx, tx, sellerID, itemID)
	if err != nil {
		return domain.Rules{}, err
	}
	return rules, tx.Commit()
}
