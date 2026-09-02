// Package schema provides PostgreSQL schema introspection, versioned snapshots,
// and drift detection for CI pipelines.
//
// Exit code conventions (for CLI use):
//
//	0 — schema is clean (no drift)
//	1 — error (introspection or I/O failure)
//	2 — drift detected between snapshot and live schema
package schema

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Snapshot is a point-in-time representation of a PostgreSQL schema.
type Snapshot struct {
	CapturedAt time.Time           `json:"captured_at"`
	Tables     map[string]*Table   `json:"tables"`
	Enums      map[string][]string `json:"enums,omitempty"`
	Sequences  []string            `json:"sequences,omitempty"`
}

// Table describes a single PostgreSQL table.
type Table struct {
	Name        string       `json:"name"`
	Columns     []Column     `json:"columns"`
	Indexes     []Index      `json:"indexes,omitempty"`
	Constraints []Constraint `json:"constraints,omitempty"`
}

// Column describes a table column.
type Column struct {
	Name       string  `json:"name"`
	DataType   string  `json:"data_type"`
	IsNullable bool    `json:"nullable"`
	Default    *string `json:"default,omitempty"`
	Position   int     `json:"position"`
}

// Index describes a table index.
type Index struct {
	Name       string   `json:"name"`
	Unique     bool     `json:"unique"`
	Primary    bool     `json:"primary"`
	Columns    []string `json:"columns"`
	Definition string   `json:"definition"`
}

// Constraint describes a table constraint (PK, FK, unique, check).
type Constraint struct {
	Name       string `json:"name"`
	Type       string `json:"type"`       // p=primary key, f=foreign key, u=unique, c=check
	Definition string `json:"definition"` // Canonical SQL definition
}

// ignoreTable reports whether a table should be excluded from snapshots.
// We skip the devengine internal migration tracking table.
func ignoreTable(name string) bool {
	return name == "_devengine_migrations" || strings.HasPrefix(name, "pg_") || strings.HasPrefix(name, "information_schema")
}

// Capture performs a full introspection of the public schema in the given pool
// and returns a Snapshot. It does not execute any DDL or DML.
func Capture(ctx context.Context, pool *pgxpool.Pool) (*Snapshot, error) {
	snap := &Snapshot{
		CapturedAt: time.Now().UTC(),
		Tables:     make(map[string]*Table),
	}

	if err := captureColumns(ctx, pool, snap); err != nil {
		return nil, fmt.Errorf("schema: capture columns: %w", err)
	}
	if err := captureIndexes(ctx, pool, snap); err != nil {
		return nil, fmt.Errorf("schema: capture indexes: %w", err)
	}
	if err := captureConstraints(ctx, pool, snap); err != nil {
		return nil, fmt.Errorf("schema: capture constraints: %w", err)
	}
	if err := captureEnums(ctx, pool, snap); err != nil {
		return nil, fmt.Errorf("schema: capture enums: %w", err)
	}
	if err := captureSequences(ctx, pool, snap); err != nil {
		return nil, fmt.Errorf("schema: capture sequences: %w", err)
	}
	return snap, nil
}

func captureColumns(ctx context.Context, pool *pgxpool.Pool, snap *Snapshot) error {
	rows, err := pool.Query(ctx, `
		SELECT
			c.relname AS table_name,
			a.attname AS column_name,
			format_type(a.atttypid, a.atttypmod) AS data_type,
			NOT a.attnotnull AS nullable,
			pg_get_expr(ad.adbin, ad.adrelid) AS column_default,
			a.attnum AS ordinal_position
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef ad ON ad.adrelid = c.oid AND ad.adnum = a.attnum
		WHERE n.nspname = 'public'
		  AND c.relkind = 'r'
		  AND a.attnum > 0
		  AND NOT a.attisdropped
		ORDER BY c.relname, a.attnum
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			tableName, colName, dataType string
			nullable                     bool
			colDefault                   *string
			position                     int
		)
		if err := rows.Scan(&tableName, &colName, &dataType, &nullable, &colDefault, &position); err != nil {
			return err
		}
		if ignoreTable(tableName) {
			continue
		}
		tbl, ok := snap.Tables[tableName]
		if !ok {
			tbl = &Table{Name: tableName}
			snap.Tables[tableName] = tbl
		}
		tbl.Columns = append(tbl.Columns, Column{
			Name:       colName,
			DataType:   dataType,
			IsNullable: nullable,
			Default:    colDefault,
			Position:   position,
		})
	}
	return rows.Err()
}

func captureIndexes(ctx context.Context, pool *pgxpool.Pool, snap *Snapshot) error {
	rows, err := pool.Query(ctx, `
		SELECT
			t.relname AS table_name,
			i.relname AS index_name,
			ix.indisunique,
			ix.indisprimary,
			pg_get_indexdef(ix.indexrelid) AS definition,
			ARRAY(
				SELECT a.attname
				FROM pg_attribute a
				WHERE a.attrelid = t.oid
				  AND a.attnum = ANY(ix.indkey)
				ORDER BY array_position(ix.indkey, a.attnum)
			) AS columns
		FROM pg_class t
		JOIN pg_index ix ON ix.indrelid = t.oid
		JOIN pg_class i  ON i.oid = ix.indexrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = 'public'
		  AND t.relkind = 'r'
		ORDER BY t.relname, i.relname
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			tableName, indexName, definition string
			unique, primary                  bool
			columns                          []string
		)
		if err := rows.Scan(&tableName, &indexName, &unique, &primary, &definition, &columns); err != nil {
			return err
		}
		if ignoreTable(tableName) {
			continue
		}
		tbl, ok := snap.Tables[tableName]
		if !ok {
			tbl = &Table{Name: tableName}
			snap.Tables[tableName] = tbl
		}
		tbl.Indexes = append(tbl.Indexes, Index{
			Name:       indexName,
			Unique:     unique,
			Primary:    primary,
			Columns:    columns,
			Definition: definition,
		})
	}
	return rows.Err()
}

func captureConstraints(ctx context.Context, pool *pgxpool.Pool, snap *Snapshot) error {
	rows, err := pool.Query(ctx, `
		SELECT
			c.relname AS table_name,
			con.conname AS constraint_name,
			con.contype::text AS constraint_type,
			pg_get_constraintdef(con.oid) AS definition
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND con.contype IN ('p', 'f', 'u', 'c')
		ORDER BY c.relname, con.conname
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var tableName, constraintName, constraintType, definition string
		if err := rows.Scan(&tableName, &constraintName, &constraintType, &definition); err != nil {
			return err
		}
		if ignoreTable(tableName) {
			continue
		}
		tbl, ok := snap.Tables[tableName]
		if !ok {
			tbl = &Table{Name: tableName}
			snap.Tables[tableName] = tbl
		}
		tbl.Constraints = append(tbl.Constraints, Constraint{
			Name:       constraintName,
			Type:       constraintType,
			Definition: definition,
		})
	}
	return rows.Err()
}

func captureEnums(ctx context.Context, pool *pgxpool.Pool, snap *Snapshot) error {
	rows, err := pool.Query(ctx, `
		SELECT t.typname, e.enumlabel
		FROM pg_type t
		JOIN pg_enum e ON e.enumtypid = t.oid
		JOIN pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname = 'public'
		ORDER BY t.typname, e.enumsortorder
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	enums := make(map[string][]string)
	for rows.Next() {
		var typName, label string
		if err := rows.Scan(&typName, &label); err != nil {
			return err
		}
		enums[typName] = append(enums[typName], label)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(enums) > 0 {
		snap.Enums = enums
	}
	return nil
}

func captureSequences(ctx context.Context, pool *pgxpool.Pool, snap *Snapshot) error {
	rows, err := pool.Query(ctx, `
		SELECT sequence_name
		FROM information_schema.sequences
		WHERE sequence_schema = 'public'
		ORDER BY sequence_name
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var seqs []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		seqs = append(seqs, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	snap.Sequences = seqs
	return nil
}

// MarshalJSON serialises a Snapshot to indented JSON.
func (s *Snapshot) MarshalToJSON() ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}

// UnmarshalSnapshot parses a JSON-encoded Snapshot.
func UnmarshalSnapshot(data []byte) (*Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("schema: unmarshal snapshot: %w", err)
	}
	return &s, nil
}

// DriftKind classifies a drift entry.
type DriftKind string

const (
	DriftTableAdded        DriftKind = "table_added"
	DriftTableRemoved      DriftKind = "table_removed"
	DriftColumnAdded       DriftKind = "column_added"
	DriftColumnRemoved     DriftKind = "column_removed"
	DriftColumnChanged     DriftKind = "column_changed"
	DriftIndexAdded        DriftKind = "index_added"
	DriftIndexRemoved      DriftKind = "index_removed"
	DriftIndexChanged      DriftKind = "index_changed"
	DriftConstraintAdded   DriftKind = "constraint_added"
	DriftConstraintRemoved DriftKind = "constraint_removed"
	DriftConstraintChanged DriftKind = "constraint_changed"
	DriftSequenceAdded     DriftKind = "sequence_added"
	DriftSequenceRemoved   DriftKind = "sequence_removed"
	DriftEnumChanged       DriftKind = "enum_changed"
)

// DriftEntry describes a single schema difference.
type DriftEntry struct {
	Kind   DriftKind `json:"kind"`
	Table  string    `json:"table,omitempty"`
	Object string    `json:"object,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// DriftResult is the output of Diff or Report.
type DriftResult struct {
	HasDrift bool         `json:"has_drift"`
	Entries  []DriftEntry `json:"entries"`
}

// Diff compares baseline (snapshot from file) against live (current DB snapshot).
// It returns a DriftResult. HasDrift is true when changes are detected.
func Diff(baseline, live *Snapshot) DriftResult {
	return compare(baseline, live)
}

// Report compares two snapshots (e.g. from two different captures).
func Report(a, b *Snapshot) DriftResult {
	return compare(a, b)
}

func compare(base, live *Snapshot) DriftResult {
	var entries []DriftEntry

	// Tables added in live but not in base.
	for name := range live.Tables {
		if _, ok := base.Tables[name]; !ok {
			entries = append(entries, DriftEntry{Kind: DriftTableAdded, Table: name})
		}
	}
	// Tables removed from live.
	for name := range base.Tables {
		if _, ok := live.Tables[name]; !ok {
			entries = append(entries, DriftEntry{Kind: DriftTableRemoved, Table: name})
			continue
		}
		// Compare columns.
		entries = append(entries, diffColumns(name, base.Tables[name], live.Tables[name])...)
		// Compare indexes.
		entries = append(entries, diffIndexes(name, base.Tables[name], live.Tables[name])...)
		// Compare constraints.
		entries = append(entries, diffConstraints(name, base.Tables[name], live.Tables[name])...)
	}
	// Sequences.
	entries = append(entries, diffSequences(base.Sequences, live.Sequences)...)
	// Enums.
	entries = append(entries, diffEnums(base.Enums, live.Enums)...)

	sort.Slice(entries, func(i, j int) bool {
		return fmt.Sprintf("%s/%s/%s", entries[i].Table, entries[i].Kind, entries[i].Object) <
			fmt.Sprintf("%s/%s/%s", entries[j].Table, entries[j].Kind, entries[j].Object)
	})
	return DriftResult{HasDrift: len(entries) > 0, Entries: entries}
}

func diffColumns(tableName string, base, live *Table) []DriftEntry {
	var entries []DriftEntry
	baseIdx := indexColumns(base.Columns)
	liveIdx := indexColumns(live.Columns)

	for name, col := range liveIdx {
		if _, ok := baseIdx[name]; !ok {
			entries = append(entries, DriftEntry{Kind: DriftColumnAdded, Table: tableName, Object: name, Detail: col.DataType})
		}
	}
	for name, col := range baseIdx {
		liveCol, ok := liveIdx[name]
		if !ok {
			entries = append(entries, DriftEntry{Kind: DriftColumnRemoved, Table: tableName, Object: name})
			continue
		}
		defaultBase := ""
		if col.Default != nil {
			defaultBase = *col.Default
		}
		defaultLive := ""
		if liveCol.Default != nil {
			defaultLive = *liveCol.Default
		}
		if col.DataType != liveCol.DataType || col.IsNullable != liveCol.IsNullable || defaultBase != defaultLive {
			entries = append(entries, DriftEntry{
				Kind:   DriftColumnChanged,
				Table:  tableName,
				Object: name,
				Detail: fmt.Sprintf("type: %s→%s nullable: %v→%v default: %v→%v", col.DataType, liveCol.DataType, col.IsNullable, liveCol.IsNullable, defaultBase, defaultLive),
			})
		}
	}
	return entries
}

func diffIndexes(tableName string, base, live *Table) []DriftEntry {
	var entries []DriftEntry
	baseIdx := make(map[string]Index)
	for _, i := range base.Indexes {
		baseIdx[i.Name] = i
	}
	liveIdx := make(map[string]Index)
	for _, i := range live.Indexes {
		liveIdx[i.Name] = i
	}
	for name := range liveIdx {
		if _, ok := baseIdx[name]; !ok {
			entries = append(entries, DriftEntry{Kind: DriftIndexAdded, Table: tableName, Object: name})
		}
	}
	for name, col := range baseIdx {
		liveCol, ok := liveIdx[name]
		if !ok {
			entries = append(entries, DriftEntry{Kind: DriftIndexRemoved, Table: tableName, Object: name})
			continue
		}
		if col.Definition != liveCol.Definition {
			entries = append(entries, DriftEntry{
				Kind:   DriftIndexChanged,
				Table:  tableName,
				Object: name,
				Detail: fmt.Sprintf("def: %s→%s", col.Definition, liveCol.Definition),
			})
		}
	}
	return entries
}

func diffConstraints(tableName string, base, live *Table) []DriftEntry {
	var entries []DriftEntry
	baseCon := make(map[string]Constraint)
	for _, c := range base.Constraints {
		baseCon[c.Name] = c
	}
	liveCon := make(map[string]Constraint)
	for _, c := range live.Constraints {
		liveCon[c.Name] = c
	}
	for name := range liveCon {
		if _, ok := baseCon[name]; !ok {
			entries = append(entries, DriftEntry{Kind: DriftConstraintAdded, Table: tableName, Object: name})
		}
	}
	for name, c := range baseCon {
		liveC, ok := liveCon[name]
		if !ok {
			entries = append(entries, DriftEntry{Kind: DriftConstraintRemoved, Table: tableName, Object: name})
			continue
		}
		if c.Definition != liveC.Definition {
			entries = append(entries, DriftEntry{
				Kind:   DriftConstraintChanged,
				Table:  tableName,
				Object: name,
				Detail: fmt.Sprintf("def: %s→%s", c.Definition, liveC.Definition),
			})
		}
	}
	return entries
}

func diffSequences(base, live []string) []DriftEntry {
	var entries []DriftEntry
	baseMap := make(map[string]bool)
	for _, s := range base {
		baseMap[s] = true
	}
	liveMap := make(map[string]bool)
	for _, s := range live {
		liveMap[s] = true
	}
	for s := range liveMap {
		if !baseMap[s] {
			entries = append(entries, DriftEntry{Kind: DriftSequenceAdded, Object: s})
		}
	}
	for s := range baseMap {
		if !liveMap[s] {
			entries = append(entries, DriftEntry{Kind: DriftSequenceRemoved, Object: s})
		}
	}
	return entries
}

func diffEnums(base, live map[string][]string) []DriftEntry {
	var entries []DriftEntry
	for name, liveVals := range live {
		baseVals, ok := base[name]
		if !ok || join(baseVals) != join(liveVals) {
			entries = append(entries, DriftEntry{
				Kind:   DriftEnumChanged,
				Object: name,
				Detail: fmt.Sprintf("base=%v live=%v", baseVals, liveVals),
			})
		}
	}
	for name := range base {
		if _, ok := live[name]; !ok {
			entries = append(entries, DriftEntry{Kind: DriftEnumChanged, Object: name, Detail: "removed"})
		}
	}
	return entries
}

func indexColumns(cols []Column) map[string]Column {
	m := make(map[string]Column, len(cols))
	for _, c := range cols {
		m[c.Name] = c
	}
	return m
}

func join(ss []string) string { return strings.Join(ss, ",") }
