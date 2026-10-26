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

	auditInsertPlanOnce sync.Once
	auditInsertPlan     auditEventInsertPlan
)

// auditEventInsertPlan is immutable after one-time construction. The batch
// writer reads derived metadata and pre-rendered SQL; it never populates a
// shared runtime cache.
type auditEventInsertPlan struct {
	fields     []*schema.Field
	columns    []string
	table      string
	statements map[int]string
}

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

func auditEventInsertPlanValue() auditEventInsertPlan {
	auditInsertPlanOnce.Do(func() {
		schemaValue := auditEventSchema()
		fields := make([]*schema.Field, 0, len(schemaValue.Fields))
		columns := make([]string, 0, len(schemaValue.Fields))
		for _, field := range schemaValue.Fields {
			if field.DBName == "" || !field.Creatable {
				continue
			}
			fields = append(fields, field)
			columns = append(columns, field.DBName)
		}
		statements := make(map[int]string, recorderMaxBatch)
		for count := 1; count <= recorderMaxBatch; count++ {
			statements[count] = auditBatchSQL(schemaValue.Table, columns, count)
		}
		auditInsertPlan = auditEventInsertPlan{
			fields:     fields,
			columns:    columns,
			table:      schemaValue.Table,
			statements: statements,
		}
	})
	return auditInsertPlan
}

// auditBatchPlaceholders returns the multi-row INSERT statement for a batch
// size. Common recorder batch sizes are immutable pre-rendered values; a
// defensive fallback builds unusual sizes without mutating shared state.
func auditBatchPlaceholders(count int) string {
	plan := auditEventInsertPlanValue()
	if sql, ok := plan.statements[count]; ok {
		return sql
	}
	return auditBatchSQL(plan.table, plan.columns, count)
}

func auditBatchSQL(table string, columns []string, count int) string {
	if count <= 0 || len(columns) == 0 {
		return ""
	}
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ")"
	return fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES %s",
		table,
		strings.Join(columns, ","),
		strings.TrimSuffix(strings.Repeat(row+",", count), ","),
	)
}
