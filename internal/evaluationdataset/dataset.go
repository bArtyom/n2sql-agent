// Package evaluationdataset loads the five-table dataset format used by
// WeKnora evaluation: queries, corpus, answers, qrels and qas.
package evaluationdataset

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/parquet-go/parquet-go"
)

var (
	ErrInvalidDataset = errors.New("invalid evaluation dataset")
	ErrMissingQuery   = errors.New("qrel or qa references an unknown query")
	ErrMissingPassage = errors.New("qrel references an unknown passage")
	ErrMissingAnswer  = errors.New("qa references an unknown answer")
)

type TextRow struct {
	ID   int64  `parquet:"id"`
	Text string `parquet:"text"`
}

type QrelRow struct {
	QID int64 `parquet:"qid"`
	PID int64 `parquet:"pid"`
}

type QARow struct {
	QID int64 `parquet:"qid"`
	AID int64 `parquet:"aid"`
}

type Dataset struct {
	Queries []TextRow
	Corpus  []TextRow
	Answers []TextRow
	Qrels   []QrelRow
	QAs     []QARow
}

// Snapshot is the HTTP/persistence representation of a dataset. The five
// arrays keep WeKnora's table semantics; PassageChunkIDs bridges external
// corpus passage IDs to this application's stable chunk citation IDs.
type Snapshot struct {
	Queries         []TextRow         `json:"queries"`
	Corpus          []TextRow         `json:"corpus"`
	Answers         []TextRow         `json:"answers"`
	Qrels           []QrelRow         `json:"qrels"`
	QAs             []QARow           `json:"qas"`
	PassageChunkIDs map[string]string `json:"passage_chunk_ids"`
}

func (s Snapshot) Dataset() Dataset {
	return Dataset{Queries: s.Queries, Corpus: s.Corpus, Answers: s.Answers, Qrels: s.Qrels, QAs: s.QAs}
}

type QAPair struct {
	QID      int64
	Question string
	PIDs     []int64
	Passages []string
	AID      int64
	Answer   string
}

func LoadDir(dir string) (Dataset, error) {
	if strings.TrimSpace(dir) == "" {
		return Dataset{}, ErrInvalidDataset
	}
	queries, err := readParquet[TextRow](dir + "/queries.parquet")
	if err != nil {
		return Dataset{}, fmt.Errorf("load queries: %w", err)
	}
	corpus, err := readParquet[TextRow](dir + "/corpus.parquet")
	if err != nil {
		return Dataset{}, fmt.Errorf("load corpus: %w", err)
	}
	answers, err := readParquet[TextRow](dir + "/answers.parquet")
	if err != nil {
		return Dataset{}, fmt.Errorf("load answers: %w", err)
	}
	qrels, err := readParquet[QrelRow](dir + "/qrels.parquet")
	if err != nil {
		return Dataset{}, fmt.Errorf("load qrels: %w", err)
	}
	qas, err := readParquet[QARow](dir + "/qas.parquet")
	if err != nil {
		return Dataset{}, fmt.Errorf("load qas: %w", err)
	}
	dataset := Dataset{Queries: queries, Corpus: corpus, Answers: answers, Qrels: qrels, QAs: qas}
	if err := dataset.Validate(); err != nil {
		return Dataset{}, err
	}
	return dataset, nil
}

func (d Dataset) Validate() error {
	if len(d.Queries) == 0 || len(d.Corpus) == 0 || len(d.Answers) == 0 || len(d.QAs) == 0 {
		return ErrInvalidDataset
	}
	queries, err := indexTextRows(d.Queries)
	if err != nil {
		return err
	}
	corpus, err := indexTextRows(d.Corpus)
	if err != nil {
		return err
	}
	answers, err := indexTextRows(d.Answers)
	if err != nil {
		return err
	}
	for _, row := range d.Qrels {
		if _, ok := queries[row.QID]; !ok {
			return fmt.Errorf("%w: %d", ErrMissingQuery, row.QID)
		}
		if _, ok := corpus[row.PID]; !ok {
			return fmt.Errorf("%w: %d", ErrMissingPassage, row.PID)
		}
	}
	for _, row := range d.QAs {
		if _, ok := queries[row.QID]; !ok {
			return fmt.Errorf("%w: %d", ErrMissingQuery, row.QID)
		}
		if _, ok := answers[row.AID]; !ok {
			return fmt.Errorf("%w: %d", ErrMissingAnswer, row.AID)
		}
	}
	return nil
}

func (d Dataset) Pairs() ([]QAPair, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	queries, _ := indexTextRows(d.Queries)
	corpus, _ := indexTextRows(d.Corpus)
	answers, _ := indexTextRows(d.Answers)
	relevant := make(map[int64][]int64)
	for _, row := range d.Qrels {
		relevant[row.QID] = append(relevant[row.QID], row.PID)
	}
	answerIDs := make(map[int64]int64, len(d.QAs))
	for _, row := range d.QAs {
		answerIDs[row.QID] = row.AID
	}
	qids := make([]int64, 0, len(queries))
	for qid := range queries {
		qids = append(qids, qid)
	}
	sort.Slice(qids, func(i, j int) bool { return qids[i] < qids[j] })
	pairs := make([]QAPair, 0, len(qids))
	for _, qid := range qids {
		aid := answerIDs[qid]
		pids := append([]int64(nil), relevant[qid]...)
		passages := make([]string, 0, len(pids))
		for _, pid := range pids {
			passages = append(passages, corpus[pid])
		}
		pairs = append(pairs, QAPair{QID: qid, Question: queries[qid], PIDs: pids, Passages: passages, AID: aid, Answer: answers[aid]})
	}
	return pairs, nil
}

func indexTextRows(rows []TextRow) (map[int64]string, error) {
	result := make(map[int64]string, len(rows))
	for _, row := range rows {
		if row.ID <= 0 || strings.TrimSpace(row.Text) == "" {
			return nil, ErrInvalidDataset
		}
		if _, exists := result[row.ID]; exists {
			return nil, ErrInvalidDataset
		}
		result[row.ID] = row.Text
	}
	return result, nil
}

func readParquet[T any](path string) ([]T, error) { return parquet.ReadFile[T](path) }
