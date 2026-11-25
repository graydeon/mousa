package sqlite

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/graydeon/mousa/internal/mousa"
)

// lexicalTokenizer is the FTS5 tokenizer every lexical index uses, including
// the bounded scratch index that normalizes query phrases for rescoring.
const lexicalTokenizer = "unicode61 remove_diacritics 2"

// decodeSQLiteVarints decodes exactly count big-endian SQLite varints: each of
// the first eight bytes contributes its low 7 bits with the high bit marking a
// continuation, and a ninth byte contributes all 8 bits. Trailing bytes are an
// error; callers treat an empty input as their own default.
func decodeSQLiteVarints(blob []byte, count int) ([]uint64, error) {
	values := make([]uint64, 0, count)
	for len(values) < count {
		var value uint64
		n := 0
		for {
			if n >= len(blob) || n > 8 {
				return nil, errors.New("malformed varint")
			}
			b := blob[n]
			if n == 8 {
				value = value<<8 | uint64(b)
				n++
				break
			}
			value = value<<7 | uint64(b&0x7f)
			n++
			if b < 0x80 {
				break
			}
		}
		blob = blob[n:]
		values = append(values, value)
	}
	if len(blob) != 0 {
		return nil, errors.New("trailing bytes")
	}
	return values, nil
}

// readBM25Stats sums the row count, token count and per-phrase document
// frequencies of every eligible ancestry shard. Each shard's averages record
// holds exactly two varints; an empty block is an empty index with zero rows
// and zero tokens. A phrase's MATCH count reads the same postings FTS5's BM25
// uses for the phrase's row frequency, and shards returning no hits still
// contribute their rows to the totals.
func readBM25Stats(ctx context.Context, q queryer, scopes []lexicalScope, quoted []string) (*mousa.BM25Stats, error) {
	stats := &mousa.BM25Stats{Frequencies: make([]uint64, len(quoted))}
	for _, scope := range scopes {
		var block []byte
		if err := q.QueryRowContext(ctx, `SELECT block FROM `+scope.table()+`_data WHERE id=1`).Scan(&block); err != nil {
			return nil, lexicalIntegrityError(ctx, "read lexical scope statistics", err)
		}
		sizes := []uint64{0, 0}
		if len(block) != 0 {
			var err error
			sizes, err = decodeSQLiteVarints(block, 2)
			if err != nil {
				return nil, integrity("read lexical scope statistics", "invalid averages record")
			}
		}
		if sizes[0] > maxBM25JSONSafe-stats.Documents || sizes[1] > maxBM25JSONSafe-stats.Tokens ||
			(sizes[0] == 0 && sizes[1] != 0) {
			return nil, integrity("read lexical scope statistics", "invalid shard totals")
		}
		stats.Documents += sizes[0]
		stats.Tokens += sizes[1]
		if sizes[0] == 0 {
			continue // an empty shard has no postings to scan
		}
		counted := make(map[string]uint64, len(quoted))
		for i, phrase := range quoted {
			count, done := counted[phrase]
			if !done {
				var rows int64
				if err := q.QueryRowContext(ctx, `SELECT count(*) FROM `+scope.table()+
					` WHERE `+scope.table()+` MATCH ?`, phrase).Scan(&rows); err != nil {
					return nil, lexicalQueryError(ctx, err)
				}
				if rows < 0 || uint64(rows) > sizes[0] || (rows != 0 && sizes[1] == 0) {
					return nil, integrity("read lexical scope statistics", "invalid phrase count")
				}
				count = uint64(rows)
				counted[phrase] = count
			}
			stats.Frequencies[i] += count
		}
	}
	return stats, nil
}

const maxBM25JSONSafe = 1<<53 - 1

// sharedProposals rescores each eligible shard's local BM25 hits with the
// statistics shared across all eligible shards, then merges by that score,
// ties by segment ID, at the request limit. Statistics are always computed
// for a mixed query, even when no shard returns a hit. Memory stays bounded
// by the per-shard limit, not the shard count.
func sharedProposals(ctx context.Context, q queryer, scopes []lexicalScope, quoted []string, limit int) ([]proposal, *mousa.BM25Stats, error) {
	stats, err := readBM25Stats(ctx, q, scopes, quoted)
	if err != nil {
		return nil, nil, err
	}
	idf, err := stats.IDF()
	if err != nil {
		return nil, nil, integrity("score shared lexical proposals", err.Error())
	}
	expression := strings.Join(quoted, " OR ")
	var best []proposal
	var scratch *bm25Scratch
	var phrases [][]string
	defer func() {
		if scratch != nil {
			scratch.close()
		}
	}()
	for _, scope := range scopes {
		rows, err := scopedProposals(ctx, q, scope, expression, limit, true)
		if err != nil {
			return nil, nil, err
		}
		if len(rows) == 0 {
			continue
		}
		if scratch == nil {
			scratch, err = openBM25Scratch(ctx)
			if err != nil {
				return nil, nil, err
			}
			if phrases, err = scratch.phrases(ctx, quoted); err != nil {
				return nil, nil, err
			}
		}
		if err := scratch.rescore(ctx, stats, idf, phrases, rows); err != nil {
			return nil, nil, err
		}
		best = append(best, rows...)
		slices.SortFunc(best, func(a, b proposal) int {
			return cmp.Or(cmp.Compare(a.Score, b.Score), bytes.Compare(a.SegmentID[:], b.SegmentID[:]))
		})
		best = best[:min(len(best), limit)]
	}
	return best, stats, nil
}

// bm25Scratch is one in-memory FTS5 index that normalizes the original query
// phrases and counts their occurrences inside each candidate's text. It lives
// in a single transaction on an in-memory database: no per-insert autocommit
// or segment flush, and nothing it writes ever reaches the store.
type bm25Scratch struct {
	tx     *sql.Tx
	db     *sql.DB
	insert *sql.Stmt
}

func openBM25Scratch(ctx context.Context) (*bm25Scratch, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, wrap(CodeInternal, "open lexical scratch", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		_ = db.Close()
		return nil, classify("open lexical scratch", err)
	}
	scratch := &bm25Scratch{tx: tx, db: db}
	for _, ddl := range []string{
		`CREATE VIRTUAL TABLE lex USING fts5(text, content='', tokenize='` + lexicalTokenizer + `')`,
		`CREATE VIRTUAL TABLE positions USING fts5vocab(lex, instance)`,
	} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			scratch.close()
			return nil, classify("create lexical scratch", err)
		}
	}
	if scratch.insert, err = tx.PrepareContext(ctx, `INSERT INTO lex(rowid,text) VALUES(?,?)`); err != nil {
		scratch.close()
		return nil, classify("prepare lexical scratch", err)
	}
	return scratch, nil
}

func (s *bm25Scratch) close() {
	if s.insert != nil {
		_ = s.insert.Close()
	}
	_ = s.tx.Rollback()
	_ = s.db.Close()
}

// index replaces the scratch documents with texts at explicit rowids 1..n.
func (s *bm25Scratch) index(ctx context.Context, texts []string) error {
	if _, err := s.tx.ExecContext(ctx, `INSERT INTO lex(lex) VALUES('delete-all')`); err != nil {
		return classify("clear lexical scratch", err)
	}
	for i, text := range texts {
		if _, err := s.insert.ExecContext(ctx, i+1, text); err != nil {
			return classify("index lexical scratch", err)
		}
	}
	return nil
}

// phrases returns each original quoted phrase's normalized token sequence,
// in input order, including zero-token phrases as empty sequences.
func (s *bm25Scratch) phrases(ctx context.Context, quoted []string) ([][]string, error) {
	inner := make([]string, len(quoted))
	for i, phrase := range quoted {
		inner[i] = phrase[1 : len(phrase)-1]
	}
	if err := s.index(ctx, inner); err != nil {
		return nil, err
	}
	rows, err := s.tx.QueryContext(ctx, `SELECT term, doc, offset FROM positions ORDER BY doc, offset`)
	if err != nil {
		return nil, lexicalQueryError(ctx, err)
	}
	defer rows.Close()
	phrases := make([][]string, len(quoted))
	for rows.Next() {
		var term string
		var doc, offset int64
		if err := rows.Scan(&term, &doc, &offset); err != nil {
			return nil, lexicalIntegrityError(ctx, "read scratch phrase token", err)
		}
		if doc < 1 || doc > int64(len(phrases)) || offset != int64(len(phrases[doc-1])) {
			return nil, integrity("read scratch phrase token", "vocab row disagrees with indexed phrases")
		}
		phrases[doc-1] = append(phrases[doc-1], term)
	}
	return phrases, classify("finish scratch phrase tokens", rows.Err())
}

// rescore counts every phrase's occurrences in each candidate's text, scores
// it against the shared statistics and replaces the proposal's raw score,
// match evidence and scratch text.
func (s *bm25Scratch) rescore(ctx context.Context, stats *mousa.BM25Stats, idf []float64, phrases [][]string, rows []proposal) error {
	tokens := []string{}
	seen := map[string]bool{}
	for _, phrase := range phrases {
		for _, token := range phrase {
			if !seen[token] {
				seen[token] = true
				tokens = append(tokens, token)
			}
		}
	}
	texts := make([]string, len(rows))
	for i := range rows {
		texts[i] = rows[i].text
	}
	if err := s.index(ctx, texts); err != nil {
		return err
	}
	occurrences := make([]map[string]map[int]bool, len(rows))
	for i := range occurrences {
		occurrences[i] = map[string]map[int]bool{}
	}
	for _, token := range tokens {
		matches, err := s.tx.QueryContext(ctx, `SELECT doc, offset FROM positions WHERE term = ?`, token)
		if err != nil {
			return lexicalQueryError(ctx, err)
		}
		for matches.Next() {
			var doc, offset int64
			if err := matches.Scan(&doc, &offset); err != nil {
				matches.Close()
				return lexicalIntegrityError(ctx, "read scratch phrase position", err)
			}
			if doc < 1 || doc > int64(len(rows)) || offset < 0 || uint64(offset) >= rows[doc-1].length {
				matches.Close()
				return integrity("read scratch phrase position", "vocab row disagrees with indexed candidates")
			}
			set := occurrences[doc-1][token]
			if set == nil {
				set = map[int]bool{}
				occurrences[doc-1][token] = set
			}
			set[int(offset)] = true
		}
		matches.Close()
		if err := matches.Err(); err != nil {
			return lexicalQueryError(ctx, err)
		}
	}
	for i := range rows {
		match := &mousa.BM25Match{Frequencies: make([]uint64, len(phrases)), Length: rows[i].length}
		for j, phrase := range phrases {
			match.Frequencies[j] = phraseFrequency(occurrences[i], phrase)
		}
		score, err := stats.Score(idf, *match)
		if err != nil {
			return integrity("score shared lexical proposal", err.Error())
		}
		rows[i].Score = score
		rows[i].Match = match
		rows[i].text = ""
	}
	return nil
}

// phraseFrequency counts all occurrences of the token sequence in the
// document's term positions, including overlapping matches.
func phraseFrequency(positions map[string]map[int]bool, phrase []string) uint64 {
	if len(phrase) == 0 {
		return 0
	}
	if len(phrase) == 1 {
		return uint64(len(positions[phrase[0]]))
	}
	var count uint64
	for start := range positions[phrase[0]] {
		found := true
		for i := 1; i < len(phrase); i++ {
			if !positions[phrase[i]][start+i] {
				found = false
				break
			}
		}
		if found {
			count++
		}
	}
	return count
}

// sharedBM25Changed reports whether the statistics recorded on a shared-fusion
// trail still match the currently eligible shards. A historically allowed
// source that is denied now reports changed without reading its data again.
func sharedBM25Changed(ctx context.Context, q queryer, trail mousa.Trail) (bool, error) {
	var allowed []mousa.SourceID
	for _, source := range trail.Sources {
		if source.Outcome != "allow" {
			continue
		}
		decision, err := currentSourceDecision(ctx, q, source.SourceID)
		if err != nil {
			return false, err
		}
		if decision.Outcome != "allow" {
			return true, nil
		}
		allowed = append(allowed, source.SourceID)
	}
	scopes, err := lexicalScopes(ctx, q, allowed)
	if err != nil {
		return false, err
	}
	for _, branch := range trail.Retrieval.Branches {
		quoted := make([]string, 0, len(branch.Terms))
		for _, term := range branch.Terms {
			quoted = append(quoted, mousa.QuoteLexicalTerm(term))
		}
		stats, err := readBM25Stats(ctx, q, scopes, quoted)
		if err != nil {
			return false, err
		}
		if stats.Documents != branch.SharedBM25.Documents || stats.Tokens != branch.SharedBM25.Tokens ||
			!slices.Equal(stats.Frequencies, branch.SharedBM25.Frequencies) {
			return true, nil
		}
	}
	return false, nil
}
