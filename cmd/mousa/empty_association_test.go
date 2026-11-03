package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestNativeEmptyAssociationCompatibility(t *testing.T) {
	t.Parallel()
	for _, form := range []string{"directory", "jsonl"} {
		t.Run(form, func(t *testing.T) {
			run, root := setup(t)
			selector := []string{root}
			if form == "directory" {
				writeFile(t, root, "a.txt", "needle shared evidence")
				writeFile(t, root, "b.txt", "needle shared evidence")
				writeFile(t, root, "c.txt", "needle separate qualification")
				run.run(false, "sync", root)
			} else {
				selector = []string{"--source", "empty-associations"}
				jsonlSync(t, run, selector[1], record("a.txt", "needle shared evidence")+"\n"+record("b.txt", "needle shared evidence")+"\n"+record("c.txt", "needle separate qualification"))
			}
			for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
				for _, mode := range []string{"omitted", "false", "true"} {
					t.Run(packing+"/"+mode, func(t *testing.T) {
						arguments := []string{"query", "--packing-policy", packing}
						if mode != "omitted" {
							arguments = append(arguments, "--supersession="+mode)
						}
						invoke := func(label string, target testRun, empty bool) (int, string, string) {
							t.Helper()
							args := append([]string(nil), arguments...)
							if empty {
								args = append(args, "--associations=")
							}
							args = append(args, selector...)
							args = append(args, "needle")
							code, out, stderr := target.runExit(args...)
							t.Logf("%s argv=%q exit=%d stdout=%q stderr=%q", label, args, code, out, stderr)
							return code, out, stderr
						}
						decode := func(code int, out, stderr string) map[string]any {
							t.Helper()
							if code != 0 || stderr != "" {
								t.Fatalf("query exit=%d stdout=%q stderr=%q", code, out, stderr)
							}
							var report map[string]any
							if err := json.Unmarshal([]byte(out), &report); err != nil {
								t.Fatal(err)
							}
							for _, key := range []string{"request_id", "decision_id", "trail_id", "packet_id"} {
								id, err := mousa.ParseSHA256(report[key].(string))
								if err != nil || id == (mousa.SHA256{}) {
									t.Fatalf("invalid producing %s: %v", key, report[key])
								}
							}
							historyArgs := append([]string{"trail"}, selector...)
							historyArgs = append(historyArgs, report["trail_id"].(string))
							code, out, stderr = run.runExit(historyArgs...)
							t.Logf("history argv=%q exit=%d stdout=%q stderr=%q", historyArgs, code, out, stderr)
							if code != 0 || stderr != "" {
								t.Fatalf("history refused producing query: %d %s", code, stderr)
							}
							var inspection map[string]any
							if err := json.Unmarshal([]byte(out), &inspection); err != nil {
								t.Fatal(err)
							}
							h := inspection["historical"].(map[string]any)
							for _, pair := range [][2]string{{"id", "trail_id"}, {"request_id", "request_id"}, {"decision_id", "decision_id"}, {"packet_id", "packet_id"}} {
								if h[pair[0]] != report[pair[1]] {
									t.Fatalf("history not bound to producing %s", pair[1])
								}
							}
							if inspection["authorization_decision_id"] == report["decision_id"] {
								t.Fatal("history reused producing authorization")
							}
							return report
						}
						baseline := decode(invoke("absent-flag", run, false))
						code, out, stderr := invoke("empty-flag", run, true)
						if mode == "true" {
							if code != 2 || out != "" {
								t.Fatalf("true supersession accepted empty association: %d %q %q", code, out, stderr)
							}
							absent := testRun{t: t, binary: run.binary, store: filepath.Join(t.TempDir(), "absent.sqlite")}
							code, out, stderr = invoke("absent-store", absent, true)
							if code != 2 || out != "" {
								t.Fatalf("empty association crossed open boundary: %d %q %q", code, out, stderr)
							}
							if _, err := os.Stat(absent.store); !errors.Is(err, os.ErrNotExist) {
								t.Fatalf("empty association created store: %v", err)
							}
							return
						}
						empty := decode(code, out, stderr)
						for _, key := range []string{"request_id", "decision_id", "trail_id"} {
							if baseline[key] == empty[key] {
								t.Fatalf("query reused %s", key)
							}
							delete(baseline, key)
							delete(empty, key)
						}
						for _, key := range []string{"evaluated_at_usec", "latency_micros"} {
							for _, report := range []map[string]any{baseline, empty} {
								if value, ok := report[key].(float64); !ok || value < 0 {
									t.Fatalf("invalid producing %s: %v", key, report[key])
								}
								delete(report, key)
							}
						}
						if !reflect.DeepEqual(baseline, empty) {
							t.Fatalf("empty flag changed stable facts/property set: absent=%v empty=%v", baseline, empty)
						}
					})
				}
			}
		})
	}
}

func TestNativeEmptyAssociationFileErrorsAndExclusions(t *testing.T) {
	t.Parallel()
	run, root := setup(t)
	malformed := writeDeclarationFile(t, root, `{"schema":"mousa.association_declarations.v1","associations":null}`)
	for _, mode := range []string{"omitted", "false", "true"} {
		for _, path := range []string{filepath.Join(root, "missing.json"), malformed} {
			target := testRun{t: t, binary: run.binary, store: filepath.Join(t.TempDir(), "absent.sqlite")}
			args := []string{"query", "--source", "notes", "--associations", path}
			if mode != "omitted" {
				args = append(args, "--supersession="+mode)
			}
			args = append(args, "needle")
			code, out, stderr := target.runExit(args...)
			if code != 1 || out != "" || stderr == "" {
				t.Fatalf("nonempty file error changed: %q exit=%d stdout=%q stderr=%q", args, code, out, stderr)
			}
			if _, err := os.Stat(target.store); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("file error crossed writable-open boundary: %v", err)
			}
		}
	}
	for _, mode := range [][]string{
		{"--supersession", "--context-tokens", "64"},
		{"--vector-space", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"--hybrid-space", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	} {
		target := testRun{t: t, binary: run.binary, store: filepath.Join(t.TempDir(), "absent.sqlite")}
		args := append([]string{"query", "--source", "notes", "--associations="}, mode...)
		args = append(args, "needle")
		code, out, stderr := target.runExit(args...)
		if code != 2 || out != "" {
			t.Fatalf("unsupported composition crossed parser: %q exit=%d stdout=%q stderr=%q", args, code, out, stderr)
		}
		if _, err := os.Stat(target.store); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsupported composition created store: %v", err)
		}
	}
}
