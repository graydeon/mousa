package sqlite

import (
	"context"
	"database/sql"

	"github.com/graydeon/mousa/internal/mousa"
)

// SupersessionCurrent is verified current administration, without document text.
// Null activation and declaration distinguish no history from deactivation.
type SupersessionCurrent struct {
	State       string                         `json:"state"`
	Activation  *mousa.SupersessionActivation  `json:"activation"`
	Declaration *mousa.SupersessionDeclaration `json:"declaration"`
}

// SupersessionInspection withholds Current unless fresh retrieval authorization allows it.
type SupersessionInspection struct {
	Decision mousa.PolicyDecision
	Current  *SupersessionCurrent
}

// InspectSupersession authorizes before consulting administration in one owning
// writer snapshot. It may persist a decision receipt, but never changes history.
func (store *Store) InspectSupersession(ctx context.Context, request mousa.PolicyEvaluationRequest) (SupersessionInspection, error) {
	if err := store.requireWritable("inspect supersession"); err != nil {
		return SupersessionInspection{}, err
	}
	if err := request.Validate(); err != nil {
		return SupersessionInspection{}, wrap(CodeInvalidRecord, "inspect supersession", err)
	}
	var result SupersessionInspection
	err := store.writeImmediate(ctx, "inspect supersession", func(conn *sql.Conn) error {
		var err error
		result.Decision, err = evaluateSourceRetrieval(ctx, conn, request, false)
		if err != nil || result.Decision.Outcome != mousa.PolicyOutcomeAllow {
			return err
		}
		consultation, err := readSupersessionConsultation(ctx, conn, request.SourceID)
		if err != nil {
			return err
		}
		result.Current = &SupersessionCurrent{State: "no_history"}
		if consultation.state == nil {
			return nil
		}
		activation, err := readSupersessionActivationRow(ctx, conn, consultation.state.CurrentActivationID)
		if err != nil {
			return err
		}
		result.Current.State = "deactivated"
		result.Current.Activation = &activation
		result.Current.Declaration = consultation.declaration
		if consultation.declaration != nil {
			result.Current.State = "active"
		}
		return nil
	})
	if err != nil {
		return SupersessionInspection{}, err
	}
	return result, nil
}
