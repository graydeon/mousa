package sqlite

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestRetrievalAssociatedTokenModeBinding(t *testing.T) {
	for _, mode := range []string{"vector", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			space := importAssociationVector(t, f)
			f.activate(t)
			options := tokenOptions(t)
			options.Supersession = true
			options.Associations = []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "twin", Basis: "support", Author: "test"}}
			got, err := queryTokenRetrieval(t, f, space, mode, mousa.PackingOriginal, "mode-binding", 1024, options)
			if err != nil {
				t.Fatal(err)
			}
			if got.Trail.Schema != mousa.SourceTrailSchemaV15 {
				t.Fatal("missing composed profile")
			}
			got.Trail.Token.Mode = "hybrid"
			if mode == "hybrid" {
				got.Trail.Token.Mode = "vector"
			}
			changed := rebindTokenRecord(t, got.Trail)
			data, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mousa.DecodeSourceTrail(data); err == nil || !strings.Contains(err.Error(), "token selection bindings") {
				t.Errorf("rehashed mismatched token mode: %v", err)
			}
			// Replace only the stored JSON under its existing key: decoding must reject
			// the semantic mismatch before the stored-key identity comparison.
			execDamagingStatement(t, f.store, `UPDATE source_trails SET record_json=? WHERE id=?`, data, got.Trail.ID[:])
			if _, err := f.store.GetSourceTrail(t.Context(), got.Trail.ID); !IsCode(err, CodeIntegrity) || !strings.Contains(err.Error(), "token selection bindings") {
				t.Fatalf("stored mode mismatch: %v", err)
			}
			path := f.store.path
			f.store.Close()
			reopened, err := Open(t.Context(), path)
			if reopened != nil {
				reopened.Close()
			}
			if !IsCode(err, CodeIntegrity) || !strings.Contains(err.Error(), "token selection bindings") {
				t.Fatalf("startup mode mismatch: %v", err)
			}
		})
	}
}
