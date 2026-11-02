package sqlite

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestTokenAssociatedResolutionAndUnfiredControl(t *testing.T) {
	t.Parallel()
	f := newV4Fixture(t)
	defer f.store.Close()
	seedLocalItem(t, f.store, f.source, "qualification", "Close idle sockets explicitly.")
	seedLocalItem(t, f.store, f.source, "copy", "Close idle sockets explicitly.")
	seedLocalItem(t, f.store, f.source, "next", "Do not expand this note transitively.")
	seedLocalItem(t, f.store, f.source, "gone", "Retired note.")
	if _, err := f.store.DeleteLocalItem(t.Context(), f.source.ID, "gone"); err != nil {
		t.Fatal(err)
	}
	sequence := uint64(1)
	foreign := testPushBatch(t, "foreign-notes", &sequence)
	if err := f.store.ApplyIngest(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	seedLocalItem(t, f.store, foreign.Source, "foreign-only", "A foreign payload must not be released.")
	declaration := func(from, to string) mousa.AssociationDeclaration {
		return mousa.AssociationDeclaration{FromItem: from, ToItem: to, Basis: "explicit qualification", Author: "example.operator"}
	}
	declarations := []mousa.AssociationDeclaration{
		declaration("successor", "qualification"), declaration("qualification", "next"),
		declaration("successor", "missing"), declaration("successor", "gone"),
		declaration("successor", "foreign-only"), declaration("successor", "late"), declaration("peer", "qualification"),
	}
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		query := func(name string, declarations []mousa.AssociationDeclaration) (TracedLexicalResult, string) {
			options := tokenOptions(t)
			options.Associations = declarations
			got, text, err := f.store.EvaluateAndTraceTokenLexical(t.Context(), testEvaluationRequest(t, f.source.ID, name+packing), "sharedterm", "original", 8192, packing, options)
			if err != nil {
				t.Fatal(err)
			}
			return got, text
		}
		ordinary, plain := query("ordinary", nil)
		unfired, text := query("unfired", []mousa.AssociationDeclaration{declaration("unselected", "qualification")})
		if ordinary.Trail.PacketID != unfired.Trail.PacketID || plain != text || unfired.Trail.Schema != mousa.SourceTrailSchemaV7 {
			t.Fatal("unfired declarations changed ordinary token selection")
		}
		got, text := query("resolved", declarations)
		if len(got.Associated) != 1 || !got.Trail.Associated[0].Selected || !strings.Contains(text, "Close idle sockets explicitly.") || strings.Contains(text, "transitively") || strings.Contains(text, "foreign payload") {
			t.Fatal("depth/source resolution changed", text)
		}
		var reasons []string
		for _, r := range got.Trail.AssociationOmissions {
			reasons = append(reasons, r.Reason)
		}
		want := []string{mousa.AssociationTargetUnknown, mousa.AssociationTargetInactive, mousa.AssociationTargetUnknown, mousa.AssociationFanOut, mousa.AssociationDuplicateTarget}
		if !reflect.DeepEqual(reasons, want) {
			t.Fatalf("relationship order/reasons: %v", reasons)
		}
		missing, _ := query("missing-only", []mousa.AssociationDeclaration{declaration("successor", "missing")})
		if missing.Trail.Schema != mousa.SourceTrailSchemaV11 || len(missing.Associated) != 0 || len(missing.Trail.Token.Associations.ProposedTokens) != 0 {
			t.Fatal("omission-only stage lost producing facts")
		}
		same, _ := query("same-segment", []mousa.AssociationDeclaration{declaration("successor", "peer")})
		if len(same.Trail.Associated) != 1 || same.Trail.Associated[0].Omission != "duplicate" {
			t.Fatal("associated primary segment was released twice")
		}
		equal, _ := query("byte-equal", []mousa.AssociationDeclaration{declaration("successor", "qualification"), declaration("successor", "copy")})
		if !equal.Trail.Associated[0].Selected || (equal.Trail.Associated[1].Omission == "duplicate") != (packing == mousa.PackingExactV1) {
			t.Fatal("associated byte-equal policy changed")
		}
	}
}

func TestTokenAssociatedOversizedResolutionRollsBack(t *testing.T) {
	t.Parallel()
	for _, size := range []int{100, mousa.MaxAssociationPassages + 2} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			t.Parallel()
			store := openLexicalStore(t)
			defer store.Close()
			source := seedLargeTargetItem(t, store, size)
			options := tokenOptions(t)
			options.Associations = []mousa.AssociationDeclaration{drainDeclaration()}
			before := freshAuditCounts(t, store)
			got, text, err := store.EvaluateAndTraceTokenLexical(t.Context(), testEvaluationRequest(t, source, "oversized-combined"), "Perform", "original", 8192, mousa.PackingExactV1, options)
			if err == nil || !strings.Contains(err.Error(), "passage rows") || got.Trail.ID != (mousa.SourceTrailID{}) || text != "" || !reflect.DeepEqual(before, freshAuditCounts(t, store)) {
				t.Fatal("oversized considered set produced partial success", err)
			}
			// Native byte-only resolution keeps its established consideration bound.
			byteOnly := associateQuery(t, store, source, "byte-control", "Perform", 1<<20, mousa.PackingExactV1, options.Associations)
			want := min(size, mousa.MaxAssociationPassages)
			if len(byteOnly.Associated) != want {
				t.Fatal("byte-only target bound changed")
			}
			if size > mousa.MaxAssociationPassages && byteOnly.Trail.AssociationOmissions[0].Reason != mousa.AssociationTargetTruncated {
				t.Fatal("native truncation meaning changed")
			}
		})
	}
}
