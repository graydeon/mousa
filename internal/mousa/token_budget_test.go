package mousa

import (
	"math"
	"testing"
)

func TestTokenBudgetReservations(t *testing.T) {
	for _, tc := range []struct {
		b    TokenBudget
		want uint64
		bad  bool
	}{
		{TokenBudget{Cap: 100}, 100, false},
		{TokenBudget{Cap: 100, Window: 200, PromptReserve: 60, OutputReserve: 80}, 60, false},
		{TokenBudget{Cap: 100, Window: 200, PromptReserve: 100, OutputReserve: 100}, 0, false},
		{TokenBudget{Cap: 100, Window: 20, PromptReserve: 10, OutputReserve: 11}, 0, true},
		{TokenBudget{Cap: 100, PromptReserve: 1}, 0, true},
		{TokenBudget{Cap: math.MaxUint64}, 0, true},
		{TokenBudget{Cap: 1, Window: math.MaxUint64, PromptReserve: math.MaxUint64, OutputReserve: 1}, 0, true},
		{TokenBudget{}, 0, true},
	} {
		got, err := tc.b.Available()
		if (err != nil) != tc.bad || !tc.bad && got != tc.want {
			t.Fatalf("%+v: got %d err %v want %d bad %v", tc.b, got, err, tc.want, tc.bad)
		}
	}
}
