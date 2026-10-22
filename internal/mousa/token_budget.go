package mousa

import "errors"

// TokenBudget contains caller declarations, not authenticated model-window facts.
type TokenBudget struct {
	Cap           uint64 `json:"cap"`
	Window        uint64 `json:"window"`
	PromptReserve uint64 `json:"prompt_reserve"`
	OutputReserve uint64 `json:"output_reserve"`
}

// Available subtracts reservations before combining the window with the explicit cap.
func (b TokenBudget) Available() (uint64, error) {
	const maxDeclared = 1000000000
	if b.Cap == 0 || b.Cap > 524288 || b.Window > maxDeclared || b.PromptReserve > maxDeclared || b.OutputReserve > maxDeclared {
		return 0, errors.New("token cap must be 1..524288; window and reservations must be 0..1000000000")
	}
	if b.Window == 0 {
		if b.PromptReserve != 0 || b.OutputReserve != 0 {
			return 0, errors.New("token reservations require a positive context window")
		}
		return b.Cap, nil
	}
	if b.PromptReserve > b.Window || b.OutputReserve > b.Window-b.PromptReserve {
		return 0, errors.New("token reservations exceed the declared window")
	}
	return min(b.Cap, b.Window-b.PromptReserve-b.OutputReserve), nil
}
