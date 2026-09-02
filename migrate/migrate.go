// Package migrate applies ordered PostgreSQL migrations from engine and application filesystems.
// Versions 1-999 are reserved for engine infrastructure; application migrations start at 1000.
package migrate

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// engineMigrations contains the versioned schema owned by devengine.
//
//go:embed migrations/*.up.sql
var engineMigrations embed.FS

// EngineSources returns the official migrations for devengine-owned tables.
// Pass these before application sources to Runner.
func EngineSources() []Source {
	return []Source{{Kind: EngineSource, FS: engineMigrations, Dir: "migrations"}}
}

const (
	EngineMinVersion           = 1
	EngineMaxVersion           = 999
	AppMinVersion              = 1000
	defaultMetadataTable       = "_devengine_migrations"
	migrationLockID      int64 = 0x64657665656e67 // "deveeng"
)

var migrationPattern = regexp.MustCompile(`^(\d+)_([a-zA-Z0-9_-]+)\.up\.sql$`)

// SourceKind identifies who owns a set of migrations.
type SourceKind string

const (
	EngineSource SourceKind = "engine"
	AppSource    SourceKind = "app"
)

// Source pairs a SourceKind with the filesystem and sub-directory that holds
// the *.up.sql files.
type Source struct {
	Kind SourceKind
	FS   fs.FS
	Dir  string
}

// MigrationStatus describes a single migration as seen by Status().
type MigrationStatus struct {
	Version   int
	Name      string
	Source    SourceKind
	Checksum  string
	AppliedAt *time.Time // nil when pending
	Drift     bool       // true if the stored checksum differs from the file's checksum
}

// Runner executes migrations against a pgxpool.Pool.
type Runner struct {
	// Pool is the database connection pool.
	Pool *pgxpool.Pool
	// Sources lists the filesystems to scan for migration files.
	Sources []Source
	// MetadataTable overrides the default "_devengine_migrations" table name.
	// This lets multiple engines share the same PostgreSQL instance.
	MetadataTable string
}

type migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
	Source   SourceKind
}

func (r Runner) metadataTable() string {
	t := defaultMetadataTable
	if trimmed := strings.TrimSpace(r.MetadataTable); trimmed != "" {
		t = trimmed
	}
	return pgx.Identifier{t}.Sanitize()
}

// Apply acquires a PostgreSQL advisory lock, creates the metadata table if
// needed, then applies all pending migrations in version order. Each migration
// runs in its own transaction so that a failure at version N does not roll
// back previously applied migrations.
func (r Runner) Apply(ctx context.Context) error {
	if r.Pool == nil {
		return errors.New("migrate: nil pool")
	}

	// Pin a single connection so the advisory lock stays alive for the entire run.
	conn, err := r.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("migrate: acquire advisory lock: %w", err)
	}
	defer func() {
		unlockCtx := context.Background()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	table := r.metadataTable()
	createSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		version    BIGINT      PRIMARY KEY,
		source     TEXT        NOT NULL,
		name       TEXT        NOT NULL,
		checksum   TEXT        NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`, table)
	if _, err := conn.Exec(ctx, createSQL); err != nil {
		return fmt.Errorf("migrate: create metadata table: %w", err)
	}

	migrations, err := loadSources(r.Sources)
	if err != nil {
		return err
	}
	for _, item := range migrations {
		if err := r.applyOne(ctx, conn.Conn(), item, table); err != nil {
			return err
		}
	}
	return nil
}

// Status returns the status of every migration known to the runner, including
// whether it has been applied and when.
func (r Runner) Status(ctx context.Context) ([]MigrationStatus, error) {
	if r.Pool == nil {
		return nil, errors.New("migrate: nil pool")
	}

	migrations, err := loadSources(r.Sources)
	if err != nil {
		return nil, err
	}

	table := r.metadataTable()
	rows, err := r.Pool.Query(ctx,
		fmt.Sprintf(`SELECT version, checksum, applied_at FROM %s`, table))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" { // undefined_table
			return r.pendingStatus(migrations), nil
		}
		return nil, fmt.Errorf("migrate: query status: %w", err)
	}
	defer rows.Close()

	applied := map[int]struct {
		checksum  string
		appliedAt time.Time
	}{}
	for rows.Next() {
		var (
			version   int
			checksum  string
			appliedAt time.Time
		)
		if err := rows.Scan(&version, &checksum, &appliedAt); err != nil {
			return nil, fmt.Errorf("migrate: scan status row: %w", err)
		}
		applied[version] = struct {
			checksum  string
			appliedAt time.Time
		}{checksum, appliedAt}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrate: iterate status: %w", err)
	}

	out := make([]MigrationStatus, 0, len(migrations))
	for _, m := range migrations {
		ms := MigrationStatus{
			Version:  m.Version,
			Name:     m.Name,
			Source:   m.Source,
			Checksum: m.Checksum,
		}
		if a, ok := applied[m.Version]; ok {
			t := a.appliedAt
			ms.AppliedAt = &t
			if a.checksum != m.Checksum {
				ms.Drift = true
			}
		}
		out = append(out, ms)
	}
	return out, nil
}

func (r Runner) pendingStatus(migrations []migration) []MigrationStatus {
	out := make([]MigrationStatus, 0, len(migrations))
	for _, m := range migrations {
		out = append(out, MigrationStatus{
			Version:  m.Version,
			Name:     m.Name,
			Source:   m.Source,
			Checksum: m.Checksum,
		})
	}
	return out
}

func (r Runner) applyOne(ctx context.Context, conn *pgx.Conn, item migration, table string) error {
	var checksum string
	err := conn.QueryRow(ctx,
		fmt.Sprintf(`SELECT checksum FROM %s WHERE version = $1`, table), item.Version,
	).Scan(&checksum)

	switch {
	case err == nil:
		if checksum != item.Checksum {
			return fmt.Errorf("migrate: version %d (%s) checksum changed: stored %s, current %s",
				item.Version, item.Name, checksum, item.Checksum)
		}
		return nil // already applied and checksum matches
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("migrate: inspect version %d: %w", item.Version, err)
	}

	// Not yet applied — run in its own transaction.
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migrate: begin version %d: %w", item.Version, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, item.SQL); err != nil {
		return fmt.Errorf("migrate: apply version %d (%s): %w", item.Version, item.Name, err)
	}
	if _, err := tx.Exec(ctx,
		fmt.Sprintf(`INSERT INTO %s (version, source, name, checksum) VALUES ($1, $2, $3, $4)`, table),
		item.Version, string(item.Source), item.Name, item.Checksum,
	); err != nil {
		return fmt.Errorf("migrate: record version %d: %w", item.Version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migrate: commit version %d: %w", item.Version, err)
	}
	return nil
}

func loadSources(sources []Source) ([]migration, error) {
	var all []migration
	seen := map[int]string{}
	for _, source := range sources {
		if source.FS == nil {
			continue
		}
		dir := strings.TrimSpace(source.Dir)
		if dir == "" {
			dir = "."
		}
		entries, err := fs.ReadDir(source.FS, dir)
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s source: %w", source.Kind, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			match := migrationPattern.FindStringSubmatch(entry.Name())
			if match == nil {
				continue
			}
			version64, err := strconv.ParseInt(match[1], 10, 32)
			if err != nil {
				return nil, fmt.Errorf("migrate: invalid version in %s: %w", entry.Name(), err)
			}
			version := int(version64)
			if err := validateVersion(source.Kind, version); err != nil {
				return nil, fmt.Errorf("migrate: %s: %w", entry.Name(), err)
			}
			if previous, exists := seen[version]; exists {
				return nil, fmt.Errorf("migrate: duplicate version %d in %s and %s", version, previous, entry.Name())
			}
			body, err := fs.ReadFile(source.FS, path.Join(dir, entry.Name()))
			if err != nil {
				return nil, fmt.Errorf("migrate: read %s: %w", entry.Name(), err)
			}
			sum := sha256.Sum256(body)
			seen[version] = entry.Name()
			all = append(all, migration{
				Version:  version,
				Name:     match[2],
				SQL:      string(body),
				Checksum: hex.EncodeToString(sum[:]),
				Source:   source.Kind,
			})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Version < all[j].Version })
	return all, nil
}

func validateVersion(kind SourceKind, version int) error {
	switch kind {
	case EngineSource:
		if version < EngineMinVersion || version > EngineMaxVersion {
			return fmt.Errorf("engine migration version must be between %d and %d", EngineMinVersion, EngineMaxVersion)
		}
	case AppSource:
		if version < AppMinVersion {
			return fmt.Errorf("application migration version must be >= %d", AppMinVersion)
		}
	default:
		return fmt.Errorf("unknown source kind %q", kind)
	}
	return nil
}
