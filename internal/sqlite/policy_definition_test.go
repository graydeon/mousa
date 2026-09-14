package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestPolicyDefinitionStorageConflictIntegrityAndReadOnly(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "policy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	representation, content, _ := addLexicalDocument(t, store, "policy-storage", "alpha evidence")
	if err := store.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
		t.Fatal(err)
	}
	beforeLexical, err := store.SearchLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	beforeVerified, err := store.SearchVerifiedLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}

	record := testPolicyDefinition(t, "example.operator", "outbound-default", "2026-08-25", "application/example-policy+json", "example.policy.v1", "{\"default\":\"deny\"}\n")
	if err := store.PutPolicyDefinition(ctx, record); err != nil {
		t.Fatalf("PutPolicyDefinition: %v", err)
	}
	if err := store.PutPolicyDefinition(ctx, record); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	got, err := store.GetPolicyDefinition(ctx, record.ID)
	if err != nil || !reflect.DeepEqual(got, record) {
		t.Fatalf("GetPolicyDefinition = %#v, %v", got, err)
	}

	for _, changed := range []mousa.PolicyDefinition{
		testPolicyDefinition(t, record.Namespace, record.ExternalPolicyID, record.ExternalPolicyVersion, record.DefinitionMediaType, record.DefinitionSchema, "{\"default\":\"allow\"}\n"),
		testPolicyDefinition(t, record.Namespace, record.ExternalPolicyID, record.ExternalPolicyVersion, "application/other", record.DefinitionSchema, record.Definition),
		testPolicyDefinition(t, record.Namespace, record.ExternalPolicyID, record.ExternalPolicyVersion, record.DefinitionMediaType, "other.schema", record.Definition),
		testPolicyDefinition(t, record.Namespace, record.ExternalPolicyID, record.ExternalPolicyVersion, record.DefinitionMediaType, record.DefinitionSchema, strings.TrimSuffix(record.Definition, "\n")),
	} {
		if err := store.PutPolicyDefinition(ctx, changed); !IsCode(err, CodeConflict) {
			t.Fatalf("logical-version conflict for %s = %v", changed.ID, err)
		}
	}
	unknown := testPolicyDefinition(t, " unknown namespace ", "other", " v2 ", "not parsed", ":not-a-uri", " opaque bytes \n")
	if err := store.PutPolicyDefinition(ctx, unknown); err != nil {
		t.Fatalf("opaque contract strings: %v", err)
	}
	afterLexical, err := store.SearchLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	afterVerified, err := store.SearchVerifiedLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterLexical, beforeLexical) || !reflect.DeepEqual(afterVerified, beforeVerified) {
		t.Fatalf("policy definition changed retrieval: lexical %v -> %v; verified %v -> %v", beforeLexical, afterLexical, beforeVerified, afterVerified)
	}

	missing := record.ID
	missing[0] ^= 0xff
	if _, err := store.GetPolicyDefinition(ctx, missing); !IsCode(err, CodeNotFound) {
		t.Fatalf("missing definition = %v", err)
	}
	oversized := testPolicyDefinition(t, "example", "large", "1", "opaque", "opaque", strings.Repeat("x", maxRecordBytes+1))
	if err := store.PutPolicyDefinition(ctx, oversized); !IsCode(err, CodeResourceLimit) {
		t.Fatalf("oversized definition = %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.PutPolicyDefinition(cancelled, testPolicyDefinition(t, "example", "cancelled", "1", "opaque", "opaque", "definition")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write = %v", err)
	}
	lockerStore, err := Open(ctx, store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer lockerStore.Close()
	locker, err := lockerStore.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := locker.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	busy := store.PutPolicyDefinition(ctx, testPolicyDefinition(t, "example", "busy", "1", "opaque", "opaque", "definition"))
	if _, err := locker.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	locker.Close()
	if !IsCode(busy, CodeBusy) {
		t.Fatalf("busy write = %v", busy)
	}
	readOnly, err := OpenReadOnly(ctx, store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if got, err := readOnly.GetPolicyDefinition(ctx, record.ID); err != nil || !reflect.DeepEqual(got, record) {
		t.Fatalf("read-only GetPolicyDefinition = %#v, %v", got, err)
	}
	if err := readOnly.PutPolicyDefinition(ctx, mousa.PolicyDefinition{}); !IsCode(err, CodeReadOnly) {
		t.Fatalf("read-only precedence = %v", err)
	}

	if _, err := store.db.ExecContext(ctx, `UPDATE policy_definitions SET namespace = 'tampered' WHERE id = ?`, record.ID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPolicyDefinition(ctx, record.ID); !IsCode(err, CodeIntegrity) {
		t.Fatalf("projection tamper = %v", err)
	}
	if err := store.PutPolicyDefinition(ctx, record); !IsCode(err, CodeConflict) {
		t.Fatalf("same-ID projection conflict = %v", err)
	}
}

func TestPolicyDefinitionStoreDetectsEveryTamperClass(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  string
	}{
		{"record bytes", `UPDATE policy_definitions SET record_json = x'7b7d0a'`},
		{"namespace projection", `UPDATE policy_definitions SET namespace = 'tampered'`},
		{"policy ID projection", `UPDATE policy_definitions SET external_policy_id = 'tampered'`},
		{"logical projection", `UPDATE policy_definitions SET external_policy_version = 'tampered'`},
		{"stored ID", `UPDATE policy_definitions SET id = randomblob(32)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(ctx, filepath.Join(t.TempDir(), "tamper.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			record := testPolicyDefinition(t, "example", "policy", "1", "opaque", "opaque", "definition")
			if err := store.PutPolicyDefinition(ctx, record); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, test.sql); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetPolicyDefinition(ctx, record.ID); !IsCode(err, CodeIntegrity) && test.name != "stored ID" {
				t.Fatalf("GetPolicyDefinition tamper = %v", err)
			}
			if err := verifyVersion(ctx, store.db, mustMigrations(t), 5, true, false); !IsCode(err, CodeIntegrity) {
				t.Fatalf("startup verification tamper = %v", err)
			}
		})
	}
}

func testPolicyDefinition(t testing.TB, namespace, externalID, version, mediaType, schema, definition string) mousa.PolicyDefinition {
	t.Helper()
	digest := mousa.SHA256(sha256.Sum256([]byte(definition)))
	id, err := mousa.NewPolicyDefinitionID(namespace, externalID, version, mediaType, schema, digest)
	if err != nil {
		t.Fatal(err)
	}
	return mousa.PolicyDefinition{Schema: mousa.PolicyDefinitionSchema, ID: id, Namespace: namespace, ExternalPolicyID: externalID, ExternalPolicyVersion: version, DefinitionMediaType: mediaType, DefinitionSchema: schema, DefinitionSHA256: digest, Definition: definition}
}

func mustMigrations(t testing.TB) []migration {
	t.Helper()
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	return migrations
}
