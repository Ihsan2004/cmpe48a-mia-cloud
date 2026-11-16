package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// ==================== Mock Decision Repository ====================

type mockDecisionRepo struct {
	items map[uuid.UUID]*models.DecisionItem
}

func newMockDecisionRepo() *mockDecisionRepo {
	return &mockDecisionRepo{items: make(map[uuid.UUID]*models.DecisionItem)}
}

func (m *mockDecisionRepo) UpdateChainIDForMeetingNull(_ context.Context, _ uuid.UUID) error {
	return nil
}

func (m *mockDecisionRepo) CreateBatchDecisionItems(_ context.Context, items []models.DecisionItem) error {
	for _, item := range items {
		m.items[item.ID] = &item
	}
	return nil
}

func (m *mockDecisionRepo) GetDecisionItemsByMeeting(_ context.Context, meetingID uuid.UUID) ([]models.DecisionItem, error) {
	var result []models.DecisionItem
	for _, item := range m.items {
		if item.MeetingID == meetingID {
			result = append(result, *item)
		}
	}
	return result, nil
}

func (m *mockDecisionRepo) GetDecisionItemByID(_ context.Context, itemID uuid.UUID) (*models.DecisionItem, error) {
	item, ok := m.items[itemID]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return item, nil
}

func (m *mockDecisionRepo) UpdateDecisionItemStatus(_ context.Context, itemID uuid.UUID, status string) error {
	item, ok := m.items[itemID]
	if !ok {
		return fmt.Errorf("not found")
	}
	item.Status = status
	now := time.Now()
	if status == "confirmed" {
		item.ConfirmedAt = &now
	}
	if status == "completed" {
		item.CompletedAt = &now
	}
	return nil
}

func (m *mockDecisionRepo) UpdateDecisionItem(_ context.Context, itemID uuid.UUID, text string, ownerName *string) error {
	item, ok := m.items[itemID]
	if !ok {
		return fmt.Errorf("not found")
	}
	item.Text = text
	item.OwnerName = ownerName
	item.UserEdited = true
	return nil
}

func (m *mockDecisionRepo) UpdateDecisionItemFull(_ context.Context, itemID uuid.UUID, text string, itemType string, itemContext *string, alternatives *string, sourceQuote *string, tsStart *float64, tsEnd *float64) error {
	if item, ok := m.items[itemID]; ok {
		item.Text = text
		if itemType != "" {
			item.ItemType = itemType
		}
		item.Context = itemContext
		item.AlternativesConsidered = alternatives
		item.SourceQuote = sourceQuote
		item.DecisionTimestampStart = tsStart
		item.DecisionTimestampEnd = tsEnd
	}
	return nil
}

func (m *mockDecisionRepo) DeleteDecisionItem(_ context.Context, itemID uuid.UUID) error {
	delete(m.items, itemID)
	return nil
}

func (m *mockDecisionRepo) DeleteUneditedItems(_ context.Context, meetingID uuid.UUID) error {
	for id, item := range m.items {
		if item.MeetingID == meetingID && !item.UserEdited {
			delete(m.items, id)
		}
	}
	return nil
}

func (m *mockDecisionRepo) GetUserEditedItemTexts(_ context.Context, meetingID uuid.UUID) ([]string, error) {
	var texts []string
	for _, item := range m.items {
		if item.MeetingID == meetingID && item.UserEdited {
			texts = append(texts, item.Text)
		}
	}
	return texts, nil
}

func (m *mockDecisionRepo) ReplaceUneditedItems(_ context.Context, meetingID uuid.UUID, newItems []models.DecisionItem) error {
	// Delete unedited
	for id, item := range m.items {
		if item.MeetingID == meetingID && !item.UserEdited {
			delete(m.items, id)
		}
	}
	// Collect edited texts
	editedTexts := make(map[string]bool)
	for _, item := range m.items {
		if item.MeetingID == meetingID && item.UserEdited {
			editedTexts[item.Text] = true
		}
	}
	// Insert non-duplicate items
	for _, item := range newItems {
		if !editedTexts[item.Text] {
			m.items[item.ID] = &item
		}
	}
	return nil
}

func (m *mockDecisionRepo) GetConfirmedItemsByChain(_ context.Context, chainID uuid.UUID) ([]models.DecisionItem, error) {
	return nil, nil
}

func (m *mockDecisionRepo) MarkUserEdited(_ context.Context, itemID uuid.UUID) error {
	if item, ok := m.items[itemID]; ok {
		item.UserEdited = true
	}
	return nil
}

func (m *mockDecisionRepo) UpdateChainIDForMeeting(_ context.Context, meetingID, chainID uuid.UUID) error {
	for _, item := range m.items {
		if item.MeetingID == meetingID {
			item.ChainID = &chainID
		}
	}
	return nil
}

// ==================== Complete Tests ====================

func TestCompleteItem_Success(t *testing.T) {
	t.Parallel()

	repo := newMockDecisionRepo()
	svc := &DecisionService{repo: repo}

	itemID := uuid.New()
	repo.items[itemID] = &models.DecisionItem{
		ID:     itemID,
		Status: "confirmed",
	}

	err := svc.CompleteItem(context.Background(), itemID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.items[itemID].Status != "completed" {
		t.Errorf("expected completed, got %s", repo.items[itemID].Status)
	}
	if !repo.items[itemID].UserEdited {
		t.Error("expected user_edited to be true after complete")
	}
}

func TestCompleteItem_OnlyConfirmedCanComplete(t *testing.T) {
	t.Parallel()

	repo := newMockDecisionRepo()
	svc := &DecisionService{repo: repo}

	itemID := uuid.New()
	repo.items[itemID] = &models.DecisionItem{
		ID:     itemID,
		Status: "dismissed",
	}

	err := svc.CompleteItem(context.Background(), itemID)
	if err == nil {
		t.Error("expected error: only confirmed items can be completed")
	}
}

// ==================== Dismiss Tests ====================

func TestDismissItem_Success(t *testing.T) {
	t.Parallel()

	repo := newMockDecisionRepo()
	svc := &DecisionService{repo: repo}

	itemID := uuid.New()
	repo.items[itemID] = &models.DecisionItem{
		ID:     itemID,
		Status: "confirmed",
	}

	err := svc.DismissItem(context.Background(), itemID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.items[itemID].Status != "dismissed" {
		t.Errorf("expected dismissed, got %s", repo.items[itemID].Status)
	}
	if !repo.items[itemID].UserEdited {
		t.Error("expected user_edited to be true after dismiss")
	}
}

// ==================== Delete Tests ====================

func TestDeleteItem_ConfirmedCanDelete(t *testing.T) {
	t.Parallel()

	repo := newMockDecisionRepo()
	svc := &DecisionService{repo: repo}

	itemID := uuid.New()
	repo.items[itemID] = &models.DecisionItem{
		ID:     itemID,
		Status: "confirmed",
	}

	err := svc.DeleteItem(context.Background(), itemID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, exists := repo.items[itemID]; exists {
		t.Error("expected item to be deleted")
	}
}

func TestDeleteItem_CompletedCantDelete(t *testing.T) {
	t.Parallel()

	repo := newMockDecisionRepo()
	svc := &DecisionService{repo: repo}

	itemID := uuid.New()
	repo.items[itemID] = &models.DecisionItem{
		ID:     itemID,
		Status: "completed",
	}

	err := svc.DeleteItem(context.Background(), itemID)
	if err == nil {
		t.Error("expected error: completed items can't be deleted")
	}
}

func TestDeleteItem_NotFound(t *testing.T) {
	t.Parallel()

	repo := newMockDecisionRepo()
	svc := &DecisionService{repo: repo}

	err := svc.DeleteItem(context.Background(), uuid.New())
	if err == nil {
		t.Error("expected error for non-existent item")
	}
}

// ==================== Update Tests ====================

func TestUpdateItem_Success(t *testing.T) {
	t.Parallel()

	repo := newMockDecisionRepo()
	svc := &DecisionService{repo: repo}

	itemID := uuid.New()
	owner := "Alice"
	repo.items[itemID] = &models.DecisionItem{
		ID:   itemID,
		Text: "Old text",
	}

	err := svc.UpdateItem(context.Background(), itemID, "New text", &owner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.items[itemID].Text != "New text" {
		t.Errorf("expected 'New text', got '%s'", repo.items[itemID].Text)
	}
	if *repo.items[itemID].OwnerName != "Alice" {
		t.Errorf("expected owner 'Alice', got '%s'", *repo.items[itemID].OwnerName)
	}
	if !repo.items[itemID].UserEdited {
		t.Error("expected user_edited to be true after update")
	}
}

func TestUpdateItem_EmptyText(t *testing.T) {
	t.Parallel()

	repo := newMockDecisionRepo()
	svc := &DecisionService{repo: repo}

	itemID := uuid.New()
	repo.items[itemID] = &models.DecisionItem{
		ID:   itemID,
		Text: "Old text",
	}

	err := svc.UpdateItem(context.Background(), itemID, "", nil)
	if err == nil {
		t.Error("expected error for empty text")
	}
}

// ==================== Status Transition Flow ====================

func TestFullStatusFlow_ConfirmedToCompleted(t *testing.T) {
	t.Parallel()

	repo := newMockDecisionRepo()
	svc := &DecisionService{repo: repo}

	itemID := uuid.New()
	repo.items[itemID] = &models.DecisionItem{
		ID:     itemID,
		Status: "confirmed",
	}

	// confirmed → completed
	if err := svc.CompleteItem(context.Background(), itemID); err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	if repo.items[itemID].Status != "completed" {
		t.Fatalf("expected completed")
	}

	// completed → can't delete
	if err := svc.DeleteItem(context.Background(), itemID); err == nil {
		t.Error("expected error: completed items can't be deleted")
	}
}

func TestFullStatusFlow_ConfirmedToDismissed(t *testing.T) {
	t.Parallel()

	repo := newMockDecisionRepo()
	svc := &DecisionService{repo: repo}

	itemID := uuid.New()
	repo.items[itemID] = &models.DecisionItem{
		ID:     itemID,
		Status: "confirmed",
	}

	// confirmed → dismissed
	if err := svc.DismissItem(context.Background(), itemID); err != nil {
		t.Fatalf("dismiss failed: %v", err)
	}
	if repo.items[itemID].Status != "dismissed" {
		t.Fatalf("expected dismissed")
	}
}

// ==================== Idempotency ====================

func TestDeleteUneditedItems_OnlyDeletesUnedited(t *testing.T) {
	t.Parallel()

	repo := newMockDecisionRepo()
	meetingID := uuid.New()

	unedited := uuid.New()
	edited := uuid.New()
	completed := uuid.New()

	repo.items[unedited] = &models.DecisionItem{ID: unedited, MeetingID: meetingID, Status: "confirmed", UserEdited: false}
	repo.items[edited] = &models.DecisionItem{ID: edited, MeetingID: meetingID, Status: "confirmed", UserEdited: true}
	repo.items[completed] = &models.DecisionItem{ID: completed, MeetingID: meetingID, Status: "completed", UserEdited: true}

	repo.DeleteUneditedItems(context.Background(), meetingID)

	if _, exists := repo.items[unedited]; exists {
		t.Error("expected unedited item to be deleted")
	}
	if _, exists := repo.items[edited]; !exists {
		t.Error("user-edited item should NOT be deleted")
	}
	if _, exists := repo.items[completed]; !exists {
		t.Error("completed item should NOT be deleted")
	}
}

func TestChainIDCascade(t *testing.T) {
	t.Parallel()

	repo := newMockDecisionRepo()
	meetingID := uuid.New()
	newChainID := uuid.New()

	item1 := uuid.New()
	item2 := uuid.New()

	repo.items[item1] = &models.DecisionItem{ID: item1, MeetingID: meetingID, Status: "confirmed"}
	repo.items[item2] = &models.DecisionItem{ID: item2, MeetingID: meetingID, Status: "confirmed"}

	repo.UpdateChainIDForMeeting(context.Background(), meetingID, newChainID)

	if repo.items[item1].ChainID == nil || *repo.items[item1].ChainID != newChainID {
		t.Error("expected item1 chain_id to be updated")
	}
	if repo.items[item2].ChainID == nil || *repo.items[item2].ChainID != newChainID {
		t.Error("expected item2 chain_id to be updated")
	}
}
