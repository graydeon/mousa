package sqlite

import (
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestTokenAssociatedSupersessionHistoryAndBudgets(t *testing.T) {
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			_, state := f.activate(t)
			declarations := []mousa.AssociationDeclaration{
				{FromItem: "predecessor", ToItem: "peer", Basis: "must not fire", Author: "test"},
				{FromItem: "peer", ToItem: "predecessor", Basis: "withheld support", Author: "test"},
				{FromItem: "peer", ToItem: "twin", Basis: "surviving identical support", Author: "test"},
			}
			query := func(name, expression string, bytes, tokens uint64, ds []mousa.AssociationDeclaration) (TracedLexicalResult, string) {
				o := tokenOptions(t)
				o.Supersession, o.Associations, o.Budget.Cap = true, ds, tokens
				got, text, err := f.store.EvaluateAndTraceTokenLexical(t.Context(), testEvaluationRequest(t, f.source.ID, name), expression, "original", bytes, packing, o)
				if err != nil {
					t.Fatal(err)
				}
				return got, text
			}
			full, text := query("full", "peer", 1024, 4096, declarations)
			if full.Trail.Schema != mousa.SourceTrailSchemaV13 || len(full.Trail.Associated) != 2 || len(full.Associated) != 2 {
				t.Fatalf("missing composition: %+v", full.Trail)
			}
			old, twin := full.Trail.Associated[0], full.Trail.Associated[1]
			if old.Selected || old.Omission != "superseded" || old.DuplicateOf != "" || full.Associated[0].Text != "" || full.Trail.Token.Associations.ProposedTokens[0] != nil || !twin.Selected {
				t.Fatal("suppression did not precede duplicate and budgets")
			}
			if strings.Count(text, "sharedterm predecessor evidence") != 1 {
				t.Fatal("withheld predecessor seeded duplicate suppression", text)
			}
			for _, boundary := range []struct {
				name          string
				bytes, tokens uint64
				omission      string
			}{
				{"exact", full.Trail.UsedBytes, full.Trail.Token.MeasuredTokens, ""},
				{"bytes-short", full.Trail.UsedBytes - 1, full.Trail.Token.MeasuredTokens, "bytes"},
				{"tokens-short", full.Trail.UsedBytes, full.Trail.Token.MeasuredTokens - 1, "tokens"},
			} {
				got, rendered := query(boundary.name, "peer", boundary.bytes, boundary.tokens, declarations)
				if got.Trail.Associated[0].Omission != "superseded" || got.Trail.Token.Associations.ProposedTokens[0] != nil || got.Trail.Associated[1].Omission != boundary.omission {
					t.Fatalf("%s: %+v", boundary.name, got.Trail.Associated)
				}
				if boundary.name == "exact" && rendered != text {
					t.Fatal("exact fit changed rendering")
				}
			}
			plain, _ := query("plain", "peer", 1024, 4096, nil)
			unfired, _ := query("unfired", "predecessor", 1024, 4096, declarations[:1])
			if plain.Trail.Schema != mousa.SourceTrailSchemaV8 || unfired.Trail.Schema != mousa.SourceTrailSchemaV8 || len(unfired.Associated) != 0 {
				t.Fatal("unfired declarations changed legacy profile")
			}
			missing, _ := query("missing", "peer", 1024, 4096, []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "missing", Basis: "unknown", Author: "test"}})
			if missing.Trail.Schema != mousa.SourceTrailSchemaV13 || len(missing.Trail.AssociationOmissions) != 1 {
				t.Fatal("omission-only facts lost")
			}
			history, err := f.store.InspectSourceTrailWithTokens(t.Context(), testEvaluationRequest(t, f.source.ID, "recount"), full.Trail.ID, tokenOptions(t).Counter)
			if err != nil || history.Historical == nil || history.Historical.Schema != mousa.SourceTrailSchemaV13 || history.Historical.TokenVerification != "official_recount_verified" || len(history.Historical.Associated) != 2 {
				t.Fatalf("recount: %+v %v", history, err)
			}
			transition := activationTestTransition(t, f.source.ID, state.CurrentActivationID, mousa.SupersessionDeclarationID{}, "later deactivation", activationBaseUsec+1)
			if err := f.store.ApplySupersessionActivation(t.Context(), transition); err != nil {
				t.Fatal(err)
			}
			read, err := f.store.GetSourceTrail(t.Context(), full.Trail.ID)
			if err != nil || !reflect.DeepEqual(read, full.Trail) {
				t.Fatal("history reinterpreted producing pins", err)
			}
			reopened, err := Open(t.Context(), f.store.path)
			if err != nil {
				t.Fatal("startup verification", err)
			}
			reopened.Close()
		})
	}
}

func TestTokenAssociatedSupersessionResealedMembership(t *testing.T) {
	for _, mutation := range []struct{ name, reason string }{
		{"suppression", "associated suppression disagrees"},
		{"false-suppression", "associated suppression disagrees"},
		{"attribution", "declaring item contributed no selected"},
		{"size", "associated size disagrees"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			f.activate(t)
			options := tokenOptions(t)
			options.Supersession = true
			options.Associations = []mousa.AssociationDeclaration{
				{FromItem: "peer", ToItem: "predecessor", Basis: "withheld", Author: "test"},
				{FromItem: "peer", ToItem: "twin", Basis: "surviving", Author: "test"},
			}
			got, _, err := f.store.EvaluateAndTraceTokenLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "damage"), "peer", "original", uint64(len("sharedterm peer evidence")), mousa.PackingOriginal, options)
			if err != nil {
				t.Fatal(err)
			}
			changed := got.Trail
			switch mutation.name {
			case "suppression":
				changed.Associated[0].Omission = "bytes"
			case "false-suppression":
				changed.Associated[1].Omission = "superseded"
			case "attribution":
				changed.Associated[0].FromItem = "successor"
			case "size":
				changed.Associated[0].TextBytes++
			}
			changed = rebindTokenRecord(t, changed)
			data, err := mousa.EncodeSourceTrail(changed)
			if err != nil {
				t.Fatal("mutation must reach canonical ancestry verification", err)
			}
			if _, err := mousa.DecodeSourceTrail(data); err != nil {
				t.Fatal("rehashed fixture failed structural prerequisite", err)
			}
			commitV4Trail(t, f.store, changed)
			if _, err := f.store.GetSourceTrail(t.Context(), changed.ID); !IsCode(err, CodeIntegrity) || !strings.Contains(err.Error(), mutation.reason) {
				t.Fatalf("rehashed %s missed named invariant: %v", mutation.name, err)
			}
			path := f.store.path
			f.store.Close()
			reopened, err := Open(t.Context(), path)
			if reopened != nil {
				reopened.Close()
			}
			if !IsCode(err, CodeIntegrity) || !strings.Contains(err.Error(), mutation.reason) {
				t.Fatalf("startup missed %s: %v", mutation.name, err)
			}
		})
	}
}

func TestTokenAssociatedSupersessionLifecycleCompatibility(t *testing.T) {
	f := newV4Fixture(t)
	defer f.store.Close()
	declarations := []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "predecessor", Basis: "qualification", Author: "test"}}
	query := func(name, expression string, enabled bool, ds []mousa.AssociationDeclaration) TracedLexicalResult {
		options := tokenOptions(t)
		options.Supersession, options.Associations = enabled, ds
		got, _, err := f.store.EvaluateAndTraceTokenLexical(t.Context(), testEvaluationRequest(t, f.source.ID, name), expression, "original", 8192, mousa.PackingOriginal, options)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	unknown := query("no-history", "peer", true, declarations)
	if unknown.Trail.Schema != mousa.SourceTrailSchemaV13 || unknown.Trail.Supersession.ActivationID != nil || !unknown.Trail.Associated[0].Selected {
		t.Fatal("no administrative history changed supporting selection")
	}
	ordinary := query("false", "peer", false, declarations)
	if ordinary.Trail.Schema != mousa.SourceTrailSchemaV11 {
		t.Fatal("false supersession changed legacy token associations")
	}
	empty := query("empty-declarations", "peer", true, []mousa.AssociationDeclaration{})
	noMatch := query("no-match", "unmatchedterm", true, declarations)
	if empty.Trail.Schema != mousa.SourceTrailSchemaV8 || noMatch.Trail.Schema != mousa.SourceTrailSchemaV8 || len(noMatch.Candidates) != 0 {
		t.Fatal("unfired facts gratuitously versioned legacy path")
	}
	f.activate(t)
	active := query("active", "peer", true, declarations)
	if active.Trail.Associated[0].Omission != "superseded" || active.Associated[0].Text != "" {
		t.Fatal("active pin failed to withhold support")
	}
	_, revised, text := prepareLocalRevision(t, f.store, "predecessor", "later nonmatching support")
	if _, err := f.store.ActivateLocalItem(t.Context(), f.source.ID, "predecessor", revised.ID, text); err != nil {
		t.Fatal(err)
	}
	stale := query("stale-pin", "peer", true, declarations)
	if !stale.Trail.Associated[0].Selected || stale.Associated[0].Text != "later nonmatching support" {
		t.Fatal("stale exact pin suppressed a replacement revision")
	}
	if _, err := f.store.GetSourceTrail(t.Context(), active.Trail.ID); err != nil {
		t.Fatal("retired withheld text broke recorded history", err)
	}
	if _, err := f.store.DeleteLocalItem(t.Context(), f.source.ID, "predecessor"); err != nil {
		t.Fatal(err)
	}
	inactive := query("inactive", "peer", true, declarations)
	if len(inactive.Associated) != 0 || len(inactive.Trail.AssociationOmissions) != 1 || inactive.Trail.AssociationOmissions[0].Reason != mousa.AssociationTargetInactive {
		t.Fatal("inactive target was resurrected by its declaration")
	}
	withdrawVerifiedSource(t, f.store, f.source.ID, "deny-triple", 300)
	denied := query("denied", "peer", true, declarations)
	if denied.Trail.Schema != mousa.SourceTrailSchemaV8 || denied.Decision.Outcome != mousa.PolicyOutcomeDeny || len(denied.Candidates) != 0 || len(denied.Associated) != 0 {
		t.Fatal("fresh denial released composition facts")
	}
}
