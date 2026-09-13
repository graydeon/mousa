package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func testCaller(t *testing.T, namespace, externalCallerID string) mousa.Caller {
	t.Helper()
	id, err := mousa.NewCallerID(namespace, externalCallerID)
	if err != nil {
		t.Fatal(err)
	}
	return mousa.Caller{Schema: mousa.CallerSchema, ID: id, Namespace: namespace, ExternalCallerID: externalCallerID}
}

func testPurpose(t *testing.T, namespace, externalPurposeID string) mousa.Purpose {
	t.Helper()
	id, err := mousa.NewPurposeID(namespace, externalPurposeID)
	if err != nil {
		t.Fatal(err)
	}
	return mousa.Purpose{Schema: mousa.PurposeSchema, ID: id, Namespace: namespace, ExternalPurposeID: externalPurposeID}
}

func TestCallerIdentityStorage(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "identity.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	caller := testCaller(t, "example.harness", "agent:alpha")
	if err := store.PutCaller(ctx, caller); err != nil {
		t.Fatalf("PutCaller: %v", err)
	}
	if err := store.PutCaller(ctx, caller); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	got, err := store.GetCaller(ctx, caller.ID)
	if err != nil || !reflect.DeepEqual(got, caller) {
		t.Fatalf("GetCaller = %#v, %v", got, err)
	}
	derived, err := mousa.NewCallerID(caller.Namespace, caller.ExternalCallerID)
	if err != nil {
		t.Fatal(err)
	}
	if derived != caller.ID {
		t.Fatal("caller ID is not derivable from its tuple")
	}

	changed := caller
	changed.ExternalCallerID = "agent:beta"
	changed.ID, err = mousa.NewCallerID(changed.Namespace, changed.ExternalCallerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutCaller(ctx, changed); err != nil {
		t.Fatalf("second identity: %v", err)
	}

	purpose := testPurpose(t, "example.harness", "task:answer")
	if err := store.PutPurpose(ctx, purpose); err != nil {
		t.Fatalf("PutPurpose: %v", err)
	}
	if err := store.PutPurpose(ctx, purpose); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	gotPurpose, err := store.GetPurpose(ctx, purpose.ID)
	if err != nil || !reflect.DeepEqual(gotPurpose, purpose) {
		t.Fatalf("GetPurpose = %#v, %v", gotPurpose, err)
	}

	if _, err := store.GetCaller(ctx, changed.ID); err != nil {
		t.Fatalf("second identity readable: %v", err)
	}
	if _, err := store.GetCaller(ctx, mousa.CallerID{}); !IsCode(err, CodeNotFound) {
		t.Fatalf("absent caller error = %v, want not_found", err)
	}
	if _, err := store.GetPurpose(ctx, mousa.PurposeID{}); !IsCode(err, CodeNotFound) {
		t.Fatalf("absent purpose error = %v, want not_found", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer readOnly.Close()
	if _, err := readOnly.GetCaller(ctx, caller.ID); err != nil {
		t.Fatalf("read-only GetCaller: %v", err)
	}
	if _, err := readOnly.GetPurpose(ctx, purpose.ID); err != nil {
		t.Fatalf("read-only GetPurpose: %v", err)
	}
}

func TestCallerIdentityConflictAndReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "identity-conflict.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	caller := testCaller(t, "example.harness", "agent:alpha")
	if err := store.PutCaller(ctx, caller); err != nil {
		t.Fatalf("PutCaller: %v", err)
	}
	purpose := testPurpose(t, "example.harness", "task:answer")
	if err := store.PutPurpose(ctx, purpose); err != nil {
		t.Fatalf("PutPurpose: %v", err)
	}

	forged := caller
	forged.Namespace = "example.other"
	if err := store.PutCaller(ctx, forged); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("forged identity error = %v, want invalid_record", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer readOnly.Close()
	if err := readOnly.PutCaller(ctx, caller); !IsCode(err, CodeReadOnly) {
		t.Fatalf("read-only PutCaller error = %v, want read_only", err)
	}
	if err := readOnly.PutPurpose(ctx, purpose); !IsCode(err, CodeReadOnly) {
		t.Fatalf("read-only PutPurpose error = %v, want read_only", err)
	}
	if _, err := readOnly.GetCaller(ctx, caller.ID); err != nil {
		t.Fatalf("read-only GetCaller: %v", err)
	}
	if _, err := readOnly.GetPurpose(ctx, purpose.ID); err != nil {
		t.Fatalf("read-only GetPurpose: %v", err)
	}
}

func TestCallerIdentityTamperIsIntegrity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "identity-tamper.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	caller := testCaller(t, "example.harness", "agent:alpha")
	if err := store.PutCaller(ctx, caller); err != nil {
		t.Fatalf("PutCaller: %v", err)
	}
	purpose := testPurpose(t, "example.harness", "task:answer")
	if err := store.PutPurpose(ctx, purpose); err != nil {
		t.Fatalf("PutPurpose: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	rawExec(t, path, `UPDATE callers SET external_caller_id = 'agent:beta' WHERE id = x'`+caller.ID.String()+`'`)
	damaged, err := OpenReadOnly(ctx, path)
	if err == nil {
		damaged.Close()
		t.Fatal("open must fail closed for a tampered caller projection")
	}
	if !IsCode(err, CodeIntegrity) {
		t.Fatalf("tampered caller open error = %v, want integrity", err)
	}

	rawExec(t, path, `UPDATE callers SET external_caller_id = 'agent:alpha' WHERE id = x'`+caller.ID.String()+`'`)
	rawExec(t, path, `UPDATE purposes SET namespace = 'example.other' WHERE id = x'`+purpose.ID.String()+`'`)
	if _, err := OpenReadOnly(ctx, path); err == nil {
		t.Fatal("open must fail closed for a tampered purpose projection")
	}
}

func TestCallerIdentitySchemaRejectsEmptyTuple(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "identity-schema.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	caller := testCaller(t, "example.harness", "agent:alpha")
	if err := store.PutCaller(ctx, caller); err != nil {
		t.Fatalf("PutCaller: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE callers SET external_caller_id = '' WHERE id = x'` + caller.ID.String() + `'`); err == nil {
		t.Fatal("schema must reject an empty external caller ID")
	}
}
