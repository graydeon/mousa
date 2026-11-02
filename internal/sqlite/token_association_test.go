package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestTokenAssociatedPrimaryPriorityAndRecount(t *testing.T) {
	t.Parallel()
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		for _, policy := range []string{"original", "dedup"} {
			t.Run(packing+"/"+policy, func(t *testing.T) {
				t.Parallel()
				options := tokenOptions(t)
				store := openLexicalStore(t)
				defer store.Close()
				source, _, _ := seedAssociatedItems(t, store)
				options.Associations = []mousa.AssociationDeclaration{drainDeclaration()}
				ctx := context.Background()
				got, text, err := store.EvaluateAndTraceTokenLexical(ctx, testEvaluationRequest(t, source, "combined"), "restart", policy, 8192, packing, options)
				if err != nil {
					t.Fatal(err)
				}
				if got.Trail.Schema != "mousa.source_trail.v11" || !strings.Contains(text, "Stopping a server does not drain connections.") {
					t.Fatalf("missing declared qualification: %+v %q", got.Trail, text)
				}
				history, err := store.InspectSourceTrailWithTokens(ctx, testEvaluationRequest(t, source, "recount"), got.Trail.ID, tokenOptions(t).Counter)
				if err != nil || history.Historical.TokenVerification != "official_recount_verified" {
					t.Fatalf("recount: %+v %v", history, err)
				}
				short := tokenOptions(t)
				short.Associations = options.Associations
				short.Budget.Cap = 1
				omitted, empty, err := store.EvaluateAndTraceTokenLexical(ctx, testEvaluationRequest(t, source, "primary-omitted"), "restart", policy, 8192, packing, short)
				if err != nil {
					t.Fatal(err)
				}
				if empty != "" || omitted.Trail.Schema != mousa.SourceTrailSchemaV7 || len(omitted.Associated) != 0 {
					t.Fatalf("unselected primary triggered expansion: %+v %q", omitted.Trail, empty)
				}
			})
		}
	}
}
