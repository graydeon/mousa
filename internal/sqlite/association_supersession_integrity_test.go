package sqlite

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestAssociatedSupersessionBoundariesAndHistory(t *testing.T) {
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			f := newV4Fixture(t)
			declaration, state := f.activate(t)
			primary := uint64(len("sharedterm peer evidence"))
			support := uint64(len("sharedterm predecessor evidence"))
			for _, reverse := range []bool{false, true} {
				declarations := []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "predecessor", Basis: "old", Author: "test"}, {FromItem: "peer", ToItem: "twin", Basis: "twin", Author: "test"}}
				if reverse {
					declarations[0], declarations[1] = declarations[1], declarations[0]
				}
				for _, budget := range []uint64{primary, primary + support - 1, primary + support} {
					r, err := f.store.EvaluateAndTraceAssociatedSupersessionLexical(t.Context(), testEvaluationRequest(t, f.source.ID, fmt.Sprintf("boundary-%v-%d", reverse, budget)), "peer", 10, budget, packing, declarations)
					if err != nil {
						t.Fatal(err)
					}
					selected := 0
					for _, row := range r.Trail.Associated {
						if row.ToItem == "predecessor" && (row.Selected || row.Omission != "superseded") {
							t.Fatal("predecessor not withheld before packing")
						}
						if row.ToItem == "twin" {
							if row.Selected {
								selected++
							}
							if row.Selected != (budget == primary+support) || (!row.Selected && row.Omission != "budget") {
								t.Fatalf("twin budget %+v", row)
							}
						}
					}
					if r.Trail.UsedBytes != primary+uint64(selected)*support {
						t.Fatal("withheld bytes spent budget")
					}
				}
			}
			ds := []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "predecessor", Basis: "qualification", Author: "test"}}
			original, err := f.store.EvaluateAndTraceAssociatedSupersessionLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "history-original"), "peer", 10, 1024, packing, ds)
			if err != nil {
				t.Fatal(err)
			}
			before, err := mousa.EncodeSourceTrail(original.Trail)
			if err != nil {
				t.Fatal(err)
			}
			off := activationTestTransition(t, f.source.ID, state.CurrentActivationID, mousa.SupersessionDeclarationID{}, "deactivate", activationBaseUsec+1)
			if err := f.store.ApplySupersessionActivation(t.Context(), off); err != nil {
				t.Fatal(err)
			}
			history, err := f.store.GetSourceTrail(t.Context(), original.Trail.ID)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := mousa.EncodeSourceTrail(history)
			if !bytes.Equal(before, after) {
				t.Fatal("current activation reinterpreted recorded history")
			}
			unmasked, err := f.store.EvaluateAndTraceAssociatedSupersessionLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "history-off"), "peer", 10, 1024, packing, ds)
			if err != nil || !unmasked.Trail.Associated[0].Selected {
				t.Fatalf("deactivated support: %+v %v", unmasked, err)
			}
			on := activationTestTransition(t, f.source.ID, off.ID, declaration.ID, "reactivate", activationBaseUsec+2)
			if err := f.store.ApplySupersessionActivation(t.Context(), on); err != nil {
				t.Fatal(err)
			}
			_, revised, text := prepareLocalRevision(t, f.store, "predecessor", "later nonmatching support")
			if _, err := f.store.ActivateLocalItem(t.Context(), f.source.ID, "predecessor", revised.ID, text); err != nil {
				t.Fatal(err)
			}
			later, err := f.store.EvaluateAndTraceAssociatedSupersessionLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "later-pin"), "peer", 10, 1024, packing, ds)
			if err != nil || !later.Trail.Associated[0].Selected || later.Associated[0].Text != "later nonmatching support" {
				t.Fatalf("item name mistaken for revision: %+v %v", later, err)
			}
			// Retired predecessor bytes are unavailable, but canonical pins/history still verify.
			if _, err := f.store.GetSourceTrail(t.Context(), original.Trail.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAssociatedSupersessionResealedCorruption(t *testing.T) {
	for _, mutation := range []string{"suppression", "attribution", "digest", "size", "activation"} {
		t.Run(mutation, func(t *testing.T) {
			f := newV4Fixture(t)
			f.activate(t)
			ds := []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "predecessor", Basis: "qualification", Author: "test"}}
			r, err := f.store.EvaluateAndTraceAssociatedSupersessionLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "damage"), "peer", 10, uint64(len("sharedterm peer evidence")), mousa.PackingOriginal, ds)
			if err != nil {
				t.Fatal(err)
			}
			changed := r.Trail
			switch mutation {
			case "suppression":
				changed.Associated[0].Omission = "budget"
			case "attribution":
				changed.Associated[0].FromItem = "successor"
			case "digest":
				changed.Associated[0].ContentSHA256 = mousa.SHA256{1}
			case "size":
				changed.Associated[0].TextBytes++
			case "activation":
				id := mousa.SupersessionActivationID{99}
				changed.Supersession.ActivationID = &id
			}
			changed.ID, err = mousa.NewSourceTrailID(changed)
			if err != nil {
				t.Fatal(err)
			}
			data, err := mousa.EncodeSourceTrail(changed)
			if err != nil {
				t.Fatal("mutation must reach canonical verification", err)
			}
			if _, err := mousa.DecodeSourceTrail(data); err != nil {
				t.Fatal(err)
			}
			commitV4Trail(t, f.store, changed)
			if _, err := f.store.GetSourceTrail(t.Context(), changed.ID); !IsCode(err, CodeIntegrity) {
				t.Fatalf("resealed damage accepted: %v", err)
			}
			path := f.store.path
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(t.Context(), path)
			if reopened != nil {
				reopened.Close()
			}
			if !IsCode(err, CodeIntegrity) {
				t.Fatalf("startup accepted resealed damage: %v", err)
			}
		})
	}
}

func TestAssociatedSupersessionStrictCodecAndRollback(t *testing.T) {
	f := newV4Fixture(t)
	f.activate(t)
	ds := []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "predecessor", Basis: "qualification", Author: "test"}}
	r, err := f.store.EvaluateAndTraceAssociatedSupersessionLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "codec"), "peer", 10, 1024, mousa.PackingOriginal, ds)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := mousa.EncodeSourceTrail(r.Trail)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{`"selected":false`, `"selected":0`}, {`"selected":false`, `"selected":null`}, {`"selected":false,`, ``}, {`"omission":"superseded"`, `"omission":null`}, {`"schema":"mousa.source_trail.v12"`, `"schema":"mousa.source_trail.v4"`}, {`"associated":[`, `"associated":null,"unused":[`}} {
		damaged := strings.Replace(string(encoded), pair[0], pair[1], 1)
		if damaged == string(encoded) {
			t.Fatal("missing codec mutation anchor")
		}
		if _, err := mousa.DecodeSourceTrail([]byte(damaged)); err == nil {
			t.Fatalf("strict codec accepted %s", damaged)
		}
	}
	var decisions, trails int
	counts := func() (int, int) {
		var d, n int
		if err := f.store.db.QueryRow(`SELECT count(*) FROM policy_decisions`).Scan(&d); err != nil {
			t.Fatal(err)
		}
		if err := f.store.db.QueryRow(`SELECT count(*) FROM source_trails`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return d, n
	}
	decisions, trails = counts()
	if _, err := f.store.db.Exec(`CREATE TRIGGER fail_composed BEFORE INSERT ON source_trails BEGIN SELECT RAISE(ABORT, 'forced composed rollback'); END`); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.EvaluateAndTraceAssociatedSupersessionLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "rollback"), "peer", 10, 1024, mousa.PackingOriginal, ds)
	if err == nil {
		t.Fatal("forced insertion succeeded")
	}
	if d, n := counts(); d != decisions || n != trails {
		t.Fatal("partial decision/trail persisted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = f.store.EvaluateAndTraceAssociatedSupersessionLexical(ctx, testEvaluationRequest(t, f.source.ID, "cancelled"), "peer", 10, 1024, mousa.PackingOriginal, ds)
	if err == nil {
		t.Fatal("cancelled query succeeded")
	}
	if d, n := counts(); d != decisions || n != trails {
		t.Fatal("cancelled query persisted")
	}
}
