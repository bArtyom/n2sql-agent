package knowledgegraph

import "testing"

func TestParseGraphResponseNormalizesAndDropsUngroundedRelations(t *testing.T) {
	graph, err := ParseGraphResponse("```json\n{\"nodes\":[{\"name\":\"张三\",\"type\":\"员工\"},{\"name\":\"研发部\",\"type\":\"部门\"}],\"relations\":[{\"source\":\"张三\",\"target\":\"研发部\",\"type\":\"属于\"},{\"source\":\"张三\",\"target\":\"不存在\",\"type\":\"属于\"}]}\n```")
	if err != nil {
		t.Fatalf("ParseGraphResponse() error = %v", err)
	}
	if len(graph.Entities) != 2 || len(graph.Relations) != 1 {
		t.Fatalf("graph = %#v, want two entities and one grounded relation", graph)
	}
	if graph.Relations[0].Source != "张三" || graph.Relations[0].Target != "研发部" {
		t.Fatalf("relation = %#v", graph.Relations[0])
	}
}

func TestParseGraphResponseRejectsRelationWithoutEntities(t *testing.T) {
	if _, err := ParseGraphResponse(`{"relations":[{"source":"A","target":"B","type":"depends_on"}]}`); err == nil {
		t.Fatal("expected ungrounded graph to be rejected")
	}
}
