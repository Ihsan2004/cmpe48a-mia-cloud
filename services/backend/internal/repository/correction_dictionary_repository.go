package repository

import (
	"context"
	"fmt"
)

// CorrectionTerm represents a single entry in the correction dictionary.
type CorrectionTerm struct {
	WrongForm   string
	CorrectForm string
	Description string
}

type CorrectionDictionaryRepository interface {
	GetActiveTerms(ctx context.Context) ([]CorrectionTerm, error)
}

type correctionDictionaryRepository struct{}

func NewCorrectionDictionaryRepository() CorrectionDictionaryRepository {
	return &correctionDictionaryRepository{}
}

func (r *correctionDictionaryRepository) GetActiveTerms(ctx context.Context) ([]CorrectionTerm, error) {
	query := `
		SELECT wrong_form, correct_form, COALESCE(description, '')
		FROM correction_dictionary
		WHERE is_active = true
		ORDER BY correct_form ASC
	`

	rows, err := DB.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query correction dictionary: %w", err)
	}
	defer rows.Close()

	var terms []CorrectionTerm
	for rows.Next() {
		var t CorrectionTerm
		if err := rows.Scan(&t.WrongForm, &t.CorrectForm, &t.Description); err != nil {
			return nil, fmt.Errorf("failed to scan correction term: %w", err)
		}
		terms = append(terms, t)
	}

	return terms, nil
}
