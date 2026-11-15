package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

type SourceManifestInspection struct {
	Decision mousa.PolicyDecision  `json:"decision"`
	Manifest *mousa.SourceManifest `json:"manifest,omitempty"`
	Matches  *bool                 `json:"matches,omitempty"`
}

// InspectSourceManifest authorizes and verifies current indexed content in one
// writer snapshot. Its result is not a lease on any subsequent operation.
func (store *Store) InspectSourceManifest(ctx context.Context, request mousa.PolicyEvaluationRequest, expected *mousa.SHA256) (SourceManifestInspection, error) {
	if err := store.requireWritable("inspect source manifest"); err != nil {
		return SourceManifestInspection{}, err
	}
	if err := request.Validate(); err != nil {
		return SourceManifestInspection{}, wrap(CodeInvalidRecord, "inspect source manifest", err)
	}
	if expected != nil && *expected == (mousa.SHA256{}) {
		return SourceManifestInspection{}, wrap(CodeInvalidRecord, "inspect source manifest", errors.New("expected manifest ID must be nonzero"))
	}
	var result SourceManifestInspection
	err := store.writeImmediate(ctx, "inspect source manifest", func(conn *sql.Conn) error {
		decision, err := evaluateManifestRetrieval(ctx, conn, request)
		if err != nil {
			return err
		}
		result.Decision = decision
		if decision.Outcome != mousa.PolicyOutcomeAllow {
			return nil
		}
		manifest, err := currentSourceManifest(ctx, conn, request.SourceID)
		if err != nil {
			return err
		}
		result.Manifest = &manifest
		if expected != nil {
			matches := manifest.ID == *expected
			result.Matches = &matches
		}
		return ctx.Err()
	})
	if err != nil {
		return SourceManifestInspection{}, err
	}
	return result, nil
}

func currentSourceManifest(ctx context.Context, q queryer, source mousa.SourceID) (mousa.SourceManifest, error) {
	q = boundedRecordQueryer{q}
	if _, err := getSource(ctx, q, source); err != nil {
		return mousa.SourceManifest{}, err
	}
	var recovery bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM local_recovery_sources WHERE source_id=?)`, source[:]).Scan(&recovery); err != nil {
		return mousa.SourceManifest{}, classify("manifest recovery state", err)
	}
	if recovery {
		return mousa.SourceManifest{}, wrap(CodeInvalidQuery, "source manifest", errors.New("source requires complete directory sync after migration"))
	}
	rows, err := q.QueryContext(ctx, `SELECT CASE WHEN length(CAST(item_id AS BLOB))<=4096 THEN item_id ELSE NULL END FROM local_items WHERE source_id=? AND active!=0 ORDER BY item_id LIMIT ?`, source[:], mousa.MaxSourceManifestItems+1)
	if err != nil {
		return mousa.SourceManifest{}, classify("manifest items", err)
	}
	var names []string
	for rows.Next() {
		var name sql.NullString
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return mousa.SourceManifest{}, classify("manifest items", err)
		}
		if !name.Valid || len(names) == mousa.MaxSourceManifestItems {
			rows.Close()
			return mousa.SourceManifest{}, manifestLimit()
		}
		names = append(names, name.String)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return mousa.SourceManifest{}, classify("manifest items", err)
	}
	if err := rows.Close(); err != nil {
		return mousa.SourceManifest{}, classify("manifest items", err)
	}
	items := make([]mousa.SourceManifestItem, 0, len(names))
	segmentCount := 0
	var textBytes uint64
	for _, name := range names {
		item, err := getLocalItem(ctx, q, source, name)
		if err != nil {
			return mousa.SourceManifest{}, err
		}
		rep, err := getRepresentation(ctx, q, item.RepresentationID)
		if err != nil {
			return mousa.SourceManifest{}, err
		}
		if !item.Active || rep.ProcessorID != mousa.UTF8TextProcessorID || rep.ProcessorVersion != mousa.UTF8TextProcessorVersion {
			return mousa.SourceManifest{}, integrity("source manifest", "unsupported current local representation")
		}
		if len(rep.Inputs) != 1 || rep.Inputs[0] != mousa.NewArtifactDerivationInput(item.Artifact.ID) || rep.ByteLength > item.Artifact.ByteLength || item.Artifact.ByteLength > mousa.MaxUTF8TextBytes {
			return mousa.SourceManifest{}, integrity("source manifest", "representation is not a supported direct local normalization")
		}
		if rep.ByteLength > mousa.MaxSourceManifestTextBytes-textBytes {
			return mousa.SourceManifest{}, manifestLimit()
		}
		textBytes += rep.ByteLength
		segments, more, err := lexicalSegmentsLimited(ctx, q, rep.ID, mousa.MaxSourceManifestSegments-segmentCount)
		if err != nil {
			return mousa.SourceManifest{}, err
		}
		if more {
			return mousa.SourceManifest{}, manifestLimit()
		}
		segmentCount += len(segments)
		content := make([]byte, 0, int(rep.ByteLength))
		for _, segment := range segments {
			if err := segment.ValidateAgainst(rep); err != nil {
				return mousa.SourceManifest{}, wrap(CodeIntegrity, "manifest segment", err)
			}
			span, ok := segment.Selector.TextByteRange()
			if !ok || span.Start != uint64(len(content)) || span.End-span.Start > mousa.MaxUTF8TextSegmentBytes {
				return mousa.SourceManifest{}, integrity("manifest segment", "noncontiguous or oversized local selector")
			}
			// Bound hostile index payloads before the verified reader allocates them.
			var length, digestLength int64
			err := q.QueryRowContext(ctx, `SELECT length(CAST(f.text AS BLOB)), length(f.content_sha256) FROM segment_lexical_rows r JOIN segment_lexical_fts f ON f.rowid=r.rowid WHERE r.segment_id=?`, segment.ID[:]).Scan(&length, &digestLength)
			if err != nil {
				return mousa.SourceManifest{}, wrap(CodeIntegrity, "manifest indexed segment", err)
			}
			if length < 0 || uint64(length) != span.End-span.Start || digestLength != 32 {
				return mousa.SourceManifest{}, integrity("manifest indexed segment", "index payload length disagrees with selector")
			}
			text, err := indexedSegmentText(ctx, q, segment)
			if err != nil {
				return mousa.SourceManifest{}, err
			}
			content = append(content, text...)
		}
		expected, err := mousa.SegmentUTF8Text(rep, content)
		if err != nil {
			return mousa.SourceManifest{}, wrap(CodeIntegrity, "manifest reconstruction", err)
		}
		if len(expected) != len(segments) {
			return mousa.SourceManifest{}, integrity("manifest reconstruction", "incomplete current segment inventory")
		}
		for i := range expected {
			if expected[i].ID != segments[i].ID {
				return mousa.SourceManifest{}, integrity("manifest reconstruction", "segment inventory differs from actual segmentation")
			}
		}
		digest, err := mousa.SourceManifestSegmentsSHA256(segments)
		if err != nil {
			return mousa.SourceManifest{}, wrap(CodeIntegrity, "manifest segment inventory", err)
		}
		items = append(items, mousa.SourceManifestItem{
			Item: name, ObservationID: item.Artifact.ObservationID, ArtifactID: item.Artifact.ID, RepresentationID: rep.ID,
			RawSHA256: item.Artifact.ContentSHA256, RawByteLength: item.Artifact.ByteLength,
			NormalizedSHA256: rep.ContentSHA256, NormalizedByteLength: rep.ByteLength,
			ProcessorID: rep.ProcessorID, ProcessorVersion: rep.ProcessorVersion, ParametersSHA256: rep.ParametersSHA256,
			SegmentationPolicy: item.SegmentationPolicy, SegmentCount: len(segments), SegmentsSHA256: digest,
		})
	}
	// Every indexed row under this source must be one of the verified current
	// segments. Canonical historical segments remain unrestricted and untouched.
	var indexed int
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM (
		SELECT r.rowid FROM segment_lexical_rows r
		JOIN segments s ON s.id=r.segment_id
		JOIN representation_inputs ri ON ri.representation_id=s.representation_id
		JOIN artifacts a ON a.id=ri.artifact_id
		JOIN observations o ON o.id=a.observation_id
		WHERE o.source_id=? LIMIT ?
	)`, source[:], mousa.MaxSourceManifestSegments+1).Scan(&indexed)
	if err != nil {
		return mousa.SourceManifest{}, classify("manifest index completeness", err)
	}
	if indexed > mousa.MaxSourceManifestSegments {
		return mousa.SourceManifest{}, manifestLimit()
	}
	if indexed != segmentCount {
		return mousa.SourceManifest{}, integrity("manifest index completeness", "indexed rows disagree with current inventory")
	}
	manifest, err := mousa.NewSourceManifest(source, items)
	if errors.Is(err, mousa.ErrSourceManifestLimit) {
		return mousa.SourceManifest{}, manifestLimit()
	}
	if err != nil {
		return mousa.SourceManifest{}, wrap(CodeIntegrity, "source manifest", err)
	}
	return manifest, nil
}

func manifestLimit() error {
	return wrap(CodeResourceLimit, "source manifest", mousa.ErrSourceManifestLimit)
}

// The source root is content provenance, not an input to policy evaluation.
// Defer its verification until allow so denial cannot expose root corruption.
func evaluateManifestRetrieval(ctx context.Context, conn *sql.Conn, request mousa.PolicyEvaluationRequest) (mousa.PolicyDecision, error) {
	if _, err := getPolicyDecisionByRequest(ctx, conn, request.ID); err == nil {
		return mousa.PolicyDecision{}, wrap(CodeConflict, "inspect source manifest", errors.New("current evaluation requires a new request identity"))
	} else if !errors.Is(err, sql.ErrNoRows) {
		return mousa.PolicyDecision{}, err
	}
	if err := requireUnusedExternalRequest(ctx, conn, request); err != nil {
		return mousa.PolicyDecision{}, err
	}
	snapshot := mousa.PolicyEvaluationSnapshot{Inputs: []mousa.PolicyEvaluationInput{}}
	state, err := getIngestState(ctx, conn, request.SourceID)
	if err == nil {
		collection := state.CollectionState
		snapshot.StatePresent = true
		snapshot.CollectionState = &collection
		snapshot.CurrentWithdrawalID = cloneWithdrawal(state.CurrentWithdrawalID)
	} else if !IsCode(err, CodeNotFound) {
		return mousa.PolicyDecision{}, err
	}
	for _, layer := range []mousa.PolicyDecisionLayer{mousa.PolicyLayerDeployment, mousa.PolicyLayerSource} {
		inputs, err := readActivePolicyInputs(ctx, conn, layer, request.SourceID)
		if err != nil {
			return mousa.PolicyDecision{}, err
		}
		snapshot.Inputs = append(snapshot.Inputs, inputs...)
	}
	decision, err := mousa.EvaluateSourceRetrieval(request, snapshot, time.Now().UnixMicro())
	if err != nil {
		return mousa.PolicyDecision{}, wrap(CodeInvalidRecord, "inspect source manifest", err)
	}
	if decision.Outcome != mousa.PolicyOutcomeAllow {
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sources WHERE id=?)`, request.SourceID[:]).Scan(&exists); err != nil {
			return mousa.PolicyDecision{}, classify("manifest decision source", err)
		}
		if !exists {
			// Unknown sources cannot own a persisted decision's foreign key.
			// Return the fresh denial without manufacturing a source record.
			return decision, nil
		}
	}
	if err := insertPolicyDecision(ctx, conn, decision); err != nil {
		return mousa.PolicyDecision{}, err
	}
	written, err := getPolicyDecision(ctx, conn, decision.ID)
	if err != nil {
		return mousa.PolicyDecision{}, err
	}
	if written.ID != decision.ID {
		return mousa.PolicyDecision{}, integrity("manifest decision", "read-back disagrees with write")
	}
	return written, nil
}
