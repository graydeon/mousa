// Command contention_probe measures query latency with concurrent queriers while a real
// `mousa sync` writes to the same store, for two trail placements:
//
//	A  one writer transaction per query: Store.EvaluateAndTraceLexical, today's
//	   CLI query path (decision, retrieval, packing, trail insert and read-back
//	   under BEGIN IMMEDIATE).
//	B  a short writer transaction plus a read transaction:
//	   Store.EvaluateSourceRetrieval (re-reads access, inserts the decision,
//	   reads it back) then Store.SearchEnforcedLexical (read-only retrieval).
//
// B stands in for "query in a read transaction, write the trail in a short
// write transaction afterwards". Its write is the decision record, not a trail
// with candidate rows, and it runs before the read rather than after; the lock
// pattern (one short write per query, retrieval outside the write lock) is the
// part being measured. Each querier owns one Store, so one SQLite connection.
// Stores open one after another: open takes the write lock for its checks.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

const (
	candidateLimit = 100
	budgetBytes    = 8 << 10
)

type sample struct {
	at      time.Duration
	latency time.Duration
	err     string
}

type phase struct {
	Queries  int            `json:"queries"`
	P50MS    float64        `json:"p50_ms"`
	P99MS    float64        `json:"p99_ms"`
	MaxMS    float64        `json:"max_ms"`
	PerSec   float64        `json:"queries_per_s"`
	Errors   map[string]int `json:"errors"`
	WindowMS float64        `json:"window_ms"`
}

func main() {
	bin := flag.String("bin", "", "mousa binary used for the concurrent sync")
	storePath := flag.String("store", "", "store holding the queried source; it gains decisions and trails")
	source := flag.String("source", "", "external ID of the queried JSONL source")
	layout := flag.String("layout", "A", "A or B")
	queriers := flag.Int("queriers", 16, "concurrent queriers, one Store each")
	queriesFile := flag.String("queries", "", "query texts, one per line")
	syncSource := flag.String("sync-source", "", "external ID synced during the run (empty: no sync)")
	syncFile := flag.String("sync-file", "", "JSONL records for the concurrent sync")
	warmup := flag.Duration("warmup", 20*time.Second, "idle window before the sync starts")
	tail := flag.Duration("tail", 10*time.Second, "window after the sync ends")
	out := flag.String("out", "", "JSON report path")
	flag.Parse()
	if *storePath == "" || *source == "" || *queriesFile == "" || (*layout != "A" && *layout != "B") || *queriers < 1 {
		fail(fmt.Errorf("need -store, -source, -queries, -layout A|B and -queriers >= 1"))
	}
	ctx := context.Background()
	queries, err := readLines(*queriesFile)
	if err != nil {
		fail(err)
	}
	sourceID, err := mousa.NewSourceID("mousa-jsonl", *source)
	if err != nil {
		fail(err)
	}
	openStart := time.Now()
	stores := make([]*sqlite.Store, *queriers)
	for i := range stores {
		if stores[i], err = sqlite.Open(ctx, *storePath); err != nil {
			fail(err)
		}
	}
	openTime := time.Since(openStart)
	var wg sync.WaitGroup

	run := func(store *sqlite.Store, text string) error {
		terms, err := mousa.PrepareLexicalTerms(text, false)
		if err != nil {
			return err
		}
		expression := strings.Join(terms, " OR ")
		request, err := newRequest(sourceID)
		if err != nil {
			return err
		}
		if *layout == "A" {
			_, err = store.EvaluateAndTraceLexical(ctx, request, expression, candidateLimit, budgetBytes, mousa.PackingOriginal)
			return err
		}
		if _, err := store.EvaluateSourceRetrieval(ctx, request); err != nil {
			return err
		}
		_, err = store.SearchEnforcedLexical(ctx, request, expression, candidateLimit)
		return err
	}

	start := time.Now()
	stop := make(chan struct{})
	results := make([][]sample, *queriers)
	for i, store := range stores {
		wg.Go(func() {
			for n := i; ; n += *queriers {
				select {
				case <-stop:
					return
				default:
				}
				t := time.Now()
				err := run(store, queries[n%len(queries)])
				s := sample{at: t.Sub(start), latency: time.Since(t)}
				if err != nil {
					s.err = errorKey(err)
				}
				results[i] = append(results[i], s)
			}
		})
	}

	time.Sleep(*warmup)
	syncStart := time.Since(start)
	syncReport := map[string]any{"ran": false}
	if *syncSource != "" {
		input, err := os.Open(*syncFile)
		if err != nil {
			fail(err)
		}
		cmd := exec.Command(*bin, "-store", *storePath, "sync", "--source", *syncSource, "--allow")
		cmd.Stdin = input
		var stderr strings.Builder
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		input.Close()
		syncReport = map[string]any{"ran": true, "exit_code": cmd.ProcessState.ExitCode(),
			"wall_ms": ms(time.Since(start) - syncStart), "stderr_tail": lastBytes(stderr.String(), 400)}
		if runErr != nil {
			syncReport["error"] = runErr.Error()
		}
	}
	syncEnd := time.Since(start)
	time.Sleep(*tail)
	close(stop)
	wg.Wait()
	end := time.Since(start)
	for _, store := range stores {
		store.Close()
	}

	var all []sample
	for _, r := range results {
		all = append(all, r...)
	}
	window := func(from, to time.Duration) phase {
		var in []sample
		for _, s := range all {
			if s.at >= from && s.at < to {
				in = append(in, s)
			}
		}
		return summarize(in, to-from)
	}
	report := map[string]any{
		"protocol": "mousa-contention-v1", "layout": *layout, "queriers": *queriers,
		"store": *storePath, "candidate_limit": candidateLimit, "budget_bytes": budgetBytes,
		"open_all_ms": ms(openTime), "sync": syncReport,
		"idle":   window(0, syncStart),
		"during": window(syncStart, syncEnd),
		"after":  window(syncEnd, end),
		"note":   "latency is the wall time of one query call in this process; it excludes store open and JSON output",
	}
	data, _ := json.MarshalIndent(report, "", " ")
	fmt.Println(string(data))
	if *out != "" {
		if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
			fail(err)
		}
	}
}

func newRequest(sourceID mousa.SourceID) (mousa.PolicyEvaluationRequest, error) {
	request := mousa.PolicyEvaluationRequest{
		Schema: mousa.PolicyEvaluationRequestSchema, Action: mousa.SourceRetrievalAction,
		CallerNamespace: "mousa-local.caller", ExternalCallerID: "probe",
		ExternalRequestID: "probe-" + rand.Text(),
		PurposeNamespace:  "mousa-local.purpose", ExternalPurposeID: "retrieval",
		SourceID: sourceID, RequestedAtUsec: time.Now().UnixMicro(),
	}
	id, err := mousa.NewPolicyEvaluationRequestID(request)
	request.ID = id
	return request, err
}

func summarize(in []sample, span time.Duration) phase {
	p := phase{Queries: len(in), Errors: map[string]int{}, WindowMS: ms(span)}
	var latencies []time.Duration
	for _, s := range in {
		if s.err != "" {
			p.Errors[s.err]++
		}
		latencies = append(latencies, s.latency)
	}
	if len(latencies) == 0 {
		return p
	}
	slices.Sort(latencies)
	at := func(q float64) float64 { return ms(latencies[min(len(latencies)-1, int(q*float64(len(latencies))))]) }
	p.P50MS, p.P99MS, p.MaxMS = at(0.50), at(0.99), ms(latencies[len(latencies)-1])
	p.PerSec = float64(len(in)) / span.Seconds()
	return p
}

func errorKey(err error) string {
	for _, code := range []sqlite.Code{sqlite.CodeBusy, sqlite.CodeConflict, sqlite.CodeIntegrity, sqlite.CodeNotFound} {
		if sqlite.IsCode(err, code) {
			return string(code)
		}
	}
	return lastBytes(err.Error(), 80)
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("%s has no queries", path)
	}
	return lines, scanner.Err()
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func lastBytes(s string, n int) string { return s[max(0, len(s)-n):] }

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
