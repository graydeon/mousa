package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"

	"github.com/graydeon/mousa/internal/mousa"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrationNamePattern = regexp.MustCompile(`^(\d{4})_([a-z][a-z0-9]*(?:_[a-z0-9]+)*)\.sql$`)

type migration struct {
	version int
	name    string
	sql     []byte
	hash    [32]byte
}

var requiredObjectsV1 = []string{
	"index:artifacts_observation_id_idx",
	"index:observations_source_id_idx",
	"index:representation_inputs_artifact_id_idx",
	"index:representation_inputs_representation_id_idx",
	"index:segments_representation_id_idx",
	"table:artifacts",
	"table:observations",
	"table:representation_inputs",
	"table:representations",
	"table:schema_migrations",
	"table:segments",
	"table:sources",
}

var requiredObjectsV2 = append(append([]string(nil), requiredObjectsV1...),
	"index:ingest_receipts_source_id_idx",
	"index:source_withdrawals_source_id_idx",
	"table:ingest_gaps",
	"table:ingest_receipts",
	"table:source_ingest_state",
	"table:source_withdrawals",
)

var requiredObjectsV3 = append(append([]string(nil), requiredObjectsV2...),
	"table:segment_lexical_fts",
	"table:segment_lexical_fts_config",
	"table:segment_lexical_fts_content",
	"table:segment_lexical_fts_data",
	"table:segment_lexical_fts_docsize",
	"table:segment_lexical_fts_idx",
	"table:segment_lexical_rows",
)

var requiredObjectsV4 = append(append([]string(nil), requiredObjectsV3...),
	"index:classification_bases_artifact_id_idx",
	"index:classification_bases_observation_id_idx",
	"index:classification_bases_representation_id_idx",
	"index:classification_bases_segment_id_idx",
	"index:classification_bases_source_id_idx",
	"index:classifications_subject_artifact_id_idx",
	"index:classifications_subject_observation_id_idx",
	"index:classifications_subject_representation_id_idx",
	"index:classifications_subject_segment_id_idx",
	"index:classifications_subject_source_id_idx",
	"table:classification_bases",
	"table:classifications",
)

var requiredObjectsV5 = append(append([]string(nil), requiredObjectsV4...),
	"table:policy_definitions",
)

var requiredObjectsV6 = append(append([]string(nil), requiredObjectsV5...),
	"index:policy_activations_active_binding_id_idx",
	"index:policy_activations_predecessor_idx",
	"index:policy_activations_root_idx",
	"index:policy_activations_series_idx",
	"index:policy_bindings_definition_id_idx",
	"index:policy_bindings_scope_idx",
	"index:policy_binding_state_active_binding_id_idx",
	"table:policy_activations",
	"table:policy_binding_state",
	"table:policy_bindings",
)

var requiredObjectsV7 = append(append([]string(nil), requiredObjectsV6...),
	"table:policy_decision_inputs",
	"table:policy_decisions",
)

func migrate(ctx context.Context, db *sql.DB) error {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		return wrap(CodeInternal, "load migration", err)
	}

	applicationIDValue, objects, err := inspectDatabase(ctx, db)
	if err != nil {
		return startupError("inspect database", err)
	}
	switch applicationIDValue {
	case 0:
		if len(objects) != 0 {
			return wrap(CodeIncompatibleSchema, "inspect database", errors.New("application ID is zero but user objects exist"))
		}
	case applicationID:
		version, err := databaseVersion(ctx, db)
		if err != nil {
			return err
		}
		if version > len(migrations) {
			return wrap(CodeIncompatibleSchema, "verify migrations", fmt.Errorf("schema version %d is newer than %d", version, len(migrations)))
		}
		if err := verifyVersion(ctx, db, migrations, version, true); err != nil {
			return err
		}
		if version == len(migrations) {
			return nil
		}
	default:
		return wrap(CodeIncompatibleSchema, "inspect database", fmt.Errorf("foreign SQLite application ID %d", applicationIDValue))
	}

	version, err := databaseVersion(ctx, db)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return startupError("acquire migration connection", err)
	}
	defer conn.Close()
	for _, migration := range migrations[version:] {
		if err := applyMigration(ctx, conn, migration); err != nil {
			return err
		}
	}
	if err := conn.Close(); err != nil {
		return wrap(CodeInternal, "release migration connection", err)
	}
	return verifyVersion(ctx, db, migrations, len(migrations), true)
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	paths, err := fs.Glob(fsys, "migrations/*")
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, errors.New("migration set is empty")
	}
	sort.Strings(paths)
	migrations := make([]migration, 0, len(paths))
	seen := make(map[int]struct{}, len(paths))
	for _, path := range paths {
		base := path[len("migrations/"):]
		match := migrationNamePattern.FindStringSubmatch(base)
		if match == nil {
			return nil, fmt.Errorf("malformed migration name %q", base)
		}
		version, err := strconv.Atoi(match[1])
		if err != nil || version < 1 {
			return nil, fmt.Errorf("invalid migration version in %q", base)
		}
		if _, duplicate := seen[version]; duplicate {
			return nil, fmt.Errorf("duplicate migration version %04d", version)
		}
		seen[version] = struct{}{}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", base, err)
		}
		if len(data) == 0 || len(data) > 1024*1024 {
			return nil, fmt.Errorf("invalid migration size %d for %q", len(data), base)
		}
		migrations = append(migrations, migration{version: version, name: match[2], sql: data, hash: sha256.Sum256(data)})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	for index, migration := range migrations {
		if migration.version != index+1 {
			return nil, fmt.Errorf("migration version gap before %04d", migration.version)
		}
	}
	return migrations, nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, migration migration) error {
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return startupError("begin migration", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	label := fmt.Sprintf("%04d_%s.sql", migration.version, migration.name)
	if _, err := conn.ExecContext(ctx, string(migration.sql)); err != nil {
		return startupError("apply migration "+label, err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, sha256) VALUES(?, ?, ?)`, migration.version, migration.name, migration.hash[:]); err != nil {
		return startupError("record migration "+label, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return startupError("commit migration "+label, err)
	}
	committed = true
	return nil
}

func verifyVersion(ctx context.Context, db *sql.DB, embedded []migration, wantVersion int, requireWAL bool) error {
	applicationIDValue, objects, err := inspectDatabase(ctx, db)
	if err != nil {
		return startupError("verify database", err)
	}
	if applicationIDValue != applicationID {
		return integrity("verify database", fmt.Sprintf("application ID is %d", applicationIDValue))
	}
	hasMigrationTable := false
	for _, object := range objects {
		if object == "table:schema_migrations" {
			hasMigrationTable = true
			break
		}
	}
	if !hasMigrationTable {
		return integrity("verify database", "schema_migrations is missing")
	}

	rows, err := db.QueryContext(ctx, `SELECT version, name, sha256 FROM schema_migrations ORDER BY version`)
	if err != nil {
		return startupError("verify migrations", err)
	}
	type migrationRow struct {
		version int
		name    string
		hash    []byte
	}
	var migrations []migrationRow
	for rows.Next() {
		var row migrationRow
		if err := rows.Scan(&row.version, &row.name, &row.hash); err != nil {
			rows.Close()
			return startupError("verify migrations", err)
		}
		migrations = append(migrations, row)
	}
	if err := rows.Close(); err != nil {
		return startupError("verify migrations", err)
	}
	if len(migrations) != wantVersion {
		if len(migrations) > 0 && migrations[len(migrations)-1].version > len(embedded) {
			return wrap(CodeIncompatibleSchema, "verify migrations", fmt.Errorf("schema version %d is newer than %d", migrations[len(migrations)-1].version, len(embedded)))
		}
		return integrity("verify migrations", fmt.Sprintf("migration row count is %d", len(migrations)))
	}
	for index, applied := range migrations {
		want := embedded[index]
		if applied.version != want.version || applied.name != want.name || !equalBytes(applied.hash, want.hash[:]) {
			return integrity("verify migrations", "migration name or hash disagrees with embedded migration")
		}
	}
	requiredObjects := requiredObjectsV1
	if wantVersion == 2 {
		requiredObjects = requiredObjectsV2
	} else if wantVersion == 3 {
		requiredObjects = requiredObjectsV3
	} else if wantVersion == 4 {
		requiredObjects = requiredObjectsV4
	} else if wantVersion == 5 {
		requiredObjects = requiredObjectsV5
	} else if wantVersion == 6 {
		requiredObjects = requiredObjectsV6
	} else if wantVersion == 7 {
		requiredObjects = requiredObjectsV7
	}
	if !equalStringSets(objects, requiredObjects) {
		return integrity("verify database", fmt.Sprintf("schema objects are %v, want %v", objects, requiredObjects))
	}

	var quickCheck string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&quickCheck); err != nil {
		return startupError("quick check", err)
	}
	if quickCheck != "ok" {
		return integrity("quick check", quickCheck)
	}
	foreignRows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return startupError("foreign key check", err)
	}
	if foreignRows.Next() {
		foreignRows.Close()
		return integrity("foreign key check", "foreign-key violation")
	}
	if err := foreignRows.Close(); err != nil {
		return startupError("foreign key check", err)
	}
	if err := verifyPragmas(ctx, db, requireWAL); err != nil {
		return err
	}
	if err := verifyCanonicalRecords(ctx, db); err != nil {
		return err
	}
	if wantVersion >= 2 {
		if err := verifyIngestRecords(ctx, db); err != nil {
			return err
		}
	}
	if wantVersion >= 3 {
		if err := verifyLexicalRecords(ctx, db); err != nil {
			return err
		}
	}
	if wantVersion >= 4 {
		if err := verifyClassificationRecords(ctx, db); err != nil {
			return err
		}
	}
	if wantVersion >= 5 {
		if err := verifyPolicyDefinitionRecords(ctx, db); err != nil {
			return err
		}
	}
	if wantVersion >= 6 {
		if err := verifyPolicyBindingRecords(ctx, db); err != nil {
			return err
		}
	}
	if wantVersion >= 7 {
		if err := verifyPolicyDecisionRecords(ctx, db); err != nil {
			return err
		}
	}
	return nil
}

func databaseVersion(ctx context.Context, db *sql.DB) (int, error) {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&exists); err != nil {
		return 0, startupError("inspect migration version", err)
	}
	if exists == 0 {
		return 0, nil
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, startupError("inspect migration version", err)
	}
	return version, nil
}

func inspectDatabase(ctx context.Context, db *sql.DB) (int, []string, error) {
	var id int
	if err := db.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&id); err != nil {
		return 0, nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT type, name FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var objects []string
	for rows.Next() {
		var objectType, name string
		if err := rows.Scan(&objectType, &name); err != nil {
			return 0, nil, err
		}
		objects = append(objects, objectType+":"+name)
	}
	return id, objects, rows.Err()
}

func verifyPragmas(ctx context.Context, db *sql.DB, requireWAL bool) error {
	queryOnly := 1
	if requireWAL {
		queryOnly = 0
	}
	checks := []struct {
		pragma string
		want   int
	}{
		{"foreign_keys", 1},
		{"busy_timeout", 1000},
		{"synchronous", 2},
		{"wal_autocheckpoint", 1000},
		{"trusted_schema", 0},
		{"query_only", queryOnly},
	}
	for _, check := range checks {
		var got int
		if err := db.QueryRowContext(ctx, `PRAGMA `+check.pragma).Scan(&got); err != nil {
			return startupError("verify pragma "+check.pragma, err)
		}
		if got != check.want {
			return integrity("verify pragma "+check.pragma, fmt.Sprintf("value is %d, want %d", got, check.want))
		}
	}
	if requireWAL {
		var journalMode string
		if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			return startupError("verify pragma journal_mode", err)
		}
		if journalMode != "wal" {
			return integrity("verify pragma journal_mode", fmt.Sprintf("value is %q, want wal", journalMode))
		}
		if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=OFF`).Scan(&journalMode); err != nil {
			return startupError("verify defensive mode", err)
		}
		if journalMode != "wal" {
			return integrity("verify defensive mode", fmt.Sprintf("journal mode changed to %q", journalMode))
		}
	}
	var fts5 int
	if err := db.QueryRowContext(ctx, `SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&fts5); err != nil {
		return startupError("verify FTS5", err)
	}
	if fts5 != 1 {
		return wrap(CodeIncompatibleSchema, "verify FTS5", errors.New("SQLite binding lacks ENABLE_FTS5"))
	}
	return nil
}

func verifyCanonicalRecords(ctx context.Context, db *sql.DB) error {
	checks := []struct {
		table string
		get   func([]byte) error
	}{
		{"sources", func(raw []byte) error {
			var id mousa.SourceID
			copy(id[:], raw)
			_, err := getSource(ctx, db, id)
			return err
		}},
		{"observations", func(raw []byte) error {
			var id mousa.ObservationID
			copy(id[:], raw)
			_, err := getObservation(ctx, db, id)
			return err
		}},
		{"artifacts", func(raw []byte) error {
			var id mousa.ArtifactID
			copy(id[:], raw)
			_, err := getArtifact(ctx, db, id)
			return err
		}},
		{"representations", func(raw []byte) error {
			var id mousa.RepresentationID
			copy(id[:], raw)
			_, err := getRepresentation(ctx, db, id)
			return err
		}},
		{"segments", func(raw []byte) error {
			var id mousa.SegmentID
			copy(id[:], raw)
			_, err := getSegment(ctx, db, id)
			return err
		}},
	}
	for _, check := range checks {
		rows, err := db.QueryContext(ctx, `SELECT id FROM `+check.table+` ORDER BY id`)
		if err != nil {
			return startupError("scan "+check.table, err)
		}
		var ids [][]byte
		for rows.Next() {
			var id []byte
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return startupError("scan "+check.table, err)
			}
			ids = append(ids, append([]byte(nil), id...))
		}
		if err := rows.Close(); err != nil {
			return startupError("scan "+check.table, err)
		}
		for _, id := range ids {
			if len(id) != 32 {
				return integrity("scan "+check.table, "invalid ID length")
			}
			if err := check.get(id); err != nil {
				return err
			}
		}
	}
	return nil
}

func startupError(op string, err error) error {
	var storageErr *Error
	if errors.As(err, &storageErr) {
		return err
	}
	classified := classify(op, err)
	if IsCode(classified, CodeIntegrity) {
		return classified
	}
	return classified
}

func equalStringSets(left, right []string) bool {
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
