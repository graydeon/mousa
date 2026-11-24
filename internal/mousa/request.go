package mousa

import (
	"bytes"
	"errors"
	"strings"
)

// Request audit records bind every served request to its authenticated
// principal, operation, and terminal outcome. They carry no request text,
// bodies, credentials, or raw errors.
const (
	LogKindRequestAdmission = "request_admission"
	LogKindRequestOutcome   = "request_outcome"
)

// RequestOperation is the closed set of served operations.
type RequestOperation string

const (
	RequestOperationQuery    RequestOperation = "query"
	RequestOperationNote     RequestOperation = "note"
	RequestOperationDocument RequestOperation = "document"
)

func (operation RequestOperation) valid() bool {
	switch operation {
	case RequestOperationQuery, RequestOperationNote, RequestOperationDocument:
		return true
	}
	return false
}

// RequestState is the closed set of terminal request outcomes. Exactly one
// terminal outcome is appended per admitted request.
type RequestState string

const (
	RequestStateCommitted   RequestState = "committed"
	RequestStateNoOp        RequestState = "no_op"
	RequestStateDenied      RequestState = "denied"
	RequestStateInvalid     RequestState = "invalid"
	RequestStateFailed      RequestState = "failed"
	RequestStateCanceled    RequestState = "canceled"
	RequestStateInterrupted RequestState = "interrupted"
)

// RequestErrorCode is the closed set of limited request error codes. Only
// committed and no-op requests carry an empty code.
type RequestErrorCode string

const (
	RequestErrorUnauthorized   RequestErrorCode = "unauthorized"
	RequestErrorSourceDenied   RequestErrorCode = "source_denied"
	RequestErrorInvalidRequest RequestErrorCode = "invalid_request"
	RequestErrorTooLarge       RequestErrorCode = "too_large"
	RequestErrorBusy           RequestErrorCode = "busy"
	RequestErrorConflict       RequestErrorCode = "conflict"
	RequestErrorInternal       RequestErrorCode = "internal"
	RequestErrorCanceled       RequestErrorCode = "canceled"
	RequestErrorInterrupted    RequestErrorCode = "interrupted"
)

func (state RequestState) allows(code RequestErrorCode) bool {
	switch state {
	case RequestStateCommitted, RequestStateNoOp:
		return code == ""
	case RequestStateDenied:
		return code == RequestErrorUnauthorized || code == RequestErrorSourceDenied
	case RequestStateInvalid:
		return code == RequestErrorInvalidRequest || code == RequestErrorTooLarge
	case RequestStateFailed:
		return code == RequestErrorBusy || code == RequestErrorConflict || code == RequestErrorInternal
	case RequestStateCanceled:
		return code == RequestErrorCanceled
	case RequestStateInterrupted:
		return code == RequestErrorInterrupted
	}
	return false
}

// ClientMetadata is bounded caller-reported harness identity. It is never
// marked verified and carries no request semantics.
type ClientMetadata struct {
	Harness string `json:"harness,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Model   string `json:"model,omitempty"`
	RunID   string `json:"run_id,omitempty"`
}

// clientMetadataByteLimit bounds each reported client label.
const clientMetadataByteLimit = 96

func (metadata ClientMetadata) Validate() error {
	fields := []struct {
		name  string
		value string
	}{
		{"harness", metadata.Harness},
		{"agent", metadata.Agent},
		{"model", metadata.Model},
		{"run_id", metadata.RunID},
	}
	for _, field := range fields {
		if len(field.value) > clientMetadataByteLimit {
			return newValidationError(field.name, ValidationCodeInvalidValue, "client label exceeds 96 bytes", nil)
		}
		if strings.IndexFunc(field.value, func(r rune) bool { return r < 0x20 || r > 0x7e }) >= 0 {
			return newValidationError(field.name, ValidationCodeInvalidValue, "client label must be printable ASCII", nil)
		}
	}
	return nil
}

// ValidPrincipalID reports whether id is a stable configured principal
// identifier accepted by request records.
func ValidPrincipalID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		b := id[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		case i > 0 && (b == '.' || b == '_' || b == '-'):
		default:
			return false
		}
	}
	return true
}

// ValidRequestID reports whether id is a server-issued request identifier as
// produced by rand.Text: 26 uppercase base32 characters.
func ValidRequestID(id string) bool {
	if len(id) != 26 {
		return false
	}
	for i := 0; i < len(id); i++ {
		b := id[i]
		if !(b >= 'A' && b <= 'Z' || b >= '2' && b <= '7') {
			return false
		}
	}
	return true
}

// RequestAdmission records one admitted request before serving begins. The
// principal is present only when the request authenticated.
type RequestAdmission struct {
	Authenticated  bool             `json:"authenticated"`
	Client         ClientMetadata   `json:"client,omitzero"`
	Operation      RequestOperation `json:"operation"`
	Principal      string           `json:"principal,omitempty"`
	ReceivedAtUsec int64            `json:"received_at_usec"`
	RequestID      string           `json:"request_id"`
}

func (admission RequestAdmission) Validate() error {
	if !ValidRequestID(admission.RequestID) {
		return errors.New("request admission has an invalid request id")
	}
	if !admission.Operation.valid() {
		return errors.New("request admission has an invalid operation")
	}
	if admission.ReceivedAtUsec <= 0 || admission.ReceivedAtUsec > maxJSONSafeInteger {
		return errors.New("request admission has an invalid receive time")
	}
	if admission.Authenticated {
		if !ValidPrincipalID(admission.Principal) {
			return errors.New("request admission has an invalid principal")
		}
	} else if admission.Principal != "" || admission.Client != (ClientMetadata{}) {
		return errors.New("unauthenticated request admission carries identity")
	}
	return admission.Client.Validate()
}

// RequestEvidence names the canonical rows a request outcome produced. At most
// one field is set, matching the operation.
type RequestEvidence struct {
	ItemChangeID *SHA256        `json:"item_change_id,omitempty"`
	NoteID       *SHA256        `json:"note_id,omitempty"`
	TrailID      *SourceTrailID `json:"trail_id,omitempty"`
}

func (evidence RequestEvidence) validate() error {
	switch {
	case evidence.TrailID != nil && *evidence.TrailID == (SourceTrailID{}):
		return errors.New("request evidence has a zero trail id")
	case evidence.NoteID != nil && *evidence.NoteID == (SHA256{}):
		return errors.New("request evidence has a zero note id")
	case evidence.ItemChangeID != nil && *evidence.ItemChangeID == (SHA256{}):
		return errors.New("request evidence has a zero item change id")
	}
	return nil
}

func (evidence RequestEvidence) count() int {
	count := 0
	for _, set := range []bool{evidence.TrailID != nil, evidence.NoteID != nil, evidence.ItemChangeID != nil} {
		if set {
			count++
		}
	}
	return count
}

// RequestOutcome records the single terminal disposition of an admitted
// request at the global log sequence it was committed under.
type RequestOutcome struct {
	ErrorCode      RequestErrorCode `json:"error_code,omitempty"`
	Evidence       RequestEvidence  `json:"evidence,omitzero"`
	FinishedAtUsec int64            `json:"finished_at_usec"`
	RequestID      string           `json:"request_id"`
	Seq            uint64           `json:"seq"`
	State          RequestState     `json:"state"`
}

func (outcome RequestOutcome) Validate() error {
	if !ValidRequestID(outcome.RequestID) {
		return errors.New("request outcome has an invalid request id")
	}
	if outcome.Seq == 0 || outcome.Seq > maxJSONSafeInteger {
		return errors.New("request outcome has an invalid sequence")
	}
	if outcome.FinishedAtUsec <= 0 || outcome.FinishedAtUsec > maxJSONSafeInteger {
		return errors.New("request outcome has an invalid finish time")
	}
	if !outcome.State.allows(outcome.ErrorCode) {
		return errors.New("request outcome has an invalid state and error code")
	}
	return outcome.Evidence.validate()
}

// RequestTrailState derives the outcome pairing of a stored query trail: a
// scope whose named sources all resolve denied is a denied outcome; every
// other stored trail commits.
func RequestTrailState(trail Trail) (RequestState, RequestErrorCode) {
	if len(trail.Sources) > 0 {
		denied := true
		for _, source := range trail.Sources {
			if source.Outcome != "deny" {
				denied = false
				break
			}
		}
		if denied {
			return RequestStateDenied, RequestErrorSourceDenied
		}
	}
	return RequestStateCommitted, ""
}

// CheckRequestOutcomePair verifies an outcome belongs to its admission: the
// request id, actor constraint, and the operation-appropriate evidence.
func CheckRequestOutcomePair(admission RequestAdmission, outcome RequestOutcome) error {
	if err := admission.Validate(); err != nil {
		return err
	}
	if err := outcome.Validate(); err != nil {
		return err
	}
	if outcome.RequestID != admission.RequestID {
		return errors.New("request outcome belongs to another request")
	}
	if outcome.FinishedAtUsec < admission.ReceivedAtUsec {
		return errors.New("request outcome precedes its admission")
	}
	// An unauthenticated admission may only end unauthorized or interrupted.
	if !admission.Authenticated && outcome.State != RequestStateInterrupted &&
		!(outcome.State == RequestStateDenied && outcome.ErrorCode == RequestErrorUnauthorized) {
		return errors.New("unauthenticated request claims a served outcome")
	}
	evidence := outcome.Evidence
	// An unauthorized outcome belongs only to an unauthenticated request
	// and never links evidence.
	if outcome.ErrorCode == RequestErrorUnauthorized &&
		(admission.Authenticated || evidence.count() != 0) {
		return errors.New("unauthorized outcome claims a principal or evidence")
	}
	switch outcome.State {
	case RequestStateCommitted:
		if evidence.count() != 1 {
			return errors.New("committed request must link exactly one evidence record")
		}
		switch admission.Operation {
		case RequestOperationQuery:
			if evidence.TrailID == nil {
				return errors.New("committed query must link its trail")
			}
		case RequestOperationNote:
			if evidence.NoteID == nil {
				return errors.New("committed note must link its note")
			}
		case RequestOperationDocument:
			if evidence.ItemChangeID == nil {
				return errors.New("committed document must link its item change")
			}
		}
	case RequestStateNoOp:
		switch admission.Operation {
		case RequestOperationQuery:
			return errors.New("queries cannot record a no-op outcome")
		case RequestOperationNote:
			if evidence.count() != 1 || evidence.NoteID == nil {
				return errors.New("a no-op note must link the original note")
			}
		case RequestOperationDocument:
			if evidence.count() != 0 {
				return errors.New("a no-op document carries no evidence")
			}
		}
	case RequestStateDenied:
		if evidence.NoteID != nil || evidence.ItemChangeID != nil {
			return errors.New("denied request links write evidence")
		}
		if evidence.TrailID != nil && admission.Operation != RequestOperationQuery {
			return errors.New("denied non-query request links a trail")
		}
	case RequestStateInvalid, RequestStateFailed, RequestStateCanceled, RequestStateInterrupted:
		if evidence.count() != 0 {
			return errors.New("unserved request links evidence")
		}
	}
	return nil
}

func EncodeRequestAdmission(admission RequestAdmission) ([]byte, error) {
	if err := admission.Validate(); err != nil {
		return nil, err
	}
	return canonicalTrailJSON(admission)
}

func DecodeRequestAdmission(data []byte) (RequestAdmission, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return RequestAdmission{}, err
	}
	var admission RequestAdmission
	if err := decodeJSONContract(data, &admission); err != nil {
		return RequestAdmission{}, err
	}
	canonical, err := EncodeRequestAdmission(admission)
	if err != nil {
		return RequestAdmission{}, err
	}
	if !bytes.Equal(canonical, data) {
		return RequestAdmission{}, errors.New("request admission bytes are not canonical")
	}
	return admission, nil
}

func EncodeRequestOutcome(outcome RequestOutcome) ([]byte, error) {
	if err := outcome.Validate(); err != nil {
		return nil, err
	}
	return canonicalTrailJSON(outcome)
}

func DecodeRequestOutcome(data []byte) (RequestOutcome, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return RequestOutcome{}, err
	}
	var outcome RequestOutcome
	if err := decodeJSONContract(data, &outcome); err != nil {
		return RequestOutcome{}, err
	}
	canonical, err := EncodeRequestOutcome(outcome)
	if err != nil {
		return RequestOutcome{}, err
	}
	if !bytes.Equal(canonical, data) {
		return RequestOutcome{}, errors.New("request outcome bytes are not canonical")
	}
	return outcome, nil
}
