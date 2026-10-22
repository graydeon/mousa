package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/graydeon/mousa/internal/mousa"
)

// VectorPassage contains exact normalized bytes and canonical embedding pins.
type VectorPassage struct {
	mousa.EmbeddingInput
	Item          string `json:"item"`
	ByteStart     uint64 `json:"byte_start"`
	ByteEnd       uint64 `json:"byte_end"`
	SegmentPolicy string `json:"segment_policy"`
	Text          string `json:"text"`
}

type EmbeddingInputs struct {
	Schema   string               `json:"schema"`
	Decision mousa.PolicyDecision `json:"decision"`
	Passages []VectorPassage      `json:"passages"`
}

// ExportEmbeddingInputs performs fresh authorization and reads current bytes in
// one writer snapshot. No transaction spans external embedding generation.
func (store *Store) ExportEmbeddingInputs(ctx context.Context, request mousa.PolicyEvaluationRequest) (EmbeddingInputs, error) {
	if err := store.requireWritable("export embedding inputs"); err != nil {
		return EmbeddingInputs{}, err
	}
	if err := request.Validate(); err != nil {
		return EmbeddingInputs{}, wrap(CodeInvalidRecord, "export embedding inputs", err)
	}
	result := EmbeddingInputs{Schema: "mousa.embedding_inputs.v1", Passages: []VectorPassage{}}
	err := store.writeImmediate(ctx, "export embedding inputs", func(conn *sql.Conn) error {
		decision, err := evaluateSourceRetrieval(ctx, conn, request, false)
		if err != nil {
			return err
		}
		result.Decision = decision
		if decision.Outcome != mousa.PolicyOutcomeAllow {
			return nil
		}
		result.Passages, err = currentVectorPassages(ctx, conn, request.SourceID)
		return err
	})
	if err != nil {
		return EmbeddingInputs{}, err
	}
	return result, nil
}

// currentVectorPassages bounds both item enumeration and total passage bytes.
// The caller's transaction owns policy, current pointers and indexed byte reads.
func currentVectorPassages(ctx context.Context, q queryer, source mousa.SourceID) ([]VectorPassage, error) {
	var recovery bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM local_recovery_sources WHERE source_id=?)`, source[:]).Scan(&recovery); err != nil {
		return nil, classify("vector recovery state", err)
	}
	if recovery {
		return nil, wrap(CodeInvalidQuery, "vector current inputs", errors.New("source requires complete directory sync after migration"))
	}
	rows, err := q.QueryContext(ctx, `SELECT item_id FROM local_items WHERE source_id=? AND active=1 ORDER BY item_id LIMIT 101`, source[:])
	if err != nil {
		return nil, classify("enumerate vector items", err)
	}
	var items []string
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			rows.Close()
			return nil, classify("enumerate vector items", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, classify("enumerate vector items", err)
	}
	if err := rows.Close(); err != nil {
		return nil, classify("enumerate vector items", err)
	}
	if len(items) > mousa.MaxVectorPassages {
		return nil, wrap(CodeResourceLimit, "vector source", errors.New("source exceeds 100 active items"))
	}
	passages := []VectorPassage{}
	total := 0
	for _, name := range items {
		if len(name) > 4096 {
			return nil, wrap(CodeResourceLimit, "vector source", errors.New("item identifier exceeds 4096 bytes"))
		}
		item, err := getLocalItem(ctx, q, source, name)
		if err != nil {
			return nil, err
		}
		rep, err := getRepresentation(ctx, q, item.RepresentationID)
		if err != nil {
			return nil, err
		}
		segments, more, err := lexicalSegmentsLimited(ctx, q, item.RepresentationID, mousa.MaxVectorPassages-len(passages))
		if err != nil {
			return nil, err
		}
		if more {
			return nil, wrap(CodeResourceLimit, "vector source", errors.New("source exceeds 100 current passages"))
		}
		for _, segment := range segments {
			if err := segment.ValidateAgainst(rep); err != nil {
				return nil, wrap(CodeIntegrity, "vector segment", err)
			}
			text, err := indexedSegmentText(ctx, q, segment)
			if err != nil {
				return nil, err
			}
			total += len(text)
			if total > mousa.MaxVectorTextBytes {
				return nil, wrap(CodeResourceLimit, "vector source", errors.New("source exceeds 256 KiB normalized passage bytes"))
			}
			span, ok := segment.Selector.TextByteRange()
			if !ok {
				return nil, integrity("vector segment", "not a text range")
			}
			passages = append(passages, VectorPassage{EmbeddingInput: mousa.EmbeddingInput{SourceID: source, SegmentID: segment.ID, RepresentationID: rep.ID, RepresentationSHA256: rep.ContentSHA256, ContentSHA256: segment.ContentSHA256}, Item: name, ByteStart: span.Start, ByteEnd: span.End, SegmentPolicy: item.SegmentationPolicy, Text: text})
		}
	}
	return passages, nil
}

// ImportEmbeddings is local administration, not an access grant. Every supplied
// pin must match a current passage. The whole bounded batch commits or rolls back.
func (store *Store) ImportEmbeddings(ctx context.Context, source mousa.SourceID, input mousa.VectorImport) (mousa.SHA256, error) {
	if err := store.requireWritable("import embeddings"); err != nil {
		return mousa.SHA256{}, err
	}
	if err := input.Validate(); err != nil {
		return mousa.SHA256{}, wrap(CodeInvalidRecord, "import embeddings", err)
	}
	spaceID, _ := input.Space.Identity()
	err := store.writeImmediate(ctx, "import embeddings", func(conn *sql.Conn) error {
		state, err := getIngestState(ctx, conn, source)
		if err != nil {
			return err
		}
		if state.CollectionState != mousa.CollectionActive {
			return wrap(CodeInvalidRecord, "import embeddings", errors.New("source is withdrawn"))
		}
		passages, err := currentVectorPassages(ctx, conn, source)
		if err != nil {
			return err
		}
		current := make(map[mousa.SegmentID]mousa.EmbeddingInput, len(passages))
		for _, p := range passages {
			current[p.SegmentID] = p.EmbeddingInput
		}
		for _, supplied := range input.Embeddings {
			if pin, ok := current[supplied.SegmentID]; !ok || pin != supplied.EmbeddingInput {
				return wrap(CodeInvalidRecord, "import embeddings", errors.New("embedding source/segment/content pins do not match a current passage"))
			}
		}
		spaceData, err := json.Marshal(input.Space)
		if err != nil {
			return err
		}
		if err := insertVectorSpace(ctx, conn, spaceID, spaceData); err != nil {
			return err
		}
		for _, supplied := range input.Embeddings {
			embedding, err := mousa.NewEmbedding(input.Space, supplied)
			if err != nil {
				return wrap(CodeInvalidRecord, "import embeddings", err)
			}
			encoded, err := json.Marshal(embedding)
			if err != nil {
				return err
			}
			var existingID []byte
			lookup := conn.QueryRowContext(ctx, `SELECT id FROM embeddings WHERE space_id=? AND source_id=? AND segment_id=?`, spaceID[:], source[:], supplied.SegmentID[:]).Scan(&existingID)
			if lookup != nil && lookup != sql.ErrNoRows {
				return classify("lookup immutable embedding", lookup)
			}
			if lookup == nil {
				if len(existingID) != 32 {
					return integrity("import embeddings", "invalid stored embedding identity")
				}
				var storedID mousa.SHA256
				copy(storedID[:], existingID)
				stored, err := getEmbedding(ctx, conn, storedID)
				if err != nil {
					return err
				}
				existing, _ := json.Marshal(stored)
				if !bytes.Equal(existing, encoded) {
					return wrap(CodeConflict, "import embeddings", errors.New("immutable embedding conflicts; declare a different space for changed vectors"))
				}
				continue
			}
			_, err = conn.ExecContext(ctx, `INSERT INTO embeddings(id,space_id,source_id,segment_id,record_json) VALUES(?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, embedding.ID[:], spaceID[:], source[:], supplied.SegmentID[:], encoded)
			if err != nil {
				return classify("insert embedding", err)
			}
			got, err := getEmbedding(ctx, conn, embedding.ID)
			if err != nil {
				return err
			}
			existing, _ := json.Marshal(got)
			if !bytes.Equal(existing, encoded) {
				return wrap(CodeConflict, "import embeddings", errors.New("immutable embedding conflicts; declare a different space for changed vectors"))
			}
		}
		return nil
	})
	if err != nil {
		return mousa.SHA256{}, err
	}
	return spaceID, nil
}

func insertVectorSpace(ctx context.Context, conn *sql.Conn, id mousa.SHA256, data []byte) error {
	if _, err := conn.ExecContext(ctx, `INSERT INTO vector_spaces(id,record_json) VALUES(?,?) ON CONFLICT(id) DO NOTHING`, id[:], data); err != nil {
		return classify("insert vector space", err)
	}
	var existing []byte
	if err := conn.QueryRowContext(ctx, `SELECT record_json FROM vector_spaces WHERE id=?`, id[:]).Scan(&existing); err != nil {
		return readError("read vector space", err)
	}
	if !bytes.Equal(existing, data) {
		return wrap(CodeConflict, "insert vector space", errors.New("immutable identity conflict"))
	}
	return nil
}

func getVectorSpace(ctx context.Context, q queryer, id mousa.SHA256) (mousa.VectorSpace, error) {
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT record_json FROM vector_spaces WHERE id=?`, id[:]).Scan(&data); err != nil {
		return mousa.VectorSpace{}, readError("get vector space", err)
	}
	var space mousa.VectorSpace
	if len(data) > 4096 {
		return space, integrity("get vector space", "oversized record")
	}
	if err := json.Unmarshal(data, &space); err != nil {
		return space, wrap(CodeIntegrity, "get vector space", err)
	}
	encoded, _ := json.Marshal(space)
	want, err := space.Identity()
	if err != nil || want != id || !bytes.Equal(encoded, data) {
		return space, integrity("get vector space", "canonical record or identity mismatch")
	}
	return space, nil
}

func getEmbedding(ctx context.Context, q queryer, id mousa.SHA256) (mousa.Embedding, error) {
	var data, spaceRaw, sourceRaw, segmentRaw []byte
	if err := q.QueryRowContext(ctx, `SELECT space_id,source_id,segment_id,record_json FROM embeddings WHERE id=?`, id[:]).Scan(&spaceRaw, &sourceRaw, &segmentRaw, &data); err != nil {
		return mousa.Embedding{}, readError("get embedding", err)
	}
	var embedding mousa.Embedding
	if len(data) > 128<<10 {
		return embedding, integrity("get embedding", "oversized record")
	}
	if err := json.Unmarshal(data, &embedding); err != nil {
		return embedding, wrap(CodeIntegrity, "get embedding", err)
	}
	encoded, _ := json.Marshal(embedding)
	if embedding.ID != id || !bytes.Equal(encoded, data) || !bytes.Equal(spaceRaw, embedding.SpaceID[:]) || !bytes.Equal(sourceRaw, embedding.SourceID[:]) || !bytes.Equal(segmentRaw, embedding.SegmentID[:]) {
		return embedding, integrity("get embedding", "canonical record or projection mismatch")
	}
	space, err := getVectorSpace(ctx, q, embedding.SpaceID)
	if err != nil {
		return embedding, lexicalStructuralError("embedding space", err)
	}
	want, err := mousa.NewEmbedding(space, embedding.SuppliedEmbedding)
	if err != nil || want.ID != id {
		return embedding, integrity("get embedding", "invalid vector or identity")
	}
	if err := verifyEmbeddingPins(ctx, q, embedding.EmbeddingInput); err != nil {
		return embedding, err
	}
	return embedding, nil
}

func verifyEmbeddingPins(ctx context.Context, q queryer, pin mousa.EmbeddingInput) error {
	segment, err := getSegment(ctx, q, pin.SegmentID)
	if err != nil {
		return lexicalStructuralError("embedding segment", err)
	}
	rep, err := getRepresentation(ctx, q, pin.RepresentationID)
	if err != nil {
		return lexicalStructuralError("embedding representation", err)
	}
	if segment.RepresentationID != rep.ID || segment.ContentSHA256 != pin.ContentSHA256 || rep.ContentSHA256 != pin.RepresentationSHA256 {
		return integrity("embedding pins", "canonical content mismatch")
	}
	if err := segment.ValidateAgainst(rep); err != nil {
		return wrap(CodeIntegrity, "embedding segment", err)
	}
	if _, err := localItemPathOfRepresentation(ctx, q, pin.SourceID, rep.ID); err != nil {
		return lexicalStructuralError("embedding source", err)
	}
	return nil
}

type TracedVectorResult struct {
	Decision mousa.PolicyDecision
	Trail    mousa.SourceTrail
	Passages []VectorPassage
	Context  string
	Verified []mousa.VerifiedLexicalCandidate
}

// EvaluateAndTraceVector uses fresh policy and current source pins in the same
// immediate transaction as the immutable cosine selection and packet record.
func (store *Store) EvaluateAndTraceVector(ctx context.Context, request mousa.PolicyEvaluationRequest, spaceID mousa.SHA256, values []float64, budget uint64, packing string) (TracedVectorResult, error) {
	return store.traceVector(ctx, request, spaceID, values, budget, packing, nil)
}

func (store *Store) traceVector(ctx context.Context, request mousa.PolicyEvaluationRequest, spaceID mousa.SHA256, values []float64, budget uint64, packing string, token *TokenQuery) (TracedVectorResult, error) {
	if err := store.requireWritable("query vector"); err != nil {
		return TracedVectorResult{}, err
	}
	if err := request.Validate(); err != nil {
		return TracedVectorResult{}, wrap(CodeInvalidRecord, "query vector", err)
	}
	if err := mousa.ValidateVector(values, len(values)); err != nil {
		return TracedVectorResult{}, wrap(CodeInvalidQuery, "query vector", err)
	}
	if budget == 0 || budget > mousa.MaxVectorTextBytes {
		return TracedVectorResult{}, wrap(CodeInvalidQuery, "query vector", errors.New("vector budget must be 1..262144 bytes"))
	}
	var result TracedVectorResult
	err := store.writeImmediate(ctx, "query vector", func(conn *sql.Conn) error {
		space, err := getVectorSpace(ctx, conn, spaceID)
		if err != nil {
			return err
		}
		if err := mousa.ValidateVector(values, space.Dimensions); err != nil {
			return wrap(CodeInvalidQuery, "query vector", err)
		}
		decision, err := evaluateSourceRetrieval(ctx, conn, request, false)
		if err != nil {
			return err
		}
		queryID, err := mousa.QueryVectorIdentity(spaceID, values)
		if err != nil {
			return err
		}
		selection := mousa.VectorSelection{Mode: "vector", SpaceID: spaceID, Space: space, QueryVectorID: queryID, QueryValues: values, Candidates: []mousa.VectorCandidate{}}
		type ranked struct {
			passage   VectorPassage
			candidate mousa.VectorCandidate
		}
		var rankedRows []ranked
		if decision.Outcome == mousa.PolicyOutcomeAllow {
			passages, err := currentVectorPassages(ctx, conn, request.SourceID)
			if err != nil {
				return err
			}
			selection.Eligible = len(passages)
			for _, p := range passages {
				var raw []byte
				err := conn.QueryRowContext(ctx, `SELECT id FROM embeddings WHERE space_id=? AND source_id=? AND segment_id=?`, spaceID[:], request.SourceID[:], p.SegmentID[:]).Scan(&raw)
				if err == sql.ErrNoRows {
					continue
				}
				if err != nil {
					return classify("lookup current embedding", err)
				}
				if len(raw) != 32 {
					return integrity("lookup current embedding", "invalid identity")
				}
				var id mousa.SHA256
				copy(id[:], raw)
				e, err := getEmbedding(ctx, conn, id)
				if err != nil {
					return err
				}
				if e.EmbeddingInput != p.EmbeddingInput || e.SpaceID != spaceID {
					return integrity("query vector", "embedding does not bind current source passage")
				}
				score, err := mousa.CosineSimilarity(values, e.Values)
				if err != nil {
					return wrap(CodeIntegrity, "score vector", err)
				}
				rankedRows = append(rankedRows, ranked{p, mousa.VectorCandidate{EmbeddingID: e.ID, EmbeddingInput: e.EmbeddingInput, Cosine: score}})
			}
		}
		sort.Slice(rankedRows, func(i, j int) bool {
			a, b := rankedRows[i].candidate, rankedRows[j].candidate
			if a.Cosine != b.Cosine {
				return a.Cosine > b.Cosine
			}
			return bytes.Compare(a.SegmentID[:], b.SegmentID[:]) < 0
		})
		candidates := make([]mousa.VerifiedLexicalCandidate, len(rankedRows))
		result.Passages = make([]VectorPassage, len(rankedRows))
		for i, row := range rankedRows {
			segment, err := getSegment(ctx, conn, row.passage.SegmentID)
			if err != nil {
				return err
			}
			// The shared packer consumes canonical bytes/ranks, not BM25. No lexical
			// ranking or verification routine is called on a cosine result.
			candidates[i] = mousa.VerifiedLexicalCandidate{Segment: segment, Text: row.passage.Text, FinalRank: i + 1, Disposition: mousa.CandidateAccepted}
			result.Passages[i] = row.passage
			selection.Candidates = append(selection.Candidates, row.candidate)
		}
		selection.Indexed = len(rankedRows)
		trail, err := mousa.NewVectorSourceTrail(request, decision, selection, candidates, budget, packing)
		if err != nil {
			return wrap(CodeInvalidRecord, "build vector trail", err)
		}
		if token != nil {
			result.Verified = candidates
			trail, result.Context, err = mousa.PackTokenContext(trail, request.SourceID, candidates, token.Budget, token.Counter)
			if err != nil {
				return err
			}
		}
		if err := insertSourceTrail(ctx, conn, trail); err != nil {
			return err
		}
		written, err := getSourceTrail(ctx, conn, trail.ID)
		if err != nil {
			return err
		}
		result.Decision = decision
		result.Trail = written
		return nil
	})
	if err != nil {
		return TracedVectorResult{}, err
	}
	return result, nil
}

func verifyVectorTrail(ctx context.Context, q queryer, trail mousa.SourceTrail, source mousa.SourceID) error {
	selection := trail.Vector
	if selection == nil {
		return integrity("vector trail", "missing selection")
	}
	space, err := getVectorSpace(ctx, q, selection.SpaceID)
	if err != nil {
		return lexicalStructuralError("vector trail space", err)
	}
	if space != selection.Space {
		return integrity("vector trail", "space declaration disagrees with producing record")
	}
	for _, row := range selection.Candidates {
		e, err := getEmbedding(ctx, q, row.EmbeddingID)
		if err != nil {
			return lexicalStructuralError("vector trail embedding", err)
		}
		if e.SourceID != source || e.SpaceID != selection.SpaceID || e.EmbeddingInput != row.EmbeddingInput {
			return integrity("vector trail", "candidate does not bind producing embedding/source")
		}
		score, err := mousa.CosineSimilarity(selection.QueryValues, e.Values)
		if err != nil || score != row.Cosine {
			return integrity("vector trail", "recorded cosine disagrees with producing values")
		}
	}
	return nil
}

func verifyVectorRecords(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return startupError("begin vector verification", err)
	}
	defer tx.Rollback()
	for _, table := range []string{"vector_spaces", "embeddings"} {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM `+table+` ORDER BY id`)
		if err != nil {
			return classify("scan vector records", err)
		}
		var ids []mousa.SHA256
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			if len(raw) != 32 {
				rows.Close()
				return integrity("vector records", "invalid identity")
			}
			var id mousa.SHA256
			copy(id[:], raw)
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ids {
			if table == "vector_spaces" {
				_, err = getVectorSpace(ctx, tx, id)
			} else {
				_, err = getEmbedding(ctx, tx, id)
			}
			if err != nil {
				return fmt.Errorf("verify %s: %w", table, err)
			}
		}
	}
	return classify("finish vector verification", tx.Commit())
}
