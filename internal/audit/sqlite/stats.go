package sqlite

import (
	"context"
	"fmt"
)

// StorageStats is a bounded snapshot of append-only audit growth.
type StorageStats struct {
	Events int64
}

// Stats returns an approximate event count for capacity monitoring. Audit
// events are append-only; the trigger enforces the immutability invariant.
func (db *DB) Stats(ctx context.Context) (StorageStats, error) {
	if db == nil || db.gorm == nil {
		return StorageStats{}, fmt.Errorf("audit database is closed")
	}
	var stats StorageStats
	err := db.gorm.WithContext(ctx).
		Table("agent_audit_events").
		Count(&stats.Events).Error
	if err != nil {
		return StorageStats{}, fmt.Errorf("count audit events: %w", err)
	}
	return stats, nil
}
