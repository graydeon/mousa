package main

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/graydeon/mousa/eval/beir"
)

func TestCoverageProducerConsumer(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "beir")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	command := func(t *testing.T, want int, args ...string) {
		t.Helper()
		cmd := exec.Command(binary, args...)
		output, err := cmd.CombinedOutput()
		got := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				got = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if got != want {
			t.Fatalf("%v: exit %d want %d\n%s", args, got, want, output)
		}
	}
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(data, "qrels"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"corpus.jsonl":   "{\"_id\":\"a\",\"title\":\"\",\"text\":\"" + strings.Repeat("needle ", 220) + "\"}\n{\"_id\":\"b\",\"title\":\"\",\"text\":\"needle orchard soil\"}\n",
		"queries.jsonl":  "{\"_id\":\"q1\",\"text\":\"needle needle\"}\n{\"_id\":\"q2\",\"text\":\"orchard\"}\n",
		"qrels/test.tsv": "query-id\tcorpus-id\tscore\nq1\ta\t1\nq2\tb\t1\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(data, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	produce := func(mode string, budget int) (string, beir.RunReport) {
		t.Helper()
		out := filepath.Join(dir, mode+strconv.Itoa(budget)+".json")
		command(t, 0, "-data", data, "-dataset", "toy", "-out", out, "-mode", mode, "-budget", strconv.Itoa(budget), "-limit", "100", "-query-timeout", "30s")
		report, err := beir.LoadRunReport(out)
		if err != nil {
			t.Fatal(err)
		}
		return out, report
	}
	lowPath, low := produce("traced", 1024)
	highPath, high := produce("traced", 4096)
	_, zero := produce("traced", 1)
	_, plain := produce("verified", 1024)
	for _, report := range []beir.RunReport{low, high, zero} {
		coverage := report.Aggregate.PackCoverage
		if coverage == nil {
			t.Fatal("serialized traced report omits aggregate coverage")
		}
		var gold float64
		var selected int
		var used uint64
		for _, q := range report.Queries {
			if q.PackCoverage == nil {
				t.Fatal("missing per-query coverage")
			}
			gold += q.PackCoverage.GoldCoverage
			selected += q.PackCoverage.SelectedDocs
			used += q.PackCoverage.UsedBytes
		}
		n := len(report.Queries)
		if coverage.Queries != n || coverage.GoldCoverage != gold/float64(n) || coverage.SelectedDocs != selected/n || coverage.UsedBytes != used/uint64(n) || coverage.Utilisation != float64(used/uint64(n))/float64(report.Budget) {
			t.Fatalf("aggregate disagrees with query facts: %+v", coverage)
		}
	}
	if low.Queries[0].PackCoverage.GoldCoverage != 0 || low.Queries[1].PackCoverage.GoldCoverage != 1 || high.Aggregate.PackCoverage.GoldCoverage != 1 {
		t.Fatalf("unexpected synthetic selection: %+v / %+v", low.Queries, high.Queries)
	}
	if zero.Aggregate.PackCoverage.GoldCoverage != 0 || zero.Aggregate.PackCoverage.UsedBytes != 0 {
		t.Fatal("zero coverage not preserved")
	}
	if plain.Aggregate.PackCoverage != nil {
		t.Fatal("non-traced aggregate coverage present")
	}
	for i, q := range plain.Queries {
		if q.PackCoverage != nil || !reflect.DeepEqual(q.RankedDocIDs, low.Queries[i].RankedDocIDs) || !reflect.DeepEqual(q.RankedDocIDs, high.Queries[i].RankedDocIDs) {
			t.Fatal("packing changed candidate ranking or non-traced omission")
		}
	}
	curvePath := filepath.Join(dir, "curve.json")
	command(t, 0, "-report", "coverage", "-reports", lowPath+","+highPath, "-out", curvePath)
	raw, err := os.ReadFile(curvePath)
	if err != nil {
		t.Fatal(err)
	}
	var curve coverageReport
	if err := json.Unmarshal(raw, &curve); err != nil {
		t.Fatal(err)
	}
	if len(curve.Points) != 2 || math.Abs(curve.Points[0].GoldCoverage-0.5) > 1e-12 || curve.Points[1].GoldCoverage != 1 {
		t.Fatalf("wrong native curve: %+v", curve)
	}
	for name, mutate := range map[string]func(*beir.RunReport){
		"corpus":             func(r *beir.RunReport) { r.Content.CorpusSHA256 = "different" },
		"queries-content":    func(r *beir.RunReport) { r.Content.QueriesSHA256 = "different" },
		"qrels":              func(r *beir.RunReport) { r.Content.QrelsSHA256 = "different" },
		"protocol":           func(r *beir.RunReport) { r.Protocol.Name = "different" },
		"wrong-id":           func(r *beir.RunReport) { r.Queries[0].QueryID = "wrong" },
		"duplicate":          func(r *beir.RunReport) { r.Queries[1].QueryID = r.Queries[0].QueryID },
		"empty-id":           func(r *beir.RunReport) { r.Queries[0].QueryID = "" },
		"missing-population": func(r *beir.RunReport) { r.Queries = nil },
		"coverage-count":     func(r *beir.RunReport) { r.Aggregate.PackCoverage.Queries++ },
		"aggregate-count":    func(r *beir.RunReport) { r.Aggregate.Queries++ },
		"slice":              func(r *beir.RunReport) { r.QueryLimit = 1 },
		"legacy":             func(r *beir.RunReport) { r.Content = beir.ContentIdentity{} },
	} {
		t.Run(name, func(t *testing.T) {
			changed, err := beir.LoadRunReport(highPath)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			path := filepath.Join(dir, name+".json")
			writeReport(t, path, changed)
			command(t, 1, "-report", "coverage", "-reports", lowPath+","+path, "-out", filepath.Join(dir, name+"-curve.json"))
			if _, err := beir.CoverageCurve([]beir.RunReport{low, changed}); err == nil {
				t.Fatal("domain API accepted incomparable evidence")
			}
		})
	}
}
