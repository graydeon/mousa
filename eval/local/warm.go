// Command local measures query stages on copies of a closed CLI fixture store.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

type stageCost struct {
	Stage          string  `json:"stage"`
	SamplesNS      []int64 `json:"samples_ns"`
	AllocatedBytes uint64  `json:"allocated_bytes"`
	Allocations    uint64  `json:"allocations"`
	Candidates     int     `json:"candidates"`
	UsedBytes      uint64  `json:"used_bytes"`
}

var expressionSink string

func main() {
	fixture := flag.String("store", "", "closed, checkpointed fixture store; never modified")
	root := flag.String("root", "", "directory source identity used to create the fixture")
	query := flag.String("query", "cedar", "query used by the cold CLI comparison")
	iterations := flag.Int("iterations", 30, "measured calls per stage")
	budget := flag.Uint64("budget", 128, "released-text byte budget")
	flag.Parse()
	if *fixture == "" || *root == "" || *iterations < 1 || *iterations > 1000 || *budget == 0 {
		fmt.Fprintln(os.Stderr, "require -store, -root, 1..1000 iterations, and positive budget")
		os.Exit(2)
	}
	if info, err := os.Stat(*fixture + "-wal"); err == nil && info.Size() != 0 {
		fmt.Fprintln(os.Stderr, "fixture has a nonempty WAL; close and checkpoint it first")
		os.Exit(1)
	} else if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	absolute, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sourceID, err := mousa.NewSourceID("mousa-local", absolute)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	costs := []stageCost{}
	for _, stage := range []string{"preparation", "warm_verified", "fresh_current_query"} {
		cost, err := measureStage(*fixture, sourceID, *query, stage, *iterations, *budget)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		costs = append(costs, cost)
	}
	report := struct {
		Protocol    string      `json:"protocol"`
		GoVersion   string      `json:"go_version"`
		Iterations  int         `json:"iterations"`
		Budget      uint64      `json:"budget_bytes"`
		Query       string      `json:"query"`
		Limitations string      `json:"limitations"`
		Stages      []stageCost `json:"stages"`
	}{
		Protocol: "mousa-warm-query-cost-v2", GoVersion: runtime.Version(),
		Iterations: *iterations, Budget: *budget, Query: *query,
		Limitations: "Each store stage uses a separate copy of the same closed CLI fixture. Copying, open, and warmup are excluded. Each query writes a fresh trail. Stages are not additive. Allocations are process-wide counter deltas, not peak RSS.",
		Stages:      costs,
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func measureStage(fixture string, sourceID mousa.SourceID, query, stage string, iterations int, budget uint64) (stageCost, error) {
	ctx := context.Background()
	terms, err := mousa.PrepareLexicalTerms(query, false)
	if err != nil {
		return stageCost{}, err
	}
	expression := strings.Join(terms, " OR ")
	var store *sqlite.Store
	if stage != "preparation" {
		work, err := os.MkdirTemp("", "mousa-warm-cost-")
		if err != nil {
			return stageCost{}, err
		}
		defer os.RemoveAll(work)
		input, err := os.Open(fixture)
		if err != nil {
			return stageCost{}, err
		}
		defer input.Close()
		path := filepath.Join(work, "store.sqlite")
		output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return stageCost{}, err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return stageCost{}, copyErr
		}
		if closeErr != nil {
			return stageCost{}, closeErr
		}
		store, err = sqlite.Open(ctx, path)
		if err != nil {
			return stageCost{}, err
		}
		defer store.Close()
		if _, _, err := store.SetSourceAccess(ctx, sourceID, mousa.SourceAccessAllow, "mousa-local.eval"); err != nil {
			return stageCost{}, err
		}
	}
	cost := stageCost{Stage: stage, SamplesNS: make([]int64, iterations)}
	call := func() error {
		switch stage {
		case "preparation":
			terms, err := mousa.PrepareLexicalTerms(query, false)
			if err != nil {
				return err
			}
			expressionSink = strings.Join(terms, " OR ")
		case "warm_verified":
			candidates, err := store.SearchVerifiedLexical(ctx, expression, 100)
			if err != nil {
				return err
			}
			cost.Candidates = len(candidates)
		case "fresh_current_query":
			result, err := store.Query(ctx, mousa.QueryRequest{
				Scope: mousa.Scope{Source: sourceID}, Texts: []string{query},
				Budget: mousa.Budget{Limit: budget, Unit: mousa.BudgetBytes}, Caller: "warm-probe",
			})
			if err != nil {
				return err
			}
			if result.Trail.Packing == nil {
				return fmt.Errorf("fixture authorization denied")
			}
			cost.Candidates, cost.UsedBytes = len(result.Trail.Candidates), result.Trail.Packing.Used
		default:
			return fmt.Errorf("unknown measurement stage %q", stage)
		}
		return nil
	}
	if err := call(); err != nil {
		return stageCost{}, err
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for index := range iterations {
		started := time.Now()
		err := call()
		cost.SamplesNS[index] = time.Since(started).Nanoseconds()
		if err != nil {
			return stageCost{}, err
		}
	}
	runtime.ReadMemStats(&after)
	cost.AllocatedBytes, cost.Allocations = after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs
	return cost, nil
}
