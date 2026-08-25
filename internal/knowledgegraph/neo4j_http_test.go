package knowledgegraph

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNeo4jHTTPStoreSearchDecodesGraphRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/tx/commit") {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		response := map[string]any{
			"results": []any{
				map[string]any{
					"data": []any{
						map[string]any{"row": []any{"张三", "员工", []any{}, []any{"10:2"}, "属于", "研发部", "部门", []any{}, []any{"10:2", "10:3"}}},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(response)
	}));
	defer server.Close()

	store, err := NewNeo4jHTTPStore(server.URL, "neo4j", "password", "neo4j", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Search(context.Background(), 7, []string{"张三"}, nil)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Nodes) != 2 || len(result.Relations) != 1 || len(result.Chunks) != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestNormalizeNeo4jEndpointSupportsBoltURI(t *testing.T) {
	endpoint := normalizeNeo4jEndpoint("bolt://neo4j:7687", "neo4j")
	if endpoint != "http://neo4j:7474/db/neo4j/tx/commit" {
		t.Fatalf("endpoint = %q", endpoint)
	}
}
