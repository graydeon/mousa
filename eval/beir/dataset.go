package beir

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Dataset is one loaded BEIR subset: the corpus, the queries, and the relevance
// judgments for one named split.
type Dataset struct {
	Name     string
	Corpus   map[string]string         // corpus id -> "title\n\ntext"
	QueryIDs []string                  // file order, deterministic
	Queries  map[string]string         // query id -> query text
	Qrels    map[string]map[string]int // query id -> corpus id -> relevance
}

type corpusLine struct {
	CorpusID string `json:"_id"`
	Title    string `json:"title"`
	Text     string `json:"text"`
}

type queryLine struct {
	QueryID string `json:"_id"`
	Text    string `json:"text"`
}

type qrelLine struct {
	QueryID  string `json:"query-id"`
	CorpusID string `json:"corpus-id"`
	Score    int    `json:"score"`
}

// LoadDataset reads BEIR-style corpus.jsonl, queries.jsonl, and qrels.tsv from a
// directory. Only queries that carry at least one relevant judgment are kept,
// because unjudged queries cannot contribute to the metrics.
func LoadDataset(name, dir string) (*Dataset, error) {
	dataset := &Dataset{
		Name:    name,
		Corpus:  map[string]string{},
		Queries: map[string]string{},
		Qrels:   map[string]map[string]int{},
	}
	if err := readJSONL(dir+"/corpus.jsonl", 0, func(raw []byte) error {
		var line corpusLine
		if err := json.Unmarshal(raw, &line); err != nil {
			return fmt.Errorf("corpus.jsonl: %w", err)
		}
		text := strings.TrimSpace(line.Title + "\n" + line.Text)
		dataset.Corpus[line.CorpusID] = text
		return nil
	}); err != nil {
		return nil, err
	}
	if err := readJSONL(dir+"/queries.jsonl", 0, func(raw []byte) error {
		var line queryLine
		if err := json.Unmarshal(raw, &line); err != nil {
			return fmt.Errorf("queries.jsonl: %w", err)
		}
		dataset.QueryIDs = append(dataset.QueryIDs, line.QueryID)
		dataset.Queries[line.QueryID] = line.Text
		return nil
	}); err != nil {
		return nil, err
	}
	if err := readQrels(dir+"/qrels/test.tsv", dataset); err != nil {
		return nil, err
	}
	return dataset, nil
}

func readJSONL(path string, limit int, handle func([]byte) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	count := 0
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		if err := handle(raw); err != nil {
			return err
		}
		count++
		if limit > 0 && count >= limit {
			break
		}
	}
	return scanner.Err()
}

func readQrels(path string, dataset *Dataset) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	first := true
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if first {
			first = false
			if strings.HasPrefix(strings.ToLower(line), "query-id") {
				continue
			}
		}
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) == 4 {
			// TREC-style layout: query-id iteration corpus-id score.
			fields = []string{fields[0], fields[2], fields[3]}
		}
		if len(fields) != 3 {
			continue
		}
		var score int
		if _, err := fmt.Sscanf(fields[2], "%d", &score); err != nil {
			return fmt.Errorf("qrels score %q: %w", fields[2], err)
		}
		queryID, corpusID := fields[0], fields[1]
		if _, ok := dataset.Queries[queryID]; !ok {
			continue
		}
		if _, ok := dataset.Corpus[corpusID]; !ok {
			continue
		}
		if dataset.Qrels[queryID] == nil {
			dataset.Qrels[queryID] = map[string]int{}
		}
		dataset.Qrels[queryID][corpusID] = score
	}
	return scanner.Err()
}

// JudgedQueries returns the query IDs that carry at least one relevant judgment,
// in file order.
func (dataset *Dataset) JudgedQueries() []string {
	judged := make([]string, 0, len(dataset.Qrels))
	for _, queryID := range dataset.QueryIDs {
		if len(dataset.Qrels[queryID]) > 0 {
			judged = append(judged, queryID)
		}
	}
	return judged
}

// LimitCorpus keeps only the first limit corpus entries in corpus-file order and
// drops judgments for documents outside that subset. It returns the dropped and
// kept judged-query counts so the harness can report the subset protocol.
func (dataset *Dataset) LimitCorpus(limit int) (total int, keptQueries int) {
	if limit <= 0 || len(dataset.Corpus) <= limit {
		return len(dataset.Corpus), len(dataset.Qrels)
	}
	keep := make(map[string]struct{}, limit)
	count := 0
	// Corpus map iteration order is random; sort IDs for a reproducible subset.
	ids := make([]string, 0, len(dataset.Corpus))
	for id := range dataset.Corpus {
		ids = append(ids, id)
	}
	sortStrings(ids)
	for _, id := range ids {
		if count >= limit {
			break
		}
		keep[id] = struct{}{}
		count++
	}
	for id, text := range dataset.Corpus {
		if _, ok := keep[id]; !ok {
			delete(dataset.Corpus, id)
		}
		_ = text
	}
	for queryID, judgments := range dataset.Qrels {
		for corpusID := range judgments {
			if _, ok := keep[corpusID]; !ok {
				delete(judgments, corpusID)
			}
		}
		if len(judgments) == 0 {
			delete(dataset.Qrels, queryID)
		}
	}
	return total, len(dataset.Qrels)
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
