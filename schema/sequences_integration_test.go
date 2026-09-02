package schema

import (
	"context"
	"testing"

	testpostgres "github.com/andreunix/devengine/testutil/postgres"
)

func TestCaptureAndDiffSequenceMetadata(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool().Exec(ctx, `CREATE SEQUENCE order_number_seq AS bigint START WITH 10 MINVALUE 0 MAXVALUE 100 INCREMENT BY 2 CACHE 5 CYCLE`); err != nil {
		t.Fatal(err)
	}
	baseline, err := Capture(ctx, db.Pool())
	if err != nil {
		t.Fatal(err)
	}
	sequence := sequenceNamed(t, baseline, "order_number_seq")
	if sequence.DataType != "bigint" || sequence.Start != "10" || sequence.MinValue != "0" || sequence.MaxValue != "100" || sequence.Increment != "2" || sequence.Cache != "5" || !sequence.Cycle {
		t.Fatalf("unexpected sequence: %+v", sequence)
	}

	if _, err := db.Pool().Exec(ctx, `ALTER SEQUENCE order_number_seq MINVALUE 1 MAXVALUE 99 INCREMENT BY 3 CACHE 7 NO CYCLE`); err != nil {
		t.Fatal(err)
	}
	live, err := Capture(ctx, db.Pool())
	if err != nil {
		t.Fatal(err)
	}
	if !hasDrift(Diff(baseline, live), DriftSequenceChanged, "order_number_seq") {
		t.Fatal("expected sequence_changed")
	}

	if _, err := db.Pool().Exec(ctx, `CREATE SEQUENCE added_seq`); err != nil {
		t.Fatal(err)
	}
	added, err := Capture(ctx, db.Pool())
	if err != nil {
		t.Fatal(err)
	}
	if !hasDrift(Diff(live, added), DriftSequenceAdded, "added_seq") {
		t.Fatal("expected sequence_added")
	}
	if _, err := db.Pool().Exec(ctx, `DROP SEQUENCE added_seq`); err != nil {
		t.Fatal(err)
	}
	removed, err := Capture(ctx, db.Pool())
	if err != nil {
		t.Fatal(err)
	}
	if !hasDrift(Diff(added, removed), DriftSequenceRemoved, "added_seq") {
		t.Fatal("expected sequence_removed")
	}
}

func sequenceNamed(t *testing.T, snapshot *Snapshot, name string) Sequence {
	t.Helper()
	for _, s := range snapshot.Sequences {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("sequence %q not found", name)
	return Sequence{}
}
func hasDrift(result DriftResult, kind DriftKind, object string) bool {
	for _, entry := range result.Entries {
		if entry.Kind == kind && entry.Object == object {
			return true
		}
	}
	return false
}
