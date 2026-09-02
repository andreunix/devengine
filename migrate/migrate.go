// Package migrate applies ordered PostgreSQL migrations from engine and application filesystems.
// Versions 1-999 are reserved for engine infrastructure; application migrations start at 1000.
package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	EngineMinVersion       = 1
	EngineMaxVersion       = 999
	AppMinVersion          = 1000
	migrationLockID  int64 = 0x5445434e4f // "TECNO"
)

var migrationPattern = regexp.MustCompile(`^(\d+)_([a-zA-Z0-9_-]+)\.up\.sql$`)

type SourceKind string

const (
	EngineSource SourceKind = "engine"
	AppSource    SourceKind = "app"
)

type Source struct {
	Kind SourceKind
	FS   fs.FS
	Dir  string
}

type Runner struct {
	DB      *sql.DB
	Sources []Source
}

type migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
	Source   SourceKind
}

func (r Runner) Apply(ctx context.Context) error {
	if r.DB == nil {
		return errors.New("migrate: nil database")
	}

	// PostgreSQL advisory locks are session-scoped. Pin one *sql.Conn for the
	// complete migration run so lock, migrations and unlock use the same session.
	conn, err := r.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate: reserve connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("migrate: acquire advisory lock: %w", err)
	}
	defer func() {
		unlockCtx := context.Background()
		_, _ = conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _tecno_migrations (
		version BIGINT PRIMARY KEY,
		source TEXT NOT NULL,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("migrate: create metadata table: %w", err)
	}

	migrations, err := loadSources(r.Sources)
	if err != nil {
		return err
	}
	for _, item := range migrations {
		if err := applyOne(ctx, conn, item); err != nil {
			return err
		}
	}
	return nil
}

func applyOne(ctx context.Context, conn *sql.Conn, item migration) error {
	var checksum string
	err := conn.QueryRowContext(ctx, `SELECT checksum FROM _tecno_migrations WHERE version = $1`, item.Version).Scan(&checksum)
	switch {
	case err == nil:
		if checksum != item.Checksum {
			return fmt.Errorf("migrate: version %d checksum changed", item.Version)
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("migrate: inspect version %d: %w", item.Version, err)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: begin version %d: %w", item.Version, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, item.SQL); err != nil {
		return fmt.Errorf("migrate: apply version %d (%s): %w", item.Version, item.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO _tecno_migrations(version, source, name, checksum) VALUES ($1, $2, $3, $4)`,
		item.Version, item.Source, item.Name, item.Checksum,
	); err != nil {
		return fmt.Errorf("migrate: record version %d: %w", item.Version, err)
	}
	if err := tx.Commit(); err != nil {
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
