package sqlite

import (
	"fmt"
	"reflect"
	"testing"
)

func TestLexicalStoreTemplateIsolation(t *testing.T) {
	t.Parallel()
	fresh := openFreshLexicalStore(t)
	defer fresh.Close()
	schema := func(t *testing.T, store *Store) []string {
		t.Helper()
		rows, err := store.db.Query(`SELECT type, name, tbl_name, coalesce(sql, '') FROM sqlite_master ORDER BY type, name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var result []string
		for rows.Next() {
			var kind, name, table, sql string
			if err := rows.Scan(&kind, &name, &table, &sql); err != nil {
				t.Fatal(err)
			}
			result = append(result, fmt.Sprint([]string{kind, name, table, sql}))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	expected := schema(t, fresh)
	for i := range 6 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			first := openLexicalStore(t)
			defer first.Close()
			second := openLexicalStore(t)
			defer second.Close()
			if first.path == second.path || !reflect.DeepEqual(schema(t, first), expected) || !reflect.DeepEqual(schema(t, second), expected) {
				t.Fatal("fixture path or schema differs from independent fresh store")
			}
			addLexicalDocument(t, first, fmt.Sprintf("isolated-%d", i), "independent fixture evidence")
			for _, entry := range []struct {
				store *Store
				want  int
			}{{first, 1}, {second, 0}} {
				var count int
				if err := entry.store.db.QueryRow(`SELECT count(*) FROM sources`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != entry.want {
					t.Fatalf("fixture writes leaked: got%d want%d", count, entry.want)
				}
			}
		})
	}
}
