package mousa

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
)

// validateAssociatedSupersessionProfile bounds recorded work and keeps each target's
// attribution contiguous. Target revision/order and declaring ancestry need the store.
func (t SourceTrail) validateAssociatedSupersessionProfile() error {
	invalid := func() error { return errors.New("invalid v12 lexical association/supersession profile") }
	if t.Outcome != string(PolicyOutcomeAllow) || t.Token != nil || t.Vector != nil || t.Hybrid != nil || t.Supersession == nil || len(t.Associated)+len(t.AssociationOmissions) == 0 || len(t.Associated) > MaxAssociationPassages || len(t.AssociationOmissions) > MaxAssociationDeclarations {
		return invalid()
	}
	selected := false
	for _, r := range t.Candidates {
		selected = selected || r.Selected
	}
	if !selected {
		return invalid()
	}
	targets := map[string]AssociationDeclaration{}
	seen := map[SegmentID]bool{}
	last := ""
	for _, r := range t.Associated {
		if seen[r.SegmentID] {
			return invalid()
		}
		seen[r.SegmentID] = true
		d := AssociationDeclaration{r.FromItem, r.ToItem, r.Basis, r.Author}
		if old, ok := targets[r.ToItem]; ok && (old != d || last != r.ToItem) {
			return invalid()
		}
		targets[r.ToItem] = d
		last = r.ToItem
	}
	if len(targets) > MaxAssociationTargets {
		return invalid()
	}
	return nil
}

func decodeAssociatedSupersessionTrail(data []byte) (SourceTrail, error) {
	var t SourceTrail
	if err := decodeStrictVectorJSON(data, &t); err != nil {
		return t, err
	}
	// Compare the complete typed shape to its canonical encoding. This rejects
	// absent required values, explicit nulls and case-folded/unknown/empty optional
	// members at every depth, without conflating booleans and JSON numbers.
	canonical, err := encodeJSONContract(t, "composed trail")
	if err != nil {
		return t, err
	}
	decode := func(data []byte) (any, error) {
		d := json.NewDecoder(bytes.NewReader(data))
		d.UseNumber()
		var v any
		err := d.Decode(&v)
		return v, err
	}
	actual, err := decode(data)
	if err != nil {
		return t, err
	}
	expected, err := decode(canonical)
	if err != nil {
		return t, err
	}
	if !reflect.DeepEqual(actual, expected) {
		return t, errors.New("v12 requires the exact canonical member and JSON type shape")
	}
	return t, t.Validate()
}
