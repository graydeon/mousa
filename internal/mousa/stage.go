package mousa

// Stage names one measured boundary of a served request. The set is closed so
// observers and stream consumers share one vocabulary.
type Stage string

const (
	StageReceive   Stage = "receive"
	StageQueue     Stage = "queue"
	StageAdmission Stage = "admission"
	StageAccess    Stage = "access"
	StageRetrieval Stage = "retrieval"
	StageRank      Stage = "rank"
	StagePack      Stage = "pack"
	StageIngest    Stage = "ingest"
	StageRepresent Stage = "represent"
	StageIndex     Stage = "index"
	StageNote      Stage = "note"
	StageCitations Stage = "citations"
	StageCommit    Stage = "commit"
	StageOutcome   Stage = "outcome"
	StageResponse  Stage = "response"
)

// StagePhase is the closed set of observation kinds within a stage.
type StagePhase string

const (
	StagePhaseStart    StagePhase = "start"
	StagePhaseFinish   StagePhase = "finish"
	StagePhaseError    StagePhase = "error"
	StagePhaseSkip     StagePhase = "skip"
	StagePhaseRetry    StagePhase = "retry"
	StagePhaseTerminal StagePhase = "terminal"
)

// StageObservation is one measured event at a request boundary. It carries no
// request text, record bodies, or error messages.
type StageObservation struct {
	Stage        Stage
	Phase        StagePhase
	AtUsec       int64
	DurationUsec int64
	Attempt      int
}
