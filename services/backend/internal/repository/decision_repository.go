package repository

import (
	"context"
	"fmt"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

type DecisionRepository interface {
	CreateBatchDecisionItems(ctx context.Context, items []models.DecisionItem) error
	GetDecisionItemsByMeeting(ctx context.Context, meetingID uuid.UUID) ([]models.DecisionItem, error)
	GetDecisionItemByID(ctx context.Context, itemID uuid.UUID) (*models.DecisionItem, error)
	UpdateDecisionItemStatus(ctx context.Context, itemID uuid.UUID, status string) error
	UpdateDecisionItem(ctx context.Context, itemID uuid.UUID, text string, ownerName *string) error
	UpdateDecisionItemFull(ctx context.Context, itemID uuid.UUID, text string, itemType string, itemContext *string, alternatives *string, sourceQuote *string, tsStart *float64, tsEnd *float64) error
	DeleteDecisionItem(ctx context.Context, itemID uuid.UUID) error
	DeleteUneditedItems(ctx context.Context, meetingID uuid.UUID) error
	GetUserEditedItemTexts(ctx context.Context, meetingID uuid.UUID) ([]string, error)
	ReplaceUneditedItems(ctx context.Context, meetingID uuid.UUID, newItems []models.DecisionItem) error
	GetConfirmedItemsByChain(ctx context.Context, chainID uuid.UUID) ([]models.DecisionItem, error)
	MarkUserEdited(ctx context.Context, itemID uuid.UUID) error
	UpdateChainIDForMeeting(ctx context.Context, meetingID, chainID uuid.UUID) error
	// UpdateChainIDForMeetingNull severs the chain_id linkage on every
	// decision_item attached to meetingID. Distinct method (rather than
	// a nullable param on UpdateChainIDForMeeting) so callers can't
	// accidentally pass uuid.Nil and write a zero-UUID instead of SQL
	// NULL. Phase 3 manual chain-removal cascades through here.
	UpdateChainIDForMeetingNull(ctx context.Context, meetingID uuid.UUID) error
}

type postgresDecisionRepository struct{}

func NewDecisionRepository() DecisionRepository {
	return &postgresDecisionRepository{}
}

func (r *postgresDecisionRepository) CreateBatchDecisionItems(ctx context.Context, items []models.DecisionItem) error {
	tx, err := DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, item := range items {
		_, err := tx.Exec(ctx, `
			INSERT INTO decision_items (id, meeting_id, chain_id, user_id, item_type, text, owner_name, source_quote, context, alternatives_considered, decision_timestamp_start, decision_timestamp_end, status, confirmed_at, user_edited)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		`, item.ID, item.MeetingID, item.ChainID, item.UserID, item.ItemType, item.Text, item.OwnerName, item.SourceQuote, item.Context, item.AlternativesConsidered, item.DecisionTimestampStart, item.DecisionTimestampEnd, item.Status, item.ConfirmedAt, item.UserEdited)
		if err != nil {
			return fmt.Errorf("failed to create decision item: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit batch insert: %w", err)
	}
	return nil
}

func (r *postgresDecisionRepository) GetDecisionItemsByMeeting(ctx context.Context, meetingID uuid.UUID) ([]models.DecisionItem, error) {
	query := `
		SELECT id, meeting_id, chain_id, user_id, item_type, text, owner_name, source_quote,
		       context, alternatives_considered, decision_timestamp_start, decision_timestamp_end,
		       status, user_edited, confirmed_at, completed_at, created_at, updated_at
		FROM decision_items
		WHERE meeting_id = $1
		ORDER BY created_at ASC
	`

	rows, err := DB.Query(ctx, query, meetingID)
	if err != nil {
		return nil, fmt.Errorf("failed to get decision items: %w", err)
	}
	defer rows.Close()

	var items []models.DecisionItem
	for rows.Next() {
		var item models.DecisionItem
		if err := rows.Scan(
			&item.ID, &item.MeetingID, &item.ChainID, &item.UserID, &item.ItemType,
			&item.Text, &item.OwnerName, &item.SourceQuote,
			&item.Context, &item.AlternativesConsidered, &item.DecisionTimestampStart, &item.DecisionTimestampEnd,
			&item.Status, &item.UserEdited, &item.ConfirmedAt, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan decision item: %w", err)
		}
		items = append(items, item)
	}

	return items, nil
}

func (r *postgresDecisionRepository) GetDecisionItemByID(ctx context.Context, itemID uuid.UUID) (*models.DecisionItem, error) {
	var item models.DecisionItem
	err := DB.QueryRow(ctx, `
		SELECT id, meeting_id, chain_id, user_id, item_type, text, owner_name, source_quote,
		       context, alternatives_considered, decision_timestamp_start, decision_timestamp_end,
		       status, user_edited, confirmed_at, completed_at, created_at, updated_at
		FROM decision_items WHERE id = $1
	`, itemID).Scan(
		&item.ID, &item.MeetingID, &item.ChainID, &item.UserID, &item.ItemType,
		&item.Text, &item.OwnerName, &item.SourceQuote,
		&item.Context, &item.AlternativesConsidered, &item.DecisionTimestampStart, &item.DecisionTimestampEnd,
		&item.Status, &item.UserEdited, &item.ConfirmedAt, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("decision item not found: %w", err)
	}
	return &item, nil
}

func (r *postgresDecisionRepository) UpdateDecisionItemStatus(ctx context.Context, itemID uuid.UUID, status string) error {
	now := time.Now()
	var query string

	switch status {
	case "confirmed":
		query = `UPDATE decision_items SET status = $1, confirmed_at = $2, updated_at = $2 WHERE id = $3`
	case "completed":
		query = `UPDATE decision_items SET status = $1, completed_at = $2, updated_at = $2 WHERE id = $3`
	default:
		query = `UPDATE decision_items SET status = $1, updated_at = $2 WHERE id = $3`
	}

	_, err := DB.Exec(ctx, query, status, now, itemID)
	if err != nil {
		return fmt.Errorf("failed to update decision item status: %w", err)
	}
	return nil
}

func (r *postgresDecisionRepository) UpdateDecisionItem(ctx context.Context, itemID uuid.UUID, text string, ownerName *string) error {
	_, err := DB.Exec(ctx, `
		UPDATE decision_items SET text = $1, owner_name = $2, user_edited = TRUE, updated_at = NOW() WHERE id = $3
	`, text, ownerName, itemID)
	if err != nil {
		return fmt.Errorf("failed to update decision item: %w", err)
	}
	return nil
}

func (r *postgresDecisionRepository) UpdateDecisionItemFull(ctx context.Context, itemID uuid.UUID, text string, itemType string, itemContext *string, alternatives *string, sourceQuote *string, tsStart *float64, tsEnd *float64) error {
	_, err := DB.Exec(ctx, `
		UPDATE decision_items
		SET text = $1, item_type = COALESCE(NULLIF($2, ''), item_type),
		    context = $3, alternatives_considered = $4,
		    source_quote = COALESCE($5, source_quote),
		    decision_timestamp_start = COALESCE($6, decision_timestamp_start),
		    decision_timestamp_end = COALESCE($7, decision_timestamp_end),
		    updated_at = NOW()
		WHERE id = $8
	`, text, itemType, itemContext, alternatives, sourceQuote, tsStart, tsEnd, itemID)
	if err != nil {
		return fmt.Errorf("failed to update decision item: %w", err)
	}
	return nil
}

func (r *postgresDecisionRepository) DeleteDecisionItem(ctx context.Context, itemID uuid.UUID) error {
	_, err := DB.Exec(ctx, `DELETE FROM decision_items WHERE id = $1`, itemID)
	if err != nil {
		return fmt.Errorf("failed to delete decision item: %w", err)
	}
	return nil
}

// DeleteUneditedItems removes items the user hasn't interacted with (safe to replace on re-extraction)
func (r *postgresDecisionRepository) DeleteUneditedItems(ctx context.Context, meetingID uuid.UUID) error {
	_, err := DB.Exec(ctx, `DELETE FROM decision_items WHERE meeting_id = $1 AND user_edited = FALSE`, meetingID)
	if err != nil {
		return fmt.Errorf("failed to delete unedited items: %w", err)
	}
	return nil
}

// GetUserEditedItemTexts returns texts of items the user has interacted with, used as LLM exclusion list
func (r *postgresDecisionRepository) GetUserEditedItemTexts(ctx context.Context, meetingID uuid.UUID) ([]string, error) {
	rows, err := DB.Query(ctx, `
		SELECT text FROM decision_items
		WHERE meeting_id = $1 AND user_edited = TRUE
	`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var texts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			continue
		}
		texts = append(texts, text)
	}
	return texts, nil
}

// ReplaceUneditedItems atomically deletes unedited items and inserts new ones.
// Inside the transaction it re-reads user_edited texts so that any concurrent
// user edits that landed after the LLM call are respected.
func (r *postgresDecisionRepository) ReplaceUneditedItems(ctx context.Context, meetingID uuid.UUID, newItems []models.DecisionItem) error {
	tx, err := DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Delete unedited items
	if _, err := tx.Exec(ctx, `DELETE FROM decision_items WHERE meeting_id = $1 AND user_edited = FALSE`, meetingID); err != nil {
		return fmt.Errorf("failed to delete unedited items: %w", err)
	}

	// 2. Re-read user-edited texts inside the transaction to get fresh exclusion list
	rows, err := tx.Query(ctx, `SELECT text FROM decision_items WHERE meeting_id = $1 AND user_edited = TRUE`, meetingID)
	if err != nil {
		return fmt.Errorf("failed to read edited texts: %w", err)
	}
	editedTexts := make(map[string]bool)
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			continue
		}
		editedTexts[t] = true
	}
	rows.Close()

	// 3. Insert only items whose text doesn't duplicate a user-edited item
	for _, item := range newItems {
		if editedTexts[item.Text] {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO decision_items (id, meeting_id, chain_id, user_id, item_type, text, owner_name, source_quote, context, alternatives_considered, decision_timestamp_start, decision_timestamp_end, status, confirmed_at, user_edited)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		`, item.ID, item.MeetingID, item.ChainID, item.UserID, item.ItemType, item.Text, item.OwnerName, item.SourceQuote, item.Context, item.AlternativesConsidered, item.DecisionTimestampStart, item.DecisionTimestampEnd, item.Status, item.ConfirmedAt, item.UserEdited); err != nil {
			return fmt.Errorf("failed to insert item: %w", err)
		}
	}

	return tx.Commit(ctx)
}

func (r *postgresDecisionRepository) GetConfirmedItemsByChain(ctx context.Context, chainID uuid.UUID) ([]models.DecisionItem, error) {
	query := `
		SELECT id, meeting_id, chain_id, user_id, item_type, text, owner_name, source_quote,
		       context, alternatives_considered, decision_timestamp_start, decision_timestamp_end,
		       status, user_edited, confirmed_at, completed_at, created_at, updated_at
		FROM decision_items
		WHERE chain_id = $1 AND status IN ('confirmed', 'completed')
		ORDER BY created_at ASC
	`

	rows, err := DB.Query(ctx, query, chainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []models.DecisionItem
	for rows.Next() {
		var item models.DecisionItem
		if err := rows.Scan(
			&item.ID, &item.MeetingID, &item.ChainID, &item.UserID, &item.ItemType,
			&item.Text, &item.OwnerName, &item.SourceQuote,
			&item.Context, &item.AlternativesConsidered, &item.DecisionTimestampStart, &item.DecisionTimestampEnd,
			&item.Status, &item.UserEdited, &item.ConfirmedAt, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

// MarkUserEdited sets user_edited=true when user interacts with an item
func (r *postgresDecisionRepository) MarkUserEdited(ctx context.Context, itemID uuid.UUID) error {
	_, err := DB.Exec(ctx, `UPDATE decision_items SET user_edited = TRUE, updated_at = NOW() WHERE id = $1`, itemID)
	return err
}

func (r *postgresDecisionRepository) UpdateChainIDForMeeting(ctx context.Context, meetingID, chainID uuid.UUID) error {
	_, err := DB.Exec(ctx, `UPDATE decision_items SET chain_id = $1, updated_at = NOW() WHERE meeting_id = $2`, chainID, meetingID)
	return err
}

func (r *postgresDecisionRepository) UpdateChainIDForMeetingNull(ctx context.Context, meetingID uuid.UUID) error {
	_, err := DB.Exec(ctx, `UPDATE decision_items SET chain_id = NULL, updated_at = NOW() WHERE meeting_id = $1`, meetingID)
	return err
}
