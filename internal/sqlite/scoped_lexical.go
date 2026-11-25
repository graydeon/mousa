package sqlite

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/graydeon/mousa/internal/mousa"
)

// A shard contains precisely one canonical ancestry set. Copying a
// multi-source segment into each source would let denied ancestry affect BM25.
type lexicalScope struct {
	id      int64
	sources []mousa.SourceID
}

func (s lexicalScope) table() string { return "lexical_scope_" + strconv.FormatInt(s.id, 10) }

func decodeLexicalScope(id int64, raw []byte) (lexicalScope, error) {
	const width = len(mousa.SourceID{})
	if id <= 0 || len(raw) == 0 || len(raw)%width != 0 || len(raw)/width > mousa.MaxScopeSources {
		return lexicalScope{}, integrity("read lexical scope", "invalid shard ID or ancestry size")
	}
	scope := lexicalScope{id: id, sources: make([]mousa.SourceID, 0, len(raw)/width)}
	for i := 0; i < len(raw); i += width {
		source := mousa.SourceID(raw[i : i+width])
		if source == (mousa.SourceID{}) || (i != 0 && bytes.Compare(raw[i-width:i], source[:]) >= 0) {
			return lexicalScope{}, integrity("read lexical scope", "invalid or unordered source IDs")
		}
		scope.sources = append(scope.sources, source)
	}
	return scope, nil
}

func getLexicalScope(ctx context.Context, q queryRower, raw []byte) (lexicalScope, error) {
	var id int64
	if err := q.QueryRowContext(ctx, `SELECT id FROM lexical_scopes WHERE source_ids = ?`, raw).Scan(&id); err != nil {
		return lexicalScope{}, readError("find lexical scope", err)
	}
	return decodeLexicalScope(id, raw)
}

func ensureLexicalScope(ctx context.Context, conn *sql.Conn, sources []mousa.SourceID) (lexicalScope, error) {
	raw := make([]byte, 0, len(sources)*len(mousa.SourceID{}))
	for _, id := range sources {
		raw = append(raw, id[:]...)
	}
	scope, err := getLexicalScope(ctx, conn, raw)
	if !IsCode(err, CodeNotFound) {
		return scope, err
	}
	if len(sources) == 0 || len(sources) > mousa.MaxScopeSources {
		return lexicalScope{}, wrap(CodeResourceLimit, "create lexical scope", errors.New("invalid ancestry size"))
	}
	result, err := conn.ExecContext(ctx, `INSERT INTO lexical_scopes(source_ids) VALUES(?)`, raw)
	if err != nil {
		return lexicalScope{}, classify("register lexical scope", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return lexicalScope{}, classify("register lexical scope", err)
	}
	scope, err = decodeLexicalScope(id, raw)
	if err != nil {
		return lexicalScope{}, err
	}
	_, err = conn.ExecContext(ctx, `CREATE VIRTUAL TABLE `+scope.table()+` USING fts5(text, tokenize='`+lexicalTokenizer+`')`)
	if err != nil {
		return lexicalScope{}, classify("create lexical scope index", err)
	}
	return scope, nil
}

// A nil filter is used only by verify. In queries the indexed first member
// excludes unrelated shards before we check the full ancestry set.
func lexicalScopes(ctx context.Context, q queryer, filter []mousa.SourceID) ([]lexicalScope, error) {
	statement := `SELECT id, source_ids FROM lexical_scopes`
	var args []any
	if filter != nil {
		if len(filter) == 0 {
			return nil, nil
		}
		statement += ` WHERE substr(source_ids, 1, 32) IN (` + strings.Repeat("?,", len(filter)-1) + `?)`
		for _, id := range filter {
			args = append(args, id[:])
		}
	}
	rows, err := q.QueryContext(ctx, statement+` ORDER BY id`, args...)
	if err != nil {
		return nil, classify("read lexical scopes", err)
	}
	defer rows.Close()
	var scopes []lexicalScope
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, classify("read lexical scope", err)
		}
		scope, err := decodeLexicalScope(id, raw)
		if err != nil {
			return nil, err
		}
		if filter == nil || !slices.ContainsFunc(scope.sources, func(id mousa.SourceID) bool { return !slices.Contains(filter, id) }) {
			scopes = append(scopes, scope)
		}
	}
	return scopes, classify("finish lexical scopes", rows.Err())
}

func proposeLexical(ctx context.Context, q queryer, quoted []string, sources []mousa.SourceID, limit int) ([]proposal, *mousa.BM25Stats, error) {
	expression := strings.Join(quoted, " OR ")
	if err := validateLexicalQuery(expression, limit); err != nil {
		return nil, nil, err
	}
	scopes, err := lexicalScopes(ctx, q, sources)
	if err != nil {
		return nil, nil, err
	}
	if len(scopes) > 1 {
		return sharedProposals(ctx, q, scopes, quoted, limit)
	}
	var proposals []proposal
	for _, scope := range scopes {
		rows, err := scopedProposals(ctx, q, scope, expression, limit, false)
		if err != nil {
			return nil, nil, err
		}
		proposals = append(proposals, rows...)
		slices.SortFunc(proposals, func(a, b proposal) int {
			return cmp.Or(cmp.Compare(a.Score, b.Score), bytes.Compare(a.SegmentID[:], b.SegmentID[:]))
		})
		proposals = proposals[:min(len(proposals), limit)]
	}
	return proposals, nil, nil
}

func scopedProposals(ctx context.Context, q queryer, scope lexicalScope, expression string, limit int, mixed bool) ([]proposal, error) {
	table := scope.table()
	// Final bounded join does not load shared text or segment identities for
	// every FTS match. A single shard keeps the historical rowid cutoff; in a
	// mixed query the cutoff ties by segment ID and the rescore needs the text
	// and token length of each local hit.
	inner := `SELECT rowid, rank FROM ` + table + ` WHERE ` + table + ` MATCH ? AND rank MATCH 'bm25()'
		ORDER BY rank, rowid LIMIT ?`
	extra, docsize := "", ""
	if mixed {
		inner = `SELECT ` + table + `.rowid, rank FROM ` + table + `
			LEFT JOIN segment_lexical_rows selected ON selected.rowid = ` + table + `.rowid
			WHERE ` + table + ` MATCH ? AND rank MATCH 'bm25()' ORDER BY rank, selected.segment_id LIMIT ?`
		extra = `, shard.text, sz.sz`
		docsize = ` LEFT JOIN ` + table + `_docsize sz ON sz.id = hits.rowid`
	}
	rows, err := q.QueryContext(ctx, `
		WITH hits AS MATERIALIZED (
			`+inner+`
		)
		SELECT r.segment_id, f.content_sha256, hits.rank, r.scope_id, shard.text = f.text`+extra+`
		FROM hits
		LEFT JOIN segment_lexical_rows r ON r.rowid = hits.rowid
		LEFT JOIN segment_lexical_fts f ON f.rowid = hits.rowid
		JOIN `+table+` shard ON shard.rowid = hits.rowid`+docsize, expression, limit)
	if err != nil {
		return nil, lexicalQueryError(ctx, err)
	}
	defer rows.Close()
	proposals := make([]proposal, 0, limit)
	for rows.Next() {
		var candidate proposal
		var id, digest []byte
		var scopeID sql.NullInt64
		var same sql.NullBool
		var size []byte
		var scan []any
		if mixed {
			scan = []any{&id, &digest, &candidate.Score, &scopeID, &same, &candidate.text, &size}
		} else {
			scan = []any{&id, &digest, &candidate.Score, &scopeID, &same}
		}
		if err := rows.Scan(scan...); err != nil {
			return nil, lexicalIntegrityError(ctx, "read scoped lexical proposal", err)
		}
		if len(id) != len(mousa.SegmentID{}) || len(digest) != len(mousa.SHA256{}) || !scopeID.Valid ||
			scopeID.Int64 != scope.id || !same.Valid || !same.Bool || math.IsNaN(candidate.Score) || math.IsInf(candidate.Score, 0) {
			return nil, integrity("read scoped lexical proposal", "shard projection disagrees with shared index")
		}
		if mixed {
			lengths, err := decodeSQLiteVarints(size, 1)
			if err != nil || lengths[0] == 0 {
				return nil, integrity("read scoped lexical proposal", "missing or invalid document size")
			}
			candidate.length = lengths[0]
		}
		candidate.SegmentID = mousa.SegmentID(id)
		candidate.ContentSHA256 = mousa.SHA256(digest)
		candidate.Sources = scope.sources
		proposals = append(proposals, candidate)
	}
	return proposals, classify("finish scoped lexical proposals", rows.Err())
}

func verifyScopedLexicalRow(ctx context.Context, q queryer, scope lexicalScope, rowID int64, segment mousa.Segment, text []byte) error {
	if err := verifyLexicalRow(ctx, q, rowID, segment); err != nil {
		return err
	}
	var indexed string
	if err := q.QueryRowContext(ctx, `SELECT text FROM `+scope.table()+` WHERE rowid = ?`, rowID).Scan(&indexed); err != nil {
		return lexicalIntegrityError(ctx, "read scoped lexical row", err)
	}
	if indexed != string(text) {
		return integrity("verify scoped lexical row", "shard text disagrees with write")
	}
	return nil
}

func verifyScopedLexical(ctx context.Context, db *sql.DB, scopes []lexicalScope, readOnly bool) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return classify("verify lexical scopes", err)
	}
	defer conn.Close()
	for _, scope := range scopes {
		for _, id := range scope.sources {
			if _, err := getSource(ctx, conn, id); err != nil {
				return lexicalStructuralError("verify lexical scope source", err)
			}
		}
		table := scope.table()
		if !readOnly {
			if _, err := conn.ExecContext(ctx, `INSERT INTO `+table+`(`+table+`) VALUES('integrity-check')`); err != nil {
				return lexicalIntegrityError(ctx, "verify scoped lexical index", err)
			}
		}
		if err := verifyScopedLexicalMembers(ctx, conn, scope); err != nil {
			return err
		}
	}
	return nil
}

func verifyScopedLexicalMembers(ctx context.Context, q queryer, scope lexicalScope) error {
	table := scope.table()
	var mismatch bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` shard
		LEFT JOIN segment_lexical_rows r ON r.rowid = shard.rowid
		WHERE r.scope_id IS NOT ?)`, scope.id).Scan(&mismatch)
	if err != nil {
		return lexicalIntegrityError(ctx, "verify lexical scope rows", err)
	}
	if mismatch {
		return integrity("verify lexical scope rows", "shard has an unowned row")
	}
	rows, err := q.QueryContext(ctx, `
		SELECT s.representation_id, coalesce(shard.text = f.text, 0)
		FROM segment_lexical_rows r JOIN segments s ON s.id = r.segment_id
		LEFT JOIN `+table+` shard ON shard.rowid = r.rowid
		LEFT JOIN segment_lexical_fts f ON f.rowid = r.rowid
		WHERE r.scope_id = ? ORDER BY s.representation_id`, scope.id)
	if err != nil {
		return lexicalIntegrityError(ctx, "verify lexical scope members", err)
	}
	defer rows.Close()
	var previous mousa.RepresentationID
	for rows.Next() {
		var raw []byte
		var equal bool
		if err := rows.Scan(&raw, &equal); err != nil {
			return lexicalIntegrityError(ctx, "verify lexical scope member", err)
		}
		if !equal || len(raw) != len(previous) {
			return integrity("verify lexical scope member", "shard text is missing or disagrees with shared index")
		}
		id := mousa.RepresentationID(raw)
		if id == previous {
			continue
		}
		visited := 0
		paths, err := lexicalRepresentationPaths(ctx, q, id, nil, make(map[mousa.RepresentationID]struct{}), &visited)
		if err != nil {
			return err
		}
		if !slices.Equal(scope.sources, evidenceSourceIDs(paths)) {
			return integrity("verify lexical scope member", "shard source IDs disagree with canonical ancestry")
		}
		previous = id
	}
	return classify("finish lexical scope verification", rows.Err())
}
