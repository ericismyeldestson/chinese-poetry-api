package database

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// InsertSourceRejections atomically stores and verifies the global,
// language-independent source rejection ledger. A locator conflict is accepted
// only when every stored field is identical; otherwise import fails closed.
func (db *DB) InsertSourceRejections(rejections []SourceRejection) error {
	if len(rejections) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(rejections))
	for i, rejection := range rejections {
		if err := validateSourceRejection(rejection); err != nil {
			return fmt.Errorf("source rejection %d: %w", i, err)
		}
		if _, exists := seen[rejection.LocatorID]; exists {
			return fmt.Errorf("source rejection locator %q occurs more than once", rejection.LocatorID)
		}
		seen[rejection.LocatorID] = struct{}{}
	}

	return db.Transaction(func(tx *gorm.DB) error {
		rows := make([]SourceRejection, len(rejections))
		copy(rows, rejections)
		for i := range rows {
			rows[i].ID = 0
		}
		if err := tx.Table("source_rejections").Clauses(clause.OnConflict{DoNothing: true}).
			CreateInBatches(rows, 500).Error; err != nil {
			return fmt.Errorf("failed to insert source rejections: %w", err)
		}

		const queryChunkSize = 400
		actualByLocator := make(map[string]SourceRejection, len(rejections))
		for i := 0; i < len(rejections); i += queryChunkSize {
			end := min(i+queryChunkSize, len(rejections))
			locators := make([]string, 0, end-i)
			for _, rejection := range rejections[i:end] {
				locators = append(locators, rejection.LocatorID)
			}
			var actual []SourceRejection
			if err := tx.Table("source_rejections").Where("locator_id IN ?", locators).Find(&actual).Error; err != nil {
				return fmt.Errorf("failed to read back source rejections: %w", err)
			}
			for _, rejection := range actual {
				actualByLocator[rejection.LocatorID] = rejection
			}
		}

		missing := 0
		for _, expected := range rejections {
			actual, exists := actualByLocator[expected.LocatorID]
			if !exists {
				missing++
				continue
			}
			if actual.SourceID != expected.SourceID || actual.DatasetKey != expected.DatasetKey ||
				actual.SourcePath != expected.SourcePath || actual.SourceRecordIndex != expected.SourceRecordIndex ||
				actual.Stage != expected.Stage || actual.Reason != expected.Reason {
				return fmt.Errorf("source rejection locator collision for %q: stored row differs", expected.LocatorID)
			}
		}
		if missing > 0 {
			return fmt.Errorf("failed to persist %d of %d source rejections", missing, len(rejections))
		}
		return nil
	})
}

func validateSourceRejection(rejection SourceRejection) error {
	expected, err := NewSourceRejection(
		rejection.SourceID, rejection.DatasetKey, rejection.SourcePath,
		rejection.SourceRecordIndex, rejection.Stage, rejection.Reason,
	)
	if err != nil {
		return err
	}
	if expected.LocatorID != rejection.LocatorID {
		return fmt.Errorf("rejection locator %q does not match its fields", rejection.LocatorID)
	}
	return nil
}
