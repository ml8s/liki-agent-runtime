package sqlite

import (
	"fmt"
	"strings"
	"sync"

	"gorm.io/gorm/schema"
)

// The batch INSERT text is derived from the GORM model schema, never from a
// hand-written column list, so a new model field cannot silently miss the
// grouped append path.
var (
	auditEventSchemaOnce  sync.Once
	auditEventSchemaValue *schema.Schema
	auditEventSchemaErr   error

	batchSQLMu             sync.Mutex
	batchSQLCache          = map[int]string{}
	auditInsertFieldsValue []*schema.Field
)

func auditEventSchema() *schema.Schema {
	auditEventSchemaOnce.Do(func() {
		cacheStore := &sync.Map{}
		auditEventSchemaValue, auditEventSchemaErr = schema.Parse(
			auditEventModel{},
			cacheStore,
			schema.NamingStrategy{},
		)
	})
	if auditEventSchemaErr != nil {
		// schema.Parse of a static exported struct cannot fail; failing loudly
		// beats silently bypassing the grouped path.
		panic(fmt.Sprintf("parse audit event schema: %v", auditEventSchemaErr))
	}
	return auditEventSchemaValue
}

// auditEventInsertFields returns the model fields GORM would write, in struct
// order, so the batch statement and the per-row Create path stay identical.
func auditEventInsertFields() []*schema.Field {
	if auditInsertFieldsValue == nil {
		for _, field := range auditEventSchema().Fields {
			if field.DBName != "" && field.Creatable {
				auditInsertFieldsValue = append(auditInsertFieldsValue, field)
			}
		}
	}
	return auditInsertFieldsValue
}

// auditBatchPlaceholders returns the multi-row INSERT statement for a batch
// size. Statements are cached per size: GORM's prepared-statement cache keys
// by SQL text, so each size is parsed by SQLite once per process.
func auditBatchPlaceholders(count int) string {
	batchSQLMu.Lock()
	defer batchSQLMu.Unlock()
	if sql, ok := batchSQLCache[count]; ok {
		return sql
	}
	fields := auditEventInsertFields()
	columns := make([]string, 0, len(fields))
	for _, field := range fields {
		columns = append(columns, field.DBName)
	}
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ")"
	sql := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES %s",
		auditEventSchema().Table,
		strings.Join(columns, ","),
		strings.TrimSuffix(strings.Repeat(row+",", count), ","),
	)
	batchSQLCache[count] = sql
	return sql
}
