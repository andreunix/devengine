package migrate

import (
	"testing"
	"testing/fstest"
)

func TestMigrationRangesAndOrdering(t *testing.T) {
	sources := []Source{
		{Kind: AppSource, FS: fstest.MapFS{"1001_second.up.sql": {Data: []byte("select 2;")}, "1000_first.up.sql": {Data: []byte("select 1;")}}},
		{Kind: EngineSource, FS: fstest.MapFS{"0001_engine.up.sql": {Data: []byte("select 0;")}}},
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
	_, err := loadSources([]Source{{Kind: AppSource, FS: fstest.MapFS{"0002_bad.up.sql": {Data: []byte("select 1;")}}}})
	if err == nil {
		t.Fatal("expected range error")
	}
}
