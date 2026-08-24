package mousa

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	IngestReceiptSchema    = "mousa.ingest_receipt.v1"
	SourceWithdrawalSchema = "mousa.source_withdrawal.v1"
	maxCheckpointBytes     = 4096
	maxProvenanceBytes     = 1024
	maxWithdrawalReason    = 4096
)

type Initiative string
type IngestForm string
type CollectionState string
type WithdrawalID [sha256.Size]byte

const (
	InitiativePush      Initiative      = "push"
	InitiativePull      Initiative      = "pull"
	FormItem            IngestForm      = "item"
	FormSnapshot        IngestForm      = "snapshot"
	FormStreamWindow    IngestForm      = "stream_window"
	CollectionActive    CollectionState = "active"
	CollectionWithdrawn CollectionState = "withdrawn"
)

type Coverage struct {
	StartUsec int64 `json:"start_usec"`
	EndUsec   int64 `json:"end_usec"`
}

type CheckpointAdvance struct {
	Expected []byte
	Next     []byte
}

type SequenceGap struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
}

type IngestBatch struct {
	AdapterID            string
	AdapterVersion       string
	Initiative           Initiative
	Form                 IngestForm
	CapturedAtUsec       int64
	SourceReportedAtUsec *int64
	Coverage             *Coverage
	Checkpoint           *CheckpointAdvance
	Sequence             *uint64
	Gaps                 []SequenceGap
	ResumeWithdrawalID   *WithdrawalID
	Source               Source
	Observation          Observation
	Artifacts            []Artifact
}

type IngestReceipt struct {
	Schema               string
	SourceID             SourceID
	ObservationID        ObservationID
	ArtifactIDs          []ArtifactID
	AdapterID            string
	AdapterVersion       string
	Initiative           Initiative
	Form                 IngestForm
	CapturedAtUsec       int64
	SourceReportedAtUsec *int64
	Coverage             *Coverage
	ExpectedCheckpoint   []byte
	NextCheckpoint       []byte
	Sequence             *uint64
	Gaps                 []SequenceGap
	ResumeWithdrawalID   *WithdrawalID
}

type SourceWithdrawal struct {
	Schema               string
	ID                   WithdrawalID
	SourceID             SourceID
	ExternalWithdrawalID string
	AdapterID            string
	AdapterVersion       string
	OccurredAtUsec       int64
	Reason               *string
}

type IngestState struct {
	SourceID            SourceID
	CollectionState     CollectionState
	Checkpoint          []byte
	HighSequence        *uint64
	LastObservationID   *ObservationID
	CurrentWithdrawalID *WithdrawalID
	LastCapturedAtUsec  *int64
}

func NewWithdrawalID(sourceID SourceID, externalWithdrawalID string) (WithdrawalID, error) {
	if sourceID == (SourceID{}) {
		return WithdrawalID{}, newValidationError("source_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if err := validateIdentityInput("external_withdrawal_id", externalWithdrawalID); err != nil {
		return WithdrawalID{}, err
	}
	digest := sha256.New()
	writeTuple(digest, []byte(SourceWithdrawalSchema), sourceID[:], []byte(externalWithdrawalID))
	return WithdrawalID(digest.Sum(nil)), nil
}

func (id WithdrawalID) String() string { return hex.EncodeToString(id[:]) }

func ParseWithdrawalID(value string) (WithdrawalID, error) {
	raw, err := decodeLowerHex("withdrawal_id", ValidationCodeInvalidID, value)
	if err != nil {
		return WithdrawalID{}, err
	}
	return WithdrawalID(raw), nil
}

func (id WithdrawalID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }
func (id *WithdrawalID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("withdrawal_id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseWithdrawalID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (batch IngestBatch) Receipt() (IngestReceipt, error) {
	if err := validateBoundedText("adapter_id", batch.AdapterID, maxProvenanceBytes); err != nil {
		return IngestReceipt{}, err
	}
	if err := validateBoundedText("adapter_version", batch.AdapterVersion, maxProvenanceBytes); err != nil {
		return IngestReceipt{}, err
	}
	if batch.CapturedAtUsec <= 0 {
		return IngestReceipt{}, newValidationError("captured_at_usec", ValidationCodeInvalidRange, "must be positive", nil)
	}
	if batch.SourceReportedAtUsec != nil && *batch.SourceReportedAtUsec <= 0 {
		return IngestReceipt{}, newValidationError("source_reported_at_usec", ValidationCodeInvalidRange, "must be positive", nil)
	}
	if err := batch.Source.Validate(); err != nil {
		return IngestReceipt{}, err
	}
	if err := batch.Observation.Validate(); err != nil {
		return IngestReceipt{}, err
	}
	if batch.Observation.SourceID != batch.Source.ID {
		return IngestReceipt{}, newValidationError("observation.source_id", ValidationCodeInvalidID, "must equal source ID", nil)
	}
	artifactIDs := make([]ArtifactID, len(batch.Artifacts))
	seen := make(map[ArtifactID]struct{}, len(batch.Artifacts))
	for i, artifact := range batch.Artifacts {
		if err := artifact.Validate(); err != nil {
			return IngestReceipt{}, err
		}
		if artifact.ObservationID != batch.Observation.ID {
			return IngestReceipt{}, newValidationError(fmt.Sprintf("artifacts[%d].observation_id", i), ValidationCodeInvalidID, "must equal observation ID", nil)
		}
		if _, duplicate := seen[artifact.ID]; duplicate {
			return IngestReceipt{}, newValidationError(fmt.Sprintf("artifacts[%d].id", i), ValidationCodeInvalidID, "must be unique", nil)
		}
		seen[artifact.ID] = struct{}{}
		artifactIDs[i] = artifact.ID
	}
	if err := validateDeliveryShape(batch.Initiative, batch.Form, batch.Coverage, batch.Checkpoint, batch.Sequence, batch.Gaps); err != nil {
		return IngestReceipt{}, err
	}
	receipt := IngestReceipt{
		Schema: IngestReceiptSchema, SourceID: batch.Source.ID, ObservationID: batch.Observation.ID, ArtifactIDs: artifactIDs,
		AdapterID: batch.AdapterID, AdapterVersion: batch.AdapterVersion, Initiative: batch.Initiative, Form: batch.Form,
		CapturedAtUsec: batch.CapturedAtUsec, SourceReportedAtUsec: cloneInt64(batch.SourceReportedAtUsec), Coverage: cloneCoverage(batch.Coverage),
		Sequence: cloneUint64(batch.Sequence), Gaps: append([]SequenceGap(nil), batch.Gaps...), ResumeWithdrawalID: cloneWithdrawalID(batch.ResumeWithdrawalID),
	}
	if batch.Checkpoint != nil {
		receipt.ExpectedCheckpoint = append([]byte(nil), batch.Checkpoint.Expected...)
		receipt.NextCheckpoint = append([]byte(nil), batch.Checkpoint.Next...)
	}
	return receipt, nil
}

func validateDeliveryShape(initiative Initiative, form IngestForm, coverage *Coverage, checkpoint *CheckpointAdvance, sequence *uint64, gaps []SequenceGap) error {
	if initiative != InitiativePush && initiative != InitiativePull {
		return newValidationError("initiative", ValidationCodeInvalidEnum, "must be push or pull", nil)
	}
	if form != FormItem && form != FormSnapshot && form != FormStreamWindow {
		return newValidationError("form", ValidationCodeInvalidEnum, "must be item, snapshot, or stream_window", nil)
	}
	if form == FormItem && coverage != nil {
		return newValidationError("coverage", ValidationCodeInvalidValue, "item forbids coverage", nil)
	}
	if form != FormItem {
		if coverage == nil || coverage.StartUsec <= 0 || coverage.EndUsec <= coverage.StartUsec {
			return newValidationError("coverage", ValidationCodeInvalidRange, "must be a positive non-empty half-open range", nil)
		}
	}
	if initiative == InitiativePull {
		if checkpoint == nil || len(checkpoint.Next) == 0 || len(checkpoint.Next) > maxCheckpointBytes || checkpoint.Expected != nil && len(checkpoint.Expected) == 0 || len(checkpoint.Expected) > maxCheckpointBytes {
			return newValidationError("checkpoint", ValidationCodeInvalidRange, "pull requires bounded non-empty next checkpoint", nil)
		}
		if sequence != nil || len(gaps) != 0 {
			return newValidationError("sequence", ValidationCodeInvalidValue, "pull forbids sequence evidence", nil)
		}
	} else if checkpoint != nil {
		return newValidationError("checkpoint", ValidationCodeInvalidValue, "push forbids checkpoints", nil)
	}
	if len(gaps) != 0 && sequence == nil {
		return newValidationError("gaps", ValidationCodeInvalidValue, "gaps require sequence", nil)
	}
	for i, gap := range gaps {
		if gap.Start >= gap.End {
			return newValidationError(fmt.Sprintf("gaps[%d]", i), ValidationCodeInvalidRange, "start must be less than end", nil)
		}
	}
	return nil
}

func (withdrawal SourceWithdrawal) Validate() error {
	if withdrawal.Schema != SourceWithdrawalSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.source_withdrawal.v1", nil)
	}
	want, err := NewWithdrawalID(withdrawal.SourceID, withdrawal.ExternalWithdrawalID)
	if err != nil {
		return err
	}
	if withdrawal.ID == (WithdrawalID{}) || withdrawal.ID != want {
		return newValidationError("id", ValidationCodeInvalidID, "does not match canonical identity", nil)
	}
	if err := validateBoundedText("adapter_id", withdrawal.AdapterID, maxProvenanceBytes); err != nil {
		return err
	}
	if err := validateBoundedText("adapter_version", withdrawal.AdapterVersion, maxProvenanceBytes); err != nil {
		return err
	}
	if withdrawal.OccurredAtUsec <= 0 {
		return newValidationError("occurred_at_usec", ValidationCodeInvalidRange, "must be positive", nil)
	}
	if withdrawal.Reason != nil && (!utf8.ValidString(*withdrawal.Reason) || len(*withdrawal.Reason) > maxWithdrawalReason) {
		return newValidationError("reason", ValidationCodeInvalidValue, "must be valid UTF-8 and at most 4096 bytes", nil)
	}
	return nil
}

func (receipt IngestReceipt) Validate() error {
	if receipt.Schema != IngestReceiptSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.ingest_receipt.v1", nil)
	}
	if receipt.SourceID == (SourceID{}) || receipt.ObservationID == (ObservationID{}) {
		return newValidationError("receipt", ValidationCodeInvalidID, "source and observation IDs must not be zero", nil)
	}
	if err := validateBoundedText("adapter_id", receipt.AdapterID, maxProvenanceBytes); err != nil {
		return err
	}
	if err := validateBoundedText("adapter_version", receipt.AdapterVersion, maxProvenanceBytes); err != nil {
		return err
	}
	if receipt.CapturedAtUsec <= 0 || (receipt.SourceReportedAtUsec != nil && *receipt.SourceReportedAtUsec <= 0) {
		return newValidationError("captured_at_usec", ValidationCodeInvalidRange, "time evidence must be positive", nil)
	}
	seen := make(map[ArtifactID]struct{}, len(receipt.ArtifactIDs))
	for index, id := range receipt.ArtifactIDs {
		if id == (ArtifactID{}) {
			return newValidationError(fmt.Sprintf("artifact_ids[%d]", index), ValidationCodeInvalidID, "must not be zero", nil)
		}
		if _, duplicate := seen[id]; duplicate {
			return newValidationError(fmt.Sprintf("artifact_ids[%d]", index), ValidationCodeInvalidID, "must be unique", nil)
		}
		seen[id] = struct{}{}
	}
	if receipt.ResumeWithdrawalID != nil && *receipt.ResumeWithdrawalID == (WithdrawalID{}) {
		return newValidationError("resume_withdrawal_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	var checkpoint *CheckpointAdvance
	if receipt.Initiative == InitiativePull || receipt.ExpectedCheckpoint != nil || receipt.NextCheckpoint != nil {
		checkpoint = &CheckpointAdvance{Expected: receipt.ExpectedCheckpoint, Next: receipt.NextCheckpoint}
	}
	return validateDeliveryShape(receipt.Initiative, receipt.Form, receipt.Coverage, checkpoint, receipt.Sequence, receipt.Gaps)
}

func EncodeIngestReceipt(receipt IngestReceipt) ([]byte, error) {
	if err := receipt.Validate(); err != nil {
		return nil, err
	}
	wire := receiptWireFrom(receipt)
	return encodeJSONContract(wire, "ingest receipt")
}

func DecodeIngestReceipt(data []byte) (IngestReceipt, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return IngestReceipt{}, err
	}
	var wire ingestReceiptWire
	if err := decodeJSONContract(data, &wire); err != nil {
		return IngestReceipt{}, err
	}
	receipt, err := wire.receipt()
	if err != nil {
		return IngestReceipt{}, err
	}
	encoded, err := EncodeIngestReceipt(receipt)
	if err != nil || !bytes.Equal(encoded, data) {
		return IngestReceipt{}, newValidationError("", ValidationCodeInvalidJSON, "receipt is not canonical", err)
	}
	return receipt, nil
}

type ingestReceiptWire struct {
	Schema               string        `json:"schema"`
	SourceID             SourceID      `json:"source_id"`
	ObservationID        ObservationID `json:"observation_id"`
	ArtifactIDs          []ArtifactID  `json:"artifact_ids"`
	AdapterID            string        `json:"adapter_id"`
	AdapterVersion       string        `json:"adapter_version"`
	Initiative           Initiative    `json:"initiative"`
	Form                 IngestForm    `json:"form"`
	CapturedAtUsec       int64         `json:"captured_at_usec"`
	SourceReportedAtUsec *int64        `json:"source_reported_at_usec"`
	Coverage             *Coverage     `json:"coverage"`
	ExpectedCheckpoint   *string       `json:"expected_checkpoint"`
	NextCheckpoint       *string       `json:"next_checkpoint"`
	Sequence             *uint64       `json:"sequence"`
	Gaps                 []SequenceGap `json:"gaps"`
	ResumeWithdrawalID   *WithdrawalID `json:"resume_withdrawal_id"`
}

func receiptWireFrom(receipt IngestReceipt) ingestReceiptWire {
	artifactIDs := make([]ArtifactID, len(receipt.ArtifactIDs))
	copy(artifactIDs, receipt.ArtifactIDs)
	gaps := make([]SequenceGap, len(receipt.Gaps))
	copy(gaps, receipt.Gaps)
	wire := ingestReceiptWire{Schema: receipt.Schema, SourceID: receipt.SourceID, ObservationID: receipt.ObservationID, ArtifactIDs: artifactIDs, AdapterID: receipt.AdapterID, AdapterVersion: receipt.AdapterVersion, Initiative: receipt.Initiative, Form: receipt.Form, CapturedAtUsec: receipt.CapturedAtUsec, SourceReportedAtUsec: cloneInt64(receipt.SourceReportedAtUsec), Coverage: cloneCoverage(receipt.Coverage), Sequence: cloneUint64(receipt.Sequence), Gaps: gaps, ResumeWithdrawalID: cloneWithdrawalID(receipt.ResumeWithdrawalID)}
	if receipt.ExpectedCheckpoint != nil {
		value := hex.EncodeToString(receipt.ExpectedCheckpoint)
		wire.ExpectedCheckpoint = &value
	}
	if receipt.NextCheckpoint != nil {
		value := hex.EncodeToString(receipt.NextCheckpoint)
		wire.NextCheckpoint = &value
	}
	return wire
}

func (wire ingestReceiptWire) receipt() (IngestReceipt, error) {
	if wire.Schema != IngestReceiptSchema {
		return IngestReceipt{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.ingest_receipt.v1", nil)
	}
	decode := func(field string, value *string) ([]byte, error) {
		if value == nil {
			return nil, nil
		}
		if len(*value)%2 != 0 {
			return nil, newValidationError(field, ValidationCodeInvalidValue, "must be lowercase hexadecimal", nil)
		}
		for _, c := range *value {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return nil, newValidationError(field, ValidationCodeInvalidValue, "must be lowercase hexadecimal", nil)
			}
		}
		out, err := hex.DecodeString(*value)
		if err != nil {
			return nil, newValidationError(field, ValidationCodeInvalidValue, "must be lowercase hexadecimal", err)
		}
		return out, nil
	}
	expected, err := decode("expected_checkpoint", wire.ExpectedCheckpoint)
	if err != nil {
		return IngestReceipt{}, err
	}
	next, err := decode("next_checkpoint", wire.NextCheckpoint)
	if err != nil {
		return IngestReceipt{}, err
	}
	receipt := IngestReceipt{Schema: wire.Schema, SourceID: wire.SourceID, ObservationID: wire.ObservationID, ArtifactIDs: wire.ArtifactIDs, AdapterID: wire.AdapterID, AdapterVersion: wire.AdapterVersion, Initiative: wire.Initiative, Form: wire.Form, CapturedAtUsec: wire.CapturedAtUsec, SourceReportedAtUsec: wire.SourceReportedAtUsec, Coverage: wire.Coverage, ExpectedCheckpoint: expected, NextCheckpoint: next, Sequence: wire.Sequence, Gaps: wire.Gaps, ResumeWithdrawalID: wire.ResumeWithdrawalID}
	if _, err := EncodeIngestReceipt(receipt); err != nil {
		return IngestReceipt{}, err
	}
	return receipt, nil
}

func EncodeSourceWithdrawal(withdrawal SourceWithdrawal) ([]byte, error) {
	if err := withdrawal.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(struct {
		Schema               string       `json:"schema"`
		ID                   WithdrawalID `json:"id"`
		SourceID             SourceID     `json:"source_id"`
		ExternalWithdrawalID string       `json:"external_withdrawal_id"`
		AdapterID            string       `json:"adapter_id"`
		AdapterVersion       string       `json:"adapter_version"`
		OccurredAtUsec       int64        `json:"occurred_at_usec"`
		Reason               *string      `json:"reason"`
	}{withdrawal.Schema, withdrawal.ID, withdrawal.SourceID, withdrawal.ExternalWithdrawalID, withdrawal.AdapterID, withdrawal.AdapterVersion, withdrawal.OccurredAtUsec, withdrawal.Reason}, "source withdrawal")
}

func DecodeSourceWithdrawal(data []byte) (SourceWithdrawal, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return SourceWithdrawal{}, err
	}
	var withdrawal struct {
		Schema               string       `json:"schema"`
		ID                   WithdrawalID `json:"id"`
		SourceID             SourceID     `json:"source_id"`
		ExternalWithdrawalID string       `json:"external_withdrawal_id"`
		AdapterID            string       `json:"adapter_id"`
		AdapterVersion       string       `json:"adapter_version"`
		OccurredAtUsec       int64        `json:"occurred_at_usec"`
		Reason               *string      `json:"reason"`
	}
	if err := decodeJSONContract(data, &withdrawal); err != nil {
		return SourceWithdrawal{}, err
	}
	value := SourceWithdrawal(withdrawal)
	if err := value.Validate(); err != nil {
		return SourceWithdrawal{}, err
	}
	encoded, err := EncodeSourceWithdrawal(value)
	if err != nil || !bytes.Equal(encoded, data) {
		return SourceWithdrawal{}, newValidationError("", ValidationCodeInvalidJSON, "withdrawal is not canonical", err)
	}
	return value, nil
}

func validateBoundedText(field, value string, maximum int) error {
	if value == "" || !utf8.ValidString(value) || len(value) > maximum {
		return newValidationError(field, ValidationCodeInvalidValue, fmt.Sprintf("must be non-empty valid UTF-8 and at most %d bytes", maximum), nil)
	}
	return nil
}
func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
func cloneCoverage(value *Coverage) *Coverage {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
func cloneWithdrawalID(value *WithdrawalID) *WithdrawalID {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func rejectDuplicateJSONKeys(data []byte) error {
	if !utf8.Valid(data) {
		return newValidationError("", ValidationCodeInvalidJSON, "must be valid UTF-8 JSON", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var scan func() error
	scan = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := scan(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := scan(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	if err := scan(); err != nil {
		return newValidationError("", ValidationCodeInvalidJSON, "invalid or duplicate JSON object key", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return newValidationError("", ValidationCodeTrailingData, "must contain exactly one JSON value", err)
	}
	return nil
}
