package knowledgebase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

const testDriverName = "knowledgebase-store-test-driver"

var (
	testDatabasesMu sync.Mutex
	testDatabases   = map[string]*testDatabase{}
)

type testDatabase struct {
	query      string
	queryArgs  []driver.NamedValue
	queryRows  [][]driver.Value
	queryError error
	exec       string
	execArgs   []driver.NamedValue
	execResult driver.Result
	execError  error
}

type testDriver struct{}

func (testDriver) Open(name string) (driver.Conn, error) {
	testDatabasesMu.Lock()
	database := testDatabases[name]
	testDatabasesMu.Unlock()
	if database == nil {
		return nil, errors.New("test database not found")
	}
	return testConn{database: database}, nil
}

type testConn struct{ database *testDatabase }

func (testConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported")
}
func (testConn) Close() error              { return nil }
func (testConn) Begin() (driver.Tx, error) { return nil, errors.New("transactions are not supported") }

func (c testConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.database.query = query
	c.database.queryArgs = args
	if c.database.queryError != nil {
		return nil, c.database.queryError
	}
	return &testRows{rows: c.database.queryRows}, nil
}

func (c testConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.database.exec = query
	c.database.execArgs = args
	if c.database.execError != nil {
		return nil, c.database.execError
	}
	return c.database.execResult, nil
}

type testRows struct {
	rows  [][]driver.Value
	index int
}

func (testRows) Columns() []string { return []string{"id", "name", "description"} }
func (testRows) Close() error      { return nil }
func (r *testRows) Next(destination []driver.Value) error {
	if r.index == len(r.rows) {
		return io.EOF
	}
	copy(destination, r.rows[r.index])
	r.index++
	return nil
}

func init() { sql.Register(testDriverName, testDriver{}) }

func newTestStore(t *testing.T, database *testDatabase) *PostgresStore {
	t.Helper()
	name := t.Name()
	testDatabasesMu.Lock()
	testDatabases[name] = database
	testDatabasesMu.Unlock()
	t.Cleanup(func() {
		testDatabasesMu.Lock()
		delete(testDatabases, name)
		testDatabasesMu.Unlock()
	})
	db, err := sql.Open(testDriverName, name)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewPostgresStore(db)
}

func TestPostgresStoreCreatesKnowledgeBaseForLocalAdministrator(t *testing.T) {
	database := &testDatabase{queryRows: [][]driver.Value{{int64(7), "Go 学习资料", "后端笔记"}}}
	store := newTestStore(t, database)

	knowledgeBase, err := store.Create(context.Background(), CreateInput{Name: "Go 学习资料", Description: "后端笔记"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if knowledgeBase.ID != 7 || knowledgeBase.Name != "Go 学习资料" {
		t.Fatalf("knowledge base = %#v", knowledgeBase)
	}
	if !strings.Contains(database.query, "SELECT administrator_id") {
		t.Fatalf("query must use the local administrator: %s", database.query)
	}
	if len(database.queryArgs) != 2 || database.queryArgs[0].Value != "Go 学习资料" {
		t.Fatalf("query arguments = %#v", database.queryArgs)
	}
}

func TestPostgresStoreListsKnowledgeBases(t *testing.T) {
	database := &testDatabase{queryRows: [][]driver.Value{{int64(1), "Go 学习资料", ""}, {int64(2), "数据库笔记", "PostgreSQL"}}}
	store := newTestStore(t, database)

	knowledgeBases, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(knowledgeBases) != 2 || knowledgeBases[1].Name != "数据库笔记" {
		t.Fatalf("knowledge bases = %#v", knowledgeBases)
	}
	if !strings.Contains(database.query, "WHERE administrator_id") {
		t.Fatalf("query must scope results to the local administrator: %s", database.query)
	}
}

func TestPostgresStoreReportsMissingKnowledgeBaseOnDelete(t *testing.T) {
	database := &testDatabase{execResult: driver.RowsAffected(0)}
	store := newTestStore(t, database)

	err := store.Delete(context.Background(), 9)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete() error = %v, want ErrNotFound", err)
	}
	if !strings.Contains(database.exec, "administrator_id") {
		t.Fatalf("delete must scope results to the local administrator: %s", database.exec)
	}
	if len(database.execArgs) != 1 || database.execArgs[0].Value != int64(9) {
		t.Fatalf("exec arguments = %#v", database.execArgs)
	}
}
