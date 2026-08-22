package evaluationdataset_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/evaluationdataset"
	"github.com/parquet-go/parquet-go"
)

func TestPairsFollowWeKnoraRelationsAndStableQIDOrder(t *testing.T) {
	dataset := evaluationdataset.Dataset{
		Queries: []evaluationdataset.TextRow{{ID: 2, Text: "第二问"}, {ID: 1, Text: "第一问"}},
		Corpus:  []evaluationdataset.TextRow{{ID: 10, Text: "段落十"}, {ID: 11, Text: "段落十一"}},
		Answers: []evaluationdataset.TextRow{{ID: 20, Text: "答案二"}, {ID: 21, Text: "答案一"}},
		Qrels:   []evaluationdataset.QrelRow{{QID: 2, PID: 11}, {QID: 1, PID: 10}},
		QAs:     []evaluationdataset.QARow{{QID: 2, AID: 20}, {QID: 1, AID: 21}},
	}
	pairs, err := dataset.Pairs()
	if err != nil {
		t.Fatalf("Pairs() error = %v", err)
	}
	if len(pairs) != 2 || pairs[0].QID != 1 || pairs[0].PIDs[0] != 10 || pairs[1].Answer != "答案二" {
		t.Fatalf("unexpected pairs: %#v", pairs)
	}
}

func TestLoadDirReadsWeKnoraParquetTables(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, rows any) {
		t.Helper()
		var err error
		switch typed := rows.(type) {
		case []evaluationdataset.TextRow:
			err = parquet.WriteFile(filepath.Join(dir, name), typed)
		case []evaluationdataset.QrelRow:
			err = parquet.WriteFile(filepath.Join(dir, name), typed)
		case []evaluationdataset.QARow:
			err = parquet.WriteFile(filepath.Join(dir, name), typed)
		default:
			t.Fatalf("unsupported rows type %T", rows)
		}
		if err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}
	write("queries.parquet", []evaluationdataset.TextRow{{ID: 1, Text: "问题"}})
	write("corpus.parquet", []evaluationdataset.TextRow{{ID: 10, Text: "段落"}})
	write("answers.parquet", []evaluationdataset.TextRow{{ID: 20, Text: "答案"}})
	write("qrels.parquet", []evaluationdataset.QrelRow{{QID: 1, PID: 10}})
	write("qas.parquet", []evaluationdataset.QARow{{QID: 1, AID: 20}})

	dataset, err := evaluationdataset.LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir() error = %v", err)
	}
	pairs, err := dataset.Pairs()
	if err != nil || len(pairs) != 1 || pairs[0].Answer != "答案" {
		t.Fatalf("unexpected loaded pairs: %#v, error=%v", pairs, err)
	}
}

func TestValidateRejectsDanglingRelations(t *testing.T) {
	dataset := evaluationdataset.Dataset{
		Queries: []evaluationdataset.TextRow{{ID: 1, Text: "问题"}},
		Corpus:  []evaluationdataset.TextRow{{ID: 10, Text: "段落"}},
		Answers: []evaluationdataset.TextRow{{ID: 20, Text: "答案"}},
		Qrels:   []evaluationdataset.QrelRow{{QID: 1, PID: 99}},
		QAs:     []evaluationdataset.QARow{{QID: 1, AID: 20}},
	}
	if err := dataset.Validate(); !errors.Is(err, evaluationdataset.ErrMissingPassage) {
		t.Fatalf("Validate() error = %v, want missing passage", err)
	}
}
