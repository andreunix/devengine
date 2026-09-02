package schema

import (
	"testing"
)

func TestDiffNoChanges(t *testing.T) {
	snap := &Snapshot{
		Tables: map[string]*Table{
			"users": {
				Name: "users",
				Columns: []Column{
					{Name: "id", DataType: "bigint", IsNullable: false, Position: 1},
					{Name: "email", DataType: "text", IsNullable: false, Position: 2},
				},
			},
		},
	}
	result := Diff(snap, snap)
	if result.HasDrift {
		t.Fatalf("expected no drift, got: %+v", result.Entries)
	}
}

func TestDiffTableAdded(t *testing.T) {
	base := &Snapshot{Tables: map[string]*Table{}}
	live := &Snapshot{
		Tables: map[string]*Table{
			"orders": {Name: "orders", Columns: []Column{{Name: "id", DataType: "bigint"}}},
		},
	}
	result := Diff(base, live)
	if !result.HasDrift {
		t.Fatal("expected drift")
	}
	if len(result.Entries) != 1 || result.Entries[0].Kind != DriftTableAdded {
		t.Fatalf("unexpected entries: %+v", result.Entries)
	}
}

func TestDiffTableRemoved(t *testing.T) {
	base := &Snapshot{
		Tables: map[string]*Table{
			"legacy": {Name: "legacy"},
		},
	}
	live := &Snapshot{Tables: map[string]*Table{}}
	result := Diff(base, live)
	if !result.HasDrift {
		t.Fatal("expected drift")
	}
	if result.Entries[0].Kind != DriftTableRemoved {
		t.Fatalf("expected table_removed, got %s", result.Entries[0].Kind)
	}
}

func TestDiffColumnAdded(t *testing.T) {
	base := &Snapshot{
		Tables: map[string]*Table{
			"users": {Name: "users", Columns: []Column{
				{Name: "id", DataType: "bigint"},
			}},
		},
	}
	live := &Snapshot{
		Tables: map[string]*Table{
			"users": {Name: "users", Columns: []Column{
				{Name: "id", DataType: "bigint"},
				{Name: "created_at", DataType: "timestamptz"},
			}},
		},
	}
	result := Diff(base, live)
	if !result.HasDrift {
		t.Fatal("expected drift")
	}
	found := false
	for _, e := range result.Entries {
		if e.Kind == DriftColumnAdded && e.Object == "created_at" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected column_added for created_at, got %+v", result.Entries)
	}
}

func TestDiffColumnTypeChanged(t *testing.T) {
	base := &Snapshot{
		Tables: map[string]*Table{
			"users": {Name: "users", Columns: []Column{
				{Name: "id", DataType: "integer"},
			}},
		},
	}
	live := &Snapshot{
		Tables: map[string]*Table{
			"users": {Name: "users", Columns: []Column{
				{Name: "id", DataType: "bigint"},
			}},
		},
	}
	result := Diff(base, live)
	if !result.HasDrift {
		t.Fatal("expected drift")
	}
	if result.Entries[0].Kind != DriftColumnChanged {
		t.Fatalf("expected column_changed, got %s", result.Entries[0].Kind)
	}
}

func TestReportIdentical(t *testing.T) {
	snap := &Snapshot{Tables: map[string]*Table{}}
	result := Report(snap, snap)
	if result.HasDrift {
		t.Fatal("identical snapshots should produce no drift")
	}
}

func TestIgnoreDevengineTable(t *testing.T) {
	if !ignoreTable("_devengine_migrations") {
		t.Fatal("expected _devengine_migrations to be ignored")
	}
	if ignoreTable("orders") {
		t.Fatal("expected orders to not be ignored")
	}
}

func TestSnapshotJSONRoundtrip(t *testing.T) {
	snap := &Snapshot{
		Tables: map[string]*Table{
			"products": {
				Name: "products",
				Columns: []Column{
					{Name: "id", DataType: "bigint", IsNullable: false, Position: 1},
				},
			},
		},
		Enums:           map[string][]string{"status": {"active", "inactive"}},
		SnapshotVersion: 2,
		Sequences:       []Sequence{{Name: "products_id_seq", DataType: "bigint", Start: "1", MinValue: "1", MaxValue: "9223372036854775807", Increment: "1", Cache: "1"}},
	}
	data, err := snap.MarshalToJSON()
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(got.Tables))
	}
	if len(got.Enums["status"]) != 2 {
		t.Fatalf("expected 2 enum values, got %d", len(got.Enums["status"]))
	}
}

func TestDiffSequenceChanged(t *testing.T) {
	base := &Snapshot{Tables: map[string]*Table{}, Sequences: []Sequence{{Name: "ids", Increment: "1", Cache: "1"}}}
	live := &Snapshot{Tables: map[string]*Table{}, Sequences: []Sequence{{Name: "ids", Increment: "2", Cache: "10"}}}
	result := Diff(base, live)
	if len(result.Entries) != 1 || result.Entries[0].Kind != DriftSequenceChanged {
		t.Fatalf("unexpected drift: %+v", result.Entries)
	}
}

func TestUnmarshalLegacySequenceSnapshot(t *testing.T) {
	s, err := UnmarshalSnapshot([]byte(`{"tables":{},"sequences":["ids"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.SnapshotVersion != 1 || len(s.Sequences) != 1 || s.Sequences[0].Name != "ids" {
		t.Fatalf("legacy snapshot: %+v", s)
	}
}
