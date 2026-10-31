package mousa

import (
	"encoding/json"
	"errors"
)

const SourceTrailSchemaV9 = "mousa.source_trail.v9"

// validateRetrievalSupersessionTrail reuses the producing mode's coverage, order and
// packing checks. Store verification additionally rederives exact membership from
// recorded administration and immutable ancestry, never current activation.
func (trail SourceTrail) validateRetrievalSupersessionTrail() error {
	if trail.Token != nil || trail.Supersession == nil || (trail.Vector == nil) == (trail.Hybrid == nil) || len(trail.Associated) != 0 || len(trail.AssociationOmissions) != 0 {
		return errors.New("v9 requires exactly one byte retrieval mode and supersession")
	}
	if err := trail.Supersession.Validate(); err != nil {
		return err
	}
	if trail.Supersession.Consulted != (trail.Outcome == string(PolicyOutcomeAllow)) {
		return errors.New("v9 consultation must agree with authorization")
	}
	if trail.Vector != nil {
		return trail.validateVectorTrail()
	}
	return trail.validateHybridTrail()
}

func decodeRetrievalSupersessionTrail(data []byte) (SourceTrail, error) {
	var fields map[string]json.RawMessage
	if err := decodeStrictVectorJSON(data, &fields); err != nil {
		return SourceTrail{}, err
	}
	keys := []string{"schema", "id", "request_id", "decision_id", "outcome", "budget_bytes", "used_bytes", "packet_id", "candidates", "packing_policy", "supersession"}
	_, vector := fields["vector"]
	_, hybrid := fields["hybrid"]
	if vector == hybrid {
		return SourceTrail{}, errors.New("v9 requires exactly one vector or hybrid member")
	}
	if vector {
		keys = append(keys, "vector")
	} else {
		keys = append(keys, "hybrid", "expression")
	}
	if _, err := vectorObject(data, keys...); err != nil {
		return SourceTrail{}, err
	}
	var trail SourceTrail
	if err := decodeStrictVectorJSON(data, &trail); err != nil {
		return trail, err
	}
	selection, err := decodeSupersessionSelection(fields["supersession"])
	if err != nil {
		return trail, err
	}
	trail.Supersession = selection
	return trail, trail.Validate()
}
