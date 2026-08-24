package mousa

import (
	"bytes"
	"errors"
	"testing"
)

func TestWithdrawalIDGoldenVector(t *testing.T) {
	sourceID, err := NewSourceID("test", "source-1")
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewWithdrawalID(sourceID, "withdrawal-1")
	if err != nil {
		t.Fatal(err)
	}
	const want = "b42b1369581fb07ab2860566ccac3d62d28124b141f9412ce61f7fcca853feeb"
	if id.String() != want {
		t.Fatalf("WithdrawalID = %s, want %s", id, want)
	}
	other, err := NewWithdrawalID(sourceID, "withdrawal-2")
	if err != nil || other == id {
		t.Fatalf("input sensitivity: id=%s err=%v", other, err)
	}
}

func TestIngestReceiptCanonicalJSONAndStrictDecode(t *testing.T) {
	sourceID, _ := NewSourceID("test", "source-1")
	observationID, _ := NewObservationID(sourceID, "observation-1")
	receipt := IngestReceipt{
		Schema:         IngestReceiptSchema,
		SourceID:       sourceID,
		ObservationID:  observationID,
		ArtifactIDs:    []ArtifactID{},
		AdapterID:      "adapter",
		AdapterVersion: "1",
		Initiative:     InitiativePull,
		Form:           FormItem,
		CapturedAtUsec: 123,
		NextCheckpoint: []byte("cursor-1"),
	}
	data, err := EncodeIngestReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schema":"mousa.ingest_receipt.v1","source_id":"13fb113770a49e2fa39f659bfd613d6df04a3e75ed4ffb9fefd3cd7709398d75","observation_id":"791db6e6f2041050c63cad52fa22c2d390c707b4dd6620743da14bae90ae9aed","artifact_ids":[],"adapter_id":"adapter","adapter_version":"1","initiative":"pull","form":"item","captured_at_usec":123,"source_reported_at_usec":null,"coverage":null,"expected_checkpoint":null,"next_checkpoint":"637572736f722d31","sequence":null,"gaps":[],"resume_withdrawal_id":null}
`
	if string(data) != want {
		t.Fatalf("canonical receipt:\n%s\nwant:\n%s", data, want)
	}
	decoded, err := DecodeIngestReceipt(data)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := EncodeIngestReceipt(decoded)
	if err != nil || !bytes.Equal(roundTrip, data) {
		t.Fatalf("round trip: %q err=%v", roundTrip, err)
	}
	invalid := [][]byte{
		bytes.Replace(data, []byte(`"adapter_id":"adapter"`), []byte(`"adapter_id":"adapter","adapter_id":"other"`), 1),
		append(append([]byte(nil), data...), []byte(`{}`)...),
		bytes.Replace(data, []byte(`"next_checkpoint":"637572736f722d31"`), []byte(`"next_checkpoint":"AA"`), 1),
		bytes.Replace(data, []byte(`"expected_checkpoint":null`), []byte(`"expected_checkpoint":""`), 1),
		bytes.Replace(data, []byte(`"initiative":"pull"`), []byte(`"initiative":"other"`), 1),
		bytes.Replace(data, []byte(`"captured_at_usec":123`), []byte(`"captured_at_usec":0`), 1),
		bytes.Replace(data, []byte(`"adapter_id":"adapter"`), []byte(`"adapter_id":""`), 1),
		append([]byte{'\xff'}, data...),
	}
	for index, input := range invalid {
		if _, err := DecodeIngestReceipt(input); err == nil {
			t.Fatalf("invalid case %d succeeded", index)
		}
	}
}

func TestIngestBatchValidatesInitiativeFormCoverageAndRelationships(t *testing.T) {
	sourceID, _ := NewSourceID("test", "source-1")
	observationID, _ := NewObservationID(sourceID, "observation-1")
	source := Source{Schema: SourceSchema, ID: sourceID, Namespace: "test", ExternalSourceID: "source-1"}
	observation := Observation{Schema: ObservationSchema, ID: observationID, SourceID: sourceID, ExternalObservationID: "observation-1"}
	valid := IngestBatch{AdapterID: "adapter", AdapterVersion: "1", Initiative: InitiativePull, Form: FormItem, CapturedAtUsec: 1, Checkpoint: &CheckpointAdvance{Next: []byte("next")}, Source: source, Observation: observation}
	if _, err := valid.Receipt(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	oversizedCheckpoint := valid
	oversizedCheckpoint.Checkpoint = &CheckpointAdvance{Next: make([]byte, 4097)}
	cases := []IngestBatch{
		func() IngestBatch { value := valid; value.Initiative = "other"; return value }(),
		func() IngestBatch { value := valid; value.Form = FormSnapshot; return value }(),
		func() IngestBatch { value := valid; value.CapturedAtUsec = 0; return value }(),
		func() IngestBatch { value := valid; value.Sequence = new(uint64); return value }(),
		oversizedCheckpoint,
		func() IngestBatch { value := valid; value.Observation.SourceID = SourceID{}; return value }(),
	}
	for index, batch := range cases {
		if _, err := batch.Receipt(); err == nil {
			t.Fatalf("invalid batch %d succeeded", index)
		}
	}
	var validationErr *ValidationError
	if _, err := DecodeIngestReceipt([]byte(`{"unknown":true}`)); !errors.As(err, &validationErr) {
		t.Fatalf("strict error = %T %v", err, err)
	}
}

func TestSourceWithdrawalCanonicalJSONAndStrictDecode(t *testing.T) {
	sourceID, _ := NewSourceID("test", "source-1")
	id, _ := NewWithdrawalID(sourceID, "withdrawal-1")
	withdrawal := SourceWithdrawal{Schema: SourceWithdrawalSchema, ID: id, SourceID: sourceID, ExternalWithdrawalID: "withdrawal-1", AdapterID: "adapter", AdapterVersion: "1", OccurredAtUsec: 123}
	data, err := EncodeSourceWithdrawal(withdrawal)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schema":"mousa.source_withdrawal.v1","id":"b42b1369581fb07ab2860566ccac3d62d28124b141f9412ce61f7fcca853feeb","source_id":"13fb113770a49e2fa39f659bfd613d6df04a3e75ed4ffb9fefd3cd7709398d75","external_withdrawal_id":"withdrawal-1","adapter_id":"adapter","adapter_version":"1","occurred_at_usec":123,"reason":null}
`
	if string(data) != want {
		t.Fatalf("withdrawal = %s, want %s", data, want)
	}
	decoded, err := DecodeSourceWithdrawal(data)
	if err != nil || decoded.ID != id {
		t.Fatalf("decode = %#v, %v", decoded, err)
	}
	invalid := [][]byte{
		bytes.Replace(data, []byte(`"adapter_id":"adapter"`), []byte(`"adapter_id":"adapter","adapter_id":"other"`), 1),
		append(append([]byte(nil), data...), []byte(`{}`)...),
		bytes.Replace(data, []byte(`"occurred_at_usec":123`), []byte(`"occurred_at_usec":0`), 1),
		bytes.Replace(data, []byte(`"adapter_id":"adapter"`), []byte(`"adapter_id":""`), 1),
		append([]byte(" "), data...),
	}
	for index, input := range invalid {
		if _, err := DecodeSourceWithdrawal(input); err == nil {
			t.Fatalf("invalid withdrawal %d succeeded", index)
		}
	}
}

func TestSnapshotAndStreamCoverageAreRequiredAndAccepted(t *testing.T) {
	sourceID, _ := NewSourceID("test", "source-1")
	observationID, _ := NewObservationID(sourceID, "observation-1")
	source := Source{Schema: SourceSchema, ID: sourceID, Namespace: "test", ExternalSourceID: "source-1"}
	observation := Observation{Schema: ObservationSchema, ID: observationID, SourceID: sourceID, ExternalObservationID: "observation-1"}
	for _, form := range []IngestForm{FormSnapshot, FormStreamWindow} {
		batch := IngestBatch{AdapterID: "adapter", AdapterVersion: "1", Initiative: InitiativePush, Form: form, CapturedAtUsec: 1, Coverage: &Coverage{StartUsec: 1, EndUsec: 2}, Source: source, Observation: observation}
		if _, err := batch.Receipt(); err != nil {
			t.Fatalf("%s valid: %v", form, err)
		}
		batch.Coverage = nil
		if _, err := batch.Receipt(); err == nil {
			t.Fatalf("%s without coverage succeeded", form)
		}
	}
}

func TestIngestReceiptAndWithdrawalStrictJSONEvidence(t *testing.T) {
	receiptData, err := EncodeIngestReceipt(testReceiptContract(t))
	if err != nil {
		t.Fatal(err)
	}
	withdrawalData, err := EncodeSourceWithdrawal(testWithdrawalContract(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		input []byte
		code  ValidationCode
		read  func([]byte) error
	}{
		{"receipt unknown field", bytes.Replace(receiptData, []byte("}\n"), []byte(",\"unknown\":true}\n"), 1), ValidationCodeUnknownField, func(data []byte) error { _, err := DecodeIngestReceipt(data); return err }},
		{"withdrawal unknown field", bytes.Replace(withdrawalData, []byte("}\n"), []byte(",\"unknown\":true}\n"), 1), ValidationCodeUnknownField, func(data []byte) error { _, err := DecodeSourceWithdrawal(data); return err }},
		{"receipt duplicate key", bytes.Replace(receiptData, []byte(`"adapter_id":"adapter"`), []byte(`"adapter_id":"adapter","adapter_id":"other"`), 1), ValidationCodeInvalidJSON, func(data []byte) error { _, err := DecodeIngestReceipt(data); return err }},
		{"withdrawal duplicate key", bytes.Replace(withdrawalData, []byte(`"adapter_id":"adapter"`), []byte(`"adapter_id":"adapter","adapter_id":"other"`), 1), ValidationCodeInvalidJSON, func(data []byte) error { _, err := DecodeSourceWithdrawal(data); return err }},
		{"receipt second value", append(append([]byte(nil), receiptData...), []byte(`{}`)...), ValidationCodeTrailingData, func(data []byte) error { _, err := DecodeIngestReceipt(data); return err }},
		{"withdrawal malformed trailing data", append(append([]byte(nil), withdrawalData...), byte('x')), ValidationCodeTrailingData, func(data []byte) error { _, err := DecodeSourceWithdrawal(data); return err }},
		{"receipt invalid UTF-8", append([]byte{0xff}, receiptData...), ValidationCodeInvalidJSON, func(data []byte) error { _, err := DecodeIngestReceipt(data); return err }},
		{"withdrawal invalid UTF-8", append([]byte{0xff}, withdrawalData...), ValidationCodeInvalidJSON, func(data []byte) error { _, err := DecodeSourceWithdrawal(data); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			field := ""
			if test.code == ValidationCodeUnknownField {
				field = "unknown"
			}
			requireValidationError(t, test.read(test.input), field, test.code)
		})
	}
}

func TestIngestReceiptAndWithdrawalRejectMalformedAndZeroIDs(t *testing.T) {
	receipt := testReceiptContract(t)
	receiptData, err := EncodeIngestReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	withdrawal := testWithdrawalContract(t)
	withdrawalData, err := EncodeSourceWithdrawal(withdrawal)
	if err != nil {
		t.Fatal(err)
	}
	zero := string(bytes.Repeat([]byte{'0'}, 64))
	for _, test := range []struct {
		name  string
		input []byte
		field string
		read  func([]byte) error
	}{
		{"receipt malformed source", bytes.Replace(receiptData, []byte(`"source_id":"`+receipt.SourceID.String()+`"`), []byte(`"source_id":"00"`), 1), "id", func(data []byte) error { _, err := DecodeIngestReceipt(data); return err }},
		{"receipt zero source", bytes.Replace(receiptData, []byte(`"source_id":"`+receipt.SourceID.String()+`"`), []byte(`"source_id":"`+zero+`"`), 1), "receipt", func(data []byte) error { _, err := DecodeIngestReceipt(data); return err }},
		{"withdrawal malformed ID", bytes.Replace(withdrawalData, []byte(`"id":"`+withdrawal.ID.String()+`"`), []byte(`"id":"00"`), 1), "withdrawal_id", func(data []byte) error { _, err := DecodeSourceWithdrawal(data); return err }},
		{"withdrawal zero ID", bytes.Replace(withdrawalData, []byte(`"id":"`+withdrawal.ID.String()+`"`), []byte(`"id":"`+zero+`"`), 1), "id", func(data []byte) error { _, err := DecodeSourceWithdrawal(data); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireValidationError(t, test.read(test.input), test.field, ValidationCodeInvalidID)
		})
	}
}

func TestIngestReceiptValidationFieldsAndBounds(t *testing.T) {
	valid := testReceiptContract(t)
	positive := int64(2)
	zeroWithdrawal := WithdrawalID{}
	for _, test := range []struct {
		name   string
		mutate func(*IngestReceipt)
		field  string
		code   ValidationCode
	}{
		{"adapter ID empty", func(value *IngestReceipt) { value.AdapterID = "" }, "adapter_id", ValidationCodeInvalidValue},
		{"adapter ID oversized", func(value *IngestReceipt) { value.AdapterID = string(bytes.Repeat([]byte{'a'}, maxProvenanceBytes+1)) }, "adapter_id", ValidationCodeInvalidValue},
		{"adapter version oversized", func(value *IngestReceipt) {
			value.AdapterVersion = string(bytes.Repeat([]byte{'a'}, maxProvenanceBytes+1))
		}, "adapter_version", ValidationCodeInvalidValue},
		{"initiative", func(value *IngestReceipt) { value.Initiative = "other" }, "initiative", ValidationCodeInvalidEnum},
		{"form", func(value *IngestReceipt) { value.Form = "other" }, "form", ValidationCodeInvalidEnum},
		{"capture time", func(value *IngestReceipt) { value.CapturedAtUsec = 0 }, "captured_at_usec", ValidationCodeInvalidRange},
		{"source time", func(value *IngestReceipt) { zero := int64(0); value.SourceReportedAtUsec = &zero }, "captured_at_usec", ValidationCodeInvalidRange},
		{"item coverage", func(value *IngestReceipt) { value.Coverage = &Coverage{StartUsec: 1, EndUsec: 2} }, "coverage", ValidationCodeInvalidValue},
		{"snapshot missing coverage", func(value *IngestReceipt) { value.Form = FormSnapshot }, "coverage", ValidationCodeInvalidRange},
		{"coverage ordering", func(value *IngestReceipt) {
			value.Form = FormSnapshot
			value.Coverage = &Coverage{StartUsec: 2, EndUsec: 2}
		}, "coverage", ValidationCodeInvalidRange},
		{"pull missing next checkpoint", func(value *IngestReceipt) { value.NextCheckpoint = nil }, "checkpoint", ValidationCodeInvalidRange},
		{"pull sequence", func(value *IngestReceipt) { value.Sequence = new(uint64) }, "sequence", ValidationCodeInvalidValue},
		{"push checkpoint", func(value *IngestReceipt) { value.Initiative = InitiativePush }, "checkpoint", ValidationCodeInvalidValue},
		{"push expected checkpoint", func(value *IngestReceipt) {
			value.Initiative = InitiativePush
			value.ExpectedCheckpoint = []byte("expected")
			value.NextCheckpoint = nil
		}, "checkpoint", ValidationCodeInvalidValue},
		{"push both checkpoints", func(value *IngestReceipt) {
			value.Initiative = InitiativePush
			value.ExpectedCheckpoint = []byte("expected")
		}, "checkpoint", ValidationCodeInvalidValue},
		{"push nil checkpoints", func(value *IngestReceipt) {
			value.Initiative = InitiativePush
			value.ExpectedCheckpoint = nil
			value.NextCheckpoint = nil
		}, "", ""},
		{"gap ordering", func(value *IngestReceipt) {
			value.Initiative = InitiativePush
			value.ExpectedCheckpoint = nil
			value.NextCheckpoint = nil
			value.Sequence = new(uint64)
			value.Gaps = []SequenceGap{{Start: 2, End: 2}}
		}, "gaps[0]", ValidationCodeInvalidRange},
		{"zero resume", func(value *IngestReceipt) { value.ResumeWithdrawalID = &zeroWithdrawal }, "resume_withdrawal_id", ValidationCodeInvalidID},
		{"positive source time remains valid", func(value *IngestReceipt) { value.SourceReportedAtUsec = &positive }, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			err := value.Validate()
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			requireValidationError(t, err, test.field, test.code)
			if test.field == "checkpoint" && test.code == ValidationCodeInvalidValue {
				var validationErr *ValidationError
				if !errors.As(err, &validationErr) {
					t.Fatalf("error = %T, want *ValidationError", err)
				}
				if validationErr.Message != "push forbids checkpoints" {
					t.Fatalf("message = %q, want %q", validationErr.Message, "push forbids checkpoints")
				}
			}
		})
	}
}

func TestWithdrawalValidationBounds(t *testing.T) {
	valid := testWithdrawalContract(t)
	for _, test := range []struct {
		name   string
		mutate func(*SourceWithdrawal)
		field  string
		code   ValidationCode
	}{
		{"adapter ID oversized", func(value *SourceWithdrawal) {
			value.AdapterID = string(bytes.Repeat([]byte{'a'}, maxProvenanceBytes+1))
		}, "adapter_id", ValidationCodeInvalidValue},
		{"adapter version invalid UTF-8", func(value *SourceWithdrawal) { value.AdapterVersion = string([]byte{0xff}) }, "adapter_version", ValidationCodeInvalidValue},
		{"occurred time", func(value *SourceWithdrawal) { value.OccurredAtUsec = 0 }, "occurred_at_usec", ValidationCodeInvalidRange},
		{"reason oversized", func(value *SourceWithdrawal) {
			reason := string(bytes.Repeat([]byte{'a'}, maxWithdrawalReason+1))
			value.Reason = &reason
		}, "reason", ValidationCodeInvalidValue},
		{"reason invalid UTF-8", func(value *SourceWithdrawal) { reason := string([]byte{0xff}); value.Reason = &reason }, "reason", ValidationCodeInvalidValue},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			requireValidationError(t, value.Validate(), test.field, test.code)
		})
	}
}

func TestIngestReceiptCanonicalRoundTripPreservesArtifactOrder(t *testing.T) {
	receipt := testReceiptContract(t)
	first, _ := NewArtifactID(receipt.ObservationID, "first")
	second, _ := NewArtifactID(receipt.ObservationID, "second")
	receipt.ArtifactIDs = []ArtifactID{second, first}
	data, err := EncodeIngestReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeIngestReceipt(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.ArtifactIDs) != 2 || decoded.ArtifactIDs[0] != second || decoded.ArtifactIDs[1] != first {
		t.Fatalf("artifact order = %v", decoded.ArtifactIDs)
	}
	again, err := EncodeIngestReceipt(decoded)
	if err != nil || !bytes.Equal(again, data) {
		t.Fatalf("round trip = %q err=%v", again, err)
	}
}

func testReceiptContract(t *testing.T) IngestReceipt {
	t.Helper()
	sourceID, err := NewSourceID("test", "strict-source")
	if err != nil {
		t.Fatal(err)
	}
	observationID, err := NewObservationID(sourceID, "strict-observation")
	if err != nil {
		t.Fatal(err)
	}
	return IngestReceipt{Schema: IngestReceiptSchema, SourceID: sourceID, ObservationID: observationID, ArtifactIDs: []ArtifactID{}, AdapterID: "adapter", AdapterVersion: "1", Initiative: InitiativePull, Form: FormItem, CapturedAtUsec: 1, NextCheckpoint: []byte("next"), Gaps: []SequenceGap{}}
}

func testWithdrawalContract(t *testing.T) SourceWithdrawal {
	t.Helper()
	sourceID, err := NewSourceID("test", "strict-source")
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewWithdrawalID(sourceID, "strict-withdrawal")
	if err != nil {
		t.Fatal(err)
	}
	return SourceWithdrawal{Schema: SourceWithdrawalSchema, ID: id, SourceID: sourceID, ExternalWithdrawalID: "strict-withdrawal", AdapterID: "adapter", AdapterVersion: "1", OccurredAtUsec: 1}
}
