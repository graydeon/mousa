package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassificationAdministration(t *testing.T) {
	t.Parallel()
	run, _ := setup(t)
	jsonlSync(t, run, "labels", record("doc", "cedar synthetic evidence"))
	before := querySource(t, run, "labels", "cedar")
	pin := hits(before)[0].(map[string]any)
	input := map[string]any{
		"schema":   "mousa.classification_input.v1",
		"subject":  map[string]any{"kind": "segment", "id": pin["segment_id"]},
		"taxonomy": "example.review", "taxonomy_version": "1", "label": "synthetic-reviewed",
		"asserter_id": "example.caller", "asserter_version": "1",
		"basis":            []any{map[string]any{"kind": "representation", "id": pin["representation_id"]}},
		"asserted_at_usec": 1, "confidence_ppm": 0,
	}
	put := func() string {
		t.Helper()
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		code, out, stderr := run.runExitInput(string(data), "classification", "put")
		if code != 0 {
			t.Fatalf("put exit=%d stdout=%q stderr=%s", code, out, stderr)
		}
		return out
	}
	original := put()
	var saved map[string]any
	if err := json.Unmarshal([]byte(original), &saved); err != nil {
		t.Fatal(err)
	}
	if saved["schema"] != "mousa.classification.v1" || saved["confidence_ppm"] != float64(0) {
		t.Fatalf("wrong canonical record: %s", original)
	}
	rows, links := tableDigestPair(t, run.store)
	if retry := put(); retry != original {
		t.Fatal("retry changed canonical output")
	}
	afterRows, afterLinks := tableDigestPair(t, run.store)
	if rows != afterRows || links != afterLinks {
		t.Fatal("retry changed rows or basis links")
	}
	input["label"] = "synthetic-disputed"
	changed := put()
	var changedRecord map[string]any
	if err := json.Unmarshal([]byte(changed), &changedRecord); err != nil {
		t.Fatal(err)
	}
	if changedRecord["id"] == saved["id"] {
		t.Fatal("changed assertion reused identity")
	}
	code, out, stderr := run.runExit("classification", "get", saved["id"].(string))
	if code != 0 || out != original {
		t.Fatalf("original readback exit=%d stdout=%q stderr=%s", code, out, stderr)
	}
	after := querySource(t, run, "labels", "cedar")
	if before["packet_id"] != after["packet_id"] {
		t.Fatal("administration changed retrieval selection")
	}
	rows, links = tableDigestPair(t, run.store)
	input["subject"] = map[string]any{"kind": "source", "id": strings.Repeat("a", 64)}
	data, _ := json.Marshal(input)
	code, out, stderr = run.runExitInput(string(data), "classification", "put")
	if code != 1 || out != "" || !strings.Contains(stderr, "conflict") {
		t.Fatalf("missing parent exit=%d out=%q err=%s", code, out, stderr)
	}
	afterRows, afterLinks = tableDigestPair(t, run.store)
	if rows != afterRows || links != afterLinks {
		t.Fatal("missing parent left partial rows")
	}
	jsonlSync(t, run, "labels", record("doc", "cedar replacement evidence"))
	replacement := hits(querySource(t, run, "labels", "cedar"))[0].(map[string]any)
	if replacement["segment_id"] == pin["segment_id"] || replacement["representation_id"] == pin["representation_id"] {
		t.Fatal("revision did not replace pins")
	}
	code, out, stderr = run.runExit("classification", "get", saved["id"].(string))
	if code != 0 || out != original {
		t.Fatalf("retired assertion changed: exit=%d out=%q err=%s", code, out, stderr)
	}
	// Basis parents need not share the subject's source.
	jsonlSync(t, run, "peer", record("basis", "peer evidence"))
	peer := hits(querySource(t, run, "peer", "peer"))[0].(map[string]any)
	input["subject"] = map[string]any{"kind": "segment", "id": replacement["segment_id"]}
	input["basis"] = []any{map[string]any{"kind": "representation", "id": peer["representation_id"]}}
	put()
	// Force a link insert failure after the classification insert, not just a
	// pre-insert missing-parent refusal, to check the command's rollback path.
	rawExec(t, run.store, `CREATE TRIGGER reject_classification_basis BEFORE INSERT ON classification_bases BEGIN SELECT RAISE(ABORT, 'test link failure'); END`)
	rows, links = tableDigestPair(t, run.store)
	input["label"] = "synthetic-rollback"
	data, _ = json.Marshal(input)
	code, out, stderr = run.runExitInput(string(data), "classification", "put")
	if code != 1 || out != "" {
		t.Fatalf("link failure exit=%d out=%q err=%s", code, out, stderr)
	}
	afterRows, afterLinks = tableDigestPair(t, run.store)
	if rows != afterRows || links != afterLinks {
		t.Fatal("link failure committed partial assertion")
	}
}

func tableDigestPair(t *testing.T, path string) (string, string) {
	t.Helper()
	n, a := tableDigest(t, path, "classifications")
	m, b := tableDigest(t, path, "classification_bases")
	return fmt.Sprintf("%d/%s", n, a), fmt.Sprintf("%d/%s", m, b)
}

func TestClassificationRejectsBeforeStoreCreation(t *testing.T) {
	t.Parallel()
	run, _ := setup(t)
	run.store = filepath.Join(t.TempDir(), "absent.sqlite")
	for _, test := range []struct {
		input string
		args  []string
		exit  int
	}{
		{`{}`, []string{"classification", "put"}, 1},
		{`{"schema":"mousa.classification_input.v1"} {}`, []string{"classification", "put"}, 1},
		{strings.Repeat(" ", (1<<20)+1), []string{"classification", "put"}, 1},
		{``, []string{"classification", "put", "--unknown"}, 2},
		{``, []string{"classification", "put", "extra"}, 2},
		{``, []string{"classification", "get", "abcd"}, 2},
		{``, []string{"classification", "get", strings.Repeat("A", 64)}, 2},
		{``, []string{"classification", "get"}, 2},
		{``, []string{"classification", "get", strings.Repeat("a", 64)}, 1},
	} {
		code, out, stderr := run.runExitInput(test.input, test.args...)
		if code != test.exit || out != "" {
			t.Fatalf("%v exit=%d out=%q err=%s", test.args, code, out, stderr)
		}
		if _, err := os.Stat(run.store); !os.IsNotExist(err) {
			t.Fatalf("rejected command created store: %v", err)
		}
	}
}

func TestClassificationNativeDamageAndUnknown(t *testing.T) {
	t.Parallel()
	for _, damage := range []string{
		`UPDATE classifications SET record_json = x'7b7d0a'`,
		`UPDATE classification_bases SET ordinal=99`,
		`UPDATE classifications SET subject_kind='representation',subject_representation_id=(SELECT basis_representation_id FROM classification_bases LIMIT 1),subject_segment_id=NULL`,
	} {
		t.Run(damage, func(t *testing.T) {
			run, _ := setup(t)
			jsonlSync(t, run, "damage", record("doc", "cedar evidence"))
			pin := hits(querySource(t, run, "damage", "cedar"))[0].(map[string]any)
			input := fmt.Sprintf(`{"schema":"mousa.classification_input.v1","subject":{"kind":"segment","id":%q},"taxonomy":"example","taxonomy_version":"1","label":"synthetic","asserter_id":"caller","asserter_version":"1","basis":[{"kind":"representation","id":%q}],"asserted_at_usec":1,"confidence_ppm":0}`, pin["segment_id"], pin["representation_id"])
			code, out, stderr := run.runExitInput(input, "classification", "put")
			if code != 0 {
				t.Fatalf("put exit=%d err=%s", code, stderr)
			}
			var saved map[string]any
			if err := json.Unmarshal([]byte(out), &saved); err != nil {
				t.Fatal(err)
			}
			rows, links := tableDigestPair(t, run.store)
			code, out, stderr = run.runExit("classification", "get", strings.Repeat("a", 64))
			if code != 1 || out != "" || !strings.Contains(stderr, "not_found") {
				t.Fatalf("unknown exit=%d out=%q err=%s", code, out, stderr)
			}
			a, b := tableDigestPair(t, run.store)
			if a != rows || b != links {
				t.Fatal("unknown read changed rows")
			}
			rawExec(t, run.store, damage)
			rows, links = tableDigestPair(t, run.store)
			code, out, stderr = run.runExit("classification", "get", saved["id"].(string))
			if code != 1 || out != "" || !strings.Contains(stderr, "integrity") {
				t.Fatalf("damage exit=%d out=%q err=%s", code, out, stderr)
			}
			a, b = tableDigestPair(t, run.store)
			if a != rows || b != links {
				t.Fatal("damaged read repaired records")
			}
			code, out, stderr = run.runExit("status", "--source", "damage")
			if code != 1 || out != "" || !strings.Contains(stderr, "integrity") {
				t.Fatalf("startup damage exit=%d out=%q err=%s", code, out, stderr)
			}
		})
	}
}
