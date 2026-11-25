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
