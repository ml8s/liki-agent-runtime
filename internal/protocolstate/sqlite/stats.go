package sqlite

import (
	"context"
	"fmt"
)

// StorageStats is a bounded snapshot of durable A2A protocol growth.
type StorageStats struct {
	TaskCounts  map[string]int64
	PushConfigs int64
}

// Stats returns approximate record counts for capacity monitoring. It reads
// outside request paths; callers should budget the scrape interval.
func (db *DB) Stats(ctx context.Context) (StorageStats, error) {
	if db == nil || db.sql == nil {
		return StorageStats{}, fmt.Errorf("protocol state database is closed")
	}
	stats := StorageStats{TaskCounts: make(map[string]int64)}
	rows, err := db.sql.QueryContext(ctx, `
		SELECT state, count(*)
		FROM a2a_tasks
		GROUP BY state`)
	if err != nil {
		return StorageStats{}, fmt.Errorf("count protocol tasks: %w", err)
	}
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			_ = rows.Close()
			return StorageStats{}, fmt.Errorf("scan protocol task counts: %w", err)
		}
		stats.TaskCounts[state] = count
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return StorageStats{}, fmt.Errorf("iterate protocol task counts: %w", err)
	}
	if err := rows.Close(); err != nil {
		return StorageStats{}, fmt.Errorf("close protocol task counts: %w", err)
	}

	err = db.sql.QueryRowContext(ctx, `SELECT count(*) FROM a2a_push_configs`).
		Scan(&stats.PushConfigs)
	if err != nil {
		return StorageStats{}, fmt.Errorf("count A2A push configs: %w", err)
	}
	return stats, nil
}
