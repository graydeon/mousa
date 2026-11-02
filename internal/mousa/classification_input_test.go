package mousa

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func classificationDraft(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(validClassificationJSON), &object); err != nil {
		t.Fatal(err)
	}
	delete(object, "id")
	object["schema"] = json.RawMessage(`"mousa.classification_input.v1"`)
	return object
}

func TestClassificationInputCanonicalAndSubjects(t *testing.T) {
	object := classificationDraft(t)
	data, _ := json.Marshal(object)
	got, err := DecodeClassificationInput(data)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeClassification(got)
	if err != nil || string(encoded) != validClassificationJSON {
		t.Fatalf("canonical=%s err=%v", encoded, err)
	}
	for _, kind := range []string{"source", "observation", "artifact", "representation", "segment"} {
		t.Run(kind, func(t *testing.T) {
			object := classificationDraft(t)
			object["subject"] = json.RawMessage(fmt.Sprintf(`{"kind":%q,"id":%q}`, kind, strings.Repeat("a", 64)))
			object["basis"] = json.RawMessage(fmt.Sprintf(`[{"kind":%q,"id":%q},{"kind":"source","id":%q}]`, kind, strings.Repeat("a", 64), strings.Repeat("b", 64)))
			object["confidence_ppm"] = json.RawMessage(`0`)
			data, _ := json.Marshal(object)
			record, err := DecodeClassificationInput(data)
			if err != nil {
				t.Fatal(err)
			}
			if string(record.Subject.Kind()) != kind || string(record.Basis[0].Kind()) != kind || record.Basis[1].Kind() != ClassificationSubjectSource || record.ConfidencePPM != 0 {
				t.Fatalf("wrong typed facts: %#v", record)
			}
		})
	}
	reversed := classificationDraft(t)
	reversed["basis"] = json.RawMessage(`[{"kind":"observation","id":"8ad5e465541e71ad6246c21fdd9da960736394e1ac4688e034471910784ea12a"},{"kind":"source","id":"f111237310ae4db59c528c5e0e53581384059910adab736913c6e29f0ef91d41"}]`)
	raw, _ := json.Marshal(reversed)
	swapped, err := DecodeClassificationInput(raw)
	if err != nil || swapped.ID == got.ID || reflect.DeepEqual(swapped.Basis, got.Basis) {
		t.Fatalf("basis order lost: %v", err)
	}
}

func TestClassificationInputStrict(t *testing.T) {
	object := classificationDraft(t)
	base, _ := json.Marshal(object)
	tests := map[string]string{
		"trailing": string(base) + ` {}`, "utf8": string(base) + string([]byte{255}),
		"not object": `[]`, "null object": `null`, "oversize": strings.Repeat(" ", MaxClassificationInputBytes+1),
		"duplicate":      strings.Replace(string(base), `"label":`, `"label":"first","label":`, 1),
		"case alias":     strings.Replace(string(base), `"label":`, `"Label":`, 1),
		"case duplicate": strings.Replace(string(base), `"label":`, `"Label":"first","label":`, 1),
	}
	for _, field := range []string{"schema", "subject", "taxonomy", "taxonomy_version", "label", "asserter_id", "asserter_version", "basis", "asserted_at_usec", "confidence_ppm"} {
		for _, value := range []string{"null", ""} {
			changed := classificationDraft(t)
			if value == "" {
				delete(changed, field)
			} else {
				changed[field] = json.RawMessage(value)
			}
			raw, _ := json.Marshal(changed)
			tests[field+"/"+value] = string(raw)
		}
	}
	changes := map[string][]string{
		"schema": {`"mousa.classification.v1"`}, "id": {`"` + canonicalClassificationID + `"`}, "extra": {`true`},
		"subject":          {`{"Kind":"segment","id":"` + strings.Repeat("a", 64) + `"}`, `{"kind":"segment","id":null}`, `{"kind":"segment","id":"` + strings.Repeat("a", 64) + `","id":"` + strings.Repeat("b", 64) + `"}`, `{"kind":"segment","id":"` + strings.Repeat("a", 64) + `","extra":0}`, `{"kind":"item","id":"` + strings.Repeat("a", 64) + `"}`, `{"kind":"source","id":"` + strings.Repeat("0", 64) + `"}`, `{"kind":"source","id":"` + strings.Repeat("A", 64) + `"}`, `"source"`},
		"basis":            {`[]`, `[null]`, `[{"Kind":"source","id":"` + strings.Repeat("a", 64) + `"}]`, `[{"kind":"source","id":"` + strings.Repeat("a", 64) + `","kind":"segment"}]`, `[{"kind":"source","id":null}]`, `[{"kind":"source","id":"` + strings.Repeat("a", 64) + `","extra":0}]`, `[{"kind":"source","id":"` + strings.Repeat("a", 64) + `"},{"kind":"source","id":"` + strings.Repeat("a", 64) + `"}]`},
		"asserted_at_usec": {`0`, `-1`, `1.0`, `1e2`, `"1"`, `9223372036854775808`},
		"confidence_ppm":   {`-1`, `1.0`, `1e2`, `"0"`, `1000001`, `18446744073709551616`},
		"taxonomy":         {`""`}, "asserter_id": {`""`},
	}
	for key, values := range changes {
		for i, value := range values {
			changed := classificationDraft(t)
			changed[key] = json.RawMessage(value)
			raw, _ := json.Marshal(changed)
			tests[fmt.Sprintf("%s/%d", key, i)] = string(raw)
		}
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeClassificationInput([]byte(raw)); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		})
	}
}

func TestClassificationInputBasisCapacity(t *testing.T) {
	object := classificationDraft(t)
	basis := make([]map[string]string, 4096)
	for i := range basis {
		basis[i] = map[string]string{"kind": "representation", "id": fmt.Sprintf("%064x", i+1)}
	}
	object["basis"], _ = json.Marshal(basis)
	raw, _ := json.Marshal(object)
	if len(raw) <= 64<<10 || len(raw) > MaxClassificationInputBytes {
		t.Fatalf("basis transport size=%d", len(raw))
	}
	record, err := DecodeClassificationInput(raw)
	if err != nil || len(record.Basis) != 4096 {
		t.Fatalf("4096 entries: %v", err)
	}
	padded := append(raw, []byte(strings.Repeat(" ", MaxClassificationInputBytes-len(raw)))...)
	if _, err := DecodeClassificationInput(padded); err != nil {
		t.Fatalf("exact transport bound: %v", err)
	}
	basis = append(basis, map[string]string{"kind": "source", "id": strings.Repeat("a", 64)})
	object["basis"], _ = json.Marshal(basis)
	raw, _ = json.Marshal(object)
	if _, err := DecodeClassificationInput(raw); err == nil {
		t.Fatal("accepted4097 basis entries")
	}
}
