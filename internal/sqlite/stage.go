package sqlite

import (
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

// Observed returns a shallow store clone that reports stage observations to
// observe. The clone shares the database handle; only the callback differs,
// so serving one request never changes another request's store.
func (store *Store) Observed(observe func(mousa.StageObservation)) *Store {
	clone := *store
	clone.observe = observe
	return &clone
}

// StartStage publishes a stage start and returns the closer that publishes its
// finish or, when passed an error, its failure with the elapsed duration. On
// an unobserved store it costs one nil check and returns a no-op.
func (store *Store) StartStage(stage mousa.Stage) func(error) {
	if store.observe == nil {
		return func(error) {}
	}
	start := time.Now()
	store.observe(mousa.StageObservation{Stage: stage, Phase: mousa.StagePhaseStart, AtUsec: start.UnixMicro()})
	return func(err error) {
		now := time.Now()
		phase := mousa.StagePhaseFinish
		if err != nil {
			phase = mousa.StagePhaseError
		}
		store.observe(mousa.StageObservation{
			Stage: stage, Phase: phase, AtUsec: now.UnixMicro(), DurationUsec: now.Sub(start).Microseconds(),
		})
	}
}

// ObserveMarker publishes a point observation — a skipped stage or a retry —
// for the attempts that have no duration. Attempt zero means unnumbered.
func (store *Store) ObserveMarker(stage mousa.Stage, phase mousa.StagePhase, attempt int) {
	if store.observe == nil {
		return
	}
	store.observe(mousa.StageObservation{
		Stage: stage, Phase: phase, AtUsec: time.Now().UnixMicro(), Attempt: attempt,
	})
}
