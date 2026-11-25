# Test fixtures

`schema7_fixture.sqlite.gz` — a schema-7 store captured from a build of the
tree before the paths feature. Contents, all synthetic:

- source `fixture-docs` (JSONL): items `docs/one` and `.env`, two revisions
  of `docs/one` so the store has one item_change history
- source `fixture-notes` (JSONL): a standalone note and a note citing the
  fixture trail below
- one offline query trail over `fixture-docs`, one served request
  admission/outcome pair

`schema7_notes.jsonl` — the exact `notes export --source fixture-notes`
output of the pre-migration binary against the fixture above; migration is
expected to preserve every field and add only `path`.

## Regenerating

From a checkout at the pre-migration commit, build and seed a temp store:

    go build -o /tmp/mousa-schema7 ./cmd/mousa
    M=/tmp/mousa-schema7; S=$(mktemp -d)/store.sqlite
    $M -store $S access --source fixture-docs --create allow
    $M -store $S access --source fixture-notes --create allow
    printf '%s\n' '{"id":"docs/one","text":"fixture document alpha"}' \
        '{"id":".env","text":"fixture dotfile entry"}' \
        | $M -store $S sync --source fixture-docs --allow
    printf '%s\n' '{"id":"docs/one","text":"fixture document alpha revised"}' \
        | $M -store $S sync --source fixture-docs
    $M -store $S query --source fixture-docs 'fixture document' > /tmp/q.json
    TRAIL=$(python3 -c 'import json,sys; print(json.load(open("/tmp/q.json"))["trail"]["id"])')
    $M -store $S remember --source fixture-notes 'standalone fixture note'
    $M -store $S remember --source fixture-notes --from "$TRAIL" 'cited fixture note citing trail'
    # seed one served admission/outcome pair first: run `serve --config`
    # with a throwaway principal config and post one request
    gzip -c $S > schema7_fixture.sqlite.gz
    $M -store $S notes export --source fixture-notes > schema7_notes.jsonl

The served request pair was added by running `serve --config` against a
throwaway principal config and posting one query; any equivalent pair works.

`schema8_fixture.sqlite.gz` — a schema-8 store captured from a build at the
schema-8 baseline commit. Contents, all synthetic:

- source `fixture-docs` (JSONL): items `docs/one` and `docs/two`; `docs/one`
  has two revisions with two retired segment rows that have no
  lexical rows and two active indexed segments — and `docs/two` was moved to
  `shelf/two`, so the store has retired and active segments plus a path move
- source `fixture-notes` (JSONL): a standalone note, a note citing the
  fixture trail, and a superseded note plus its replacement
- two offline query trails over `fixture-docs` — one recorded before the
  `docs/one` update so it cites the retired segments, one after — and one
  served request admission/outcome pair

`schema8_notes.jsonl` — the exact `notes export --source fixture-notes`
output of the schema-8 binary against the fixture above; migration is
expected to preserve every byte.

## Regenerating schema8_fixture.sqlite.gz

From a checkout at the schema-8 baseline commit, build and seed a temp
store:

    go build -o /tmp/mousa-schema8 ./cmd/mousa
    M=/tmp/mousa-schema8; S=$(mktemp -d)/store.sqlite
    $M -store $S access --source fixture-docs --create allow
    $M -store $S access --source fixture-notes --create allow
    # v1 of docs/one is multi-segment so retired rows cover several segments
    python3 -c 'import json; \
        print(json.dumps({"id":"docs/one","text":"Fixture document one about otters and riverbanks. "*120})); \
        print(json.dumps({"id":"docs/two","text":"Fixture document two about cedars and ridgelines."}))' \
        | $M -store $S sync --source fixture-docs
    $M -store $S query --source fixture-docs 'otters' > /tmp/q1.json
    python3 -c 'import json; \
        print(json.dumps({"id":"docs/one","text":"Fixture document one revised, now about herons and estuaries. "*100}))' \
        | $M -store $S sync --source fixture-docs
    $M -store $S query --source fixture-docs 'estuaries' > /tmp/q2.json
    TRAIL=$(python3 -c 'import json; print(json.load(open("/tmp/q2.json"))["trail"]["id"])')
    $M -store $S remember --source fixture-notes 'Standalone fixture note about otters.'
    $M -store $S remember --source fixture-notes --from "$TRAIL" 'Cited fixture note grounded in the estuaries trail.'
    NOTE1=$($M -store $S remember --source fixture-notes 'First version fixture note for supersession.' \
        | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
    $M -store $S remember --source fixture-notes --supersedes "$NOTE1" 'Replacement fixture note that supersedes the first.'
    $M -store $S move --source fixture-docs --item docs/two --to shelf/two
    # seed one served admission/outcome pair: run `serve --config` with a
    # throwaway principal config and post one query
    gzip -n -c $S > schema8_fixture.sqlite.gz
    $M -store $S notes export --source fixture-notes > schema8_notes.jsonl
