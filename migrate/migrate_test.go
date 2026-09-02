package migrate

import (
	"testing"
	"testing/fstest"
)

func TestMigrationRangesAndOrdering(t *testing.T) {
	sources := []Source{
		{Kind: AppSource, FS: fstest.MapFS{
			"1001_second.up.sql": {Data: []byte("select 2;")},
			"1000_first.up.sql":  {Data: []byte("select 1;")},
		}},
		{Kind: EngineSource, FS: fstest.MapFS{
			"0001_engine.up.sql": {Data: []byte("select 0;")},
		}},
	}
	items, err := loadSources(sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Version != 1 || items[1].Version != 1000 || items[2].Version != 1001 {
		t.Fatalf("unexpected ordering: %#v", items)
	}
}

func TestRejectsApplicationMigrationInEngineRange(t *testing.T) {
	_, err := loadSources([]Source{
		{Kind: AppSource, FS: fstest.MapFS{"0002_bad.up.sql": {Data: []byte("select 1;")}}},
	})
	if err == nil {
		t.Fatal("expected range error")
	}
}

func TestChecksumDetectsChange(t *testing.T) {
	sources := []Source{
		{Kind: AppSource, FS: fstest.MapFS{
			"1000_init.up.sql": {Data: []byte("CREATE TABLE foo (id BIGINT PRIMARY KEY);")},
		}},
	}
	items, err := loadSources(sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 migration, got %d", len(items))
	}
	if items[0].Checksum == "" {
		t.Fatal("expected non-empty checksum")
	}

	// Different content must produce a different checksum.
	sources2 := []Source{
		{Kind: AppSource, FS: fstest.MapFS{
			"1000_init.up.sql": {Data: []byte("CREATE TABLE bar (id BIGINT PRIMARY KEY);")},
		}},
	}
	items2, err := loadSources(sources2)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Checksum == items2[0].Checksum {
		t.Fatal("expected different checksums for different SQL content")
	}
}

func TestDuplicateVersionRejected(t *testing.T) {
	sources := []Source{
		{Kind: AppSource, FS: fstest.MapFS{
			"1000_first.up.sql":  {Data: []byte("select 1;")},
			"1000_second.up.sql": {Data: []byte("select 2;")},
		}},
	}
	_, err := loadSources(sources)
	if err == nil {
		t.Fatal("expected duplicate version error")
	}
}

func TestMetadataTableDefault(t *testing.T) {
	r := Runner{}
	if r.metadataTable() != `"_devengine_migrations"` {
		t.Errorf("expected \"_devengine_migrations\", got %q", r.metadataTable())
	}
}

func TestMetadataTableOverride(t *testing.T) {
	r := Runner{MetadataTable: "  _custom_migrations  "}
	if r.metadataTable() != `"_custom_migrations"` {
		t.Errorf("expected \"_custom_migrations\", got %q", r.metadataTable())
	}
}
