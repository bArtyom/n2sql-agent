package agentruntime

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrInvalidGraph     = errors.New("invalid agent graph")
	ErrUnknownGraphNode = errors.New("unknown agent graph node")
	ErrGraphStepLimit   = errors.New("agent graph step limit exceeded")
)

// AgentState is the small mutable state passed between graph nodes. CurrentNode
// is checkpoint-friendly: a Worker can restore it and continue at that node
// instead of replaying the preceding node.
type AgentState struct {
	CurrentNode string
	Values      map[string]any
}

type Node interface {
	Name() string
	Run(context.Context, *AgentState) (Transition, error)
}

type Transition struct {
	NextNode string
	Halt     bool
}

type Graph struct {
	start    string
	maxSteps int
	nodes    map[string]Node
}

func NewGraph(start string, maxSteps int, nodes ...Node) (*Graph, error) {
	if start == "" || maxSteps <= 0 || len(nodes) == 0 {
		return nil, ErrInvalidGraph
	}
	graph := &Graph{start: start, maxSteps: maxSteps, nodes: make(map[string]Node, len(nodes))}
	for _, node := range nodes {
		if node == nil || node.Name() == "" {
			return nil, ErrInvalidGraph
		}
		if _, exists := graph.nodes[node.Name()]; exists {
			return nil, fmt.Errorf("%w: duplicate node %q", ErrInvalidGraph, node.Name())
		}
		graph.nodes[node.Name()] = node
	}
	if _, exists := graph.nodes[start]; !exists {
		return nil, fmt.Errorf("%w: start node %q", ErrUnknownGraphNode, start)
	}
	return graph, nil
}

func (g *Graph) Run(ctx context.Context, state *AgentState) (*AgentState, error) {
	if g == nil || ctx == nil || state == nil {
		return nil, ErrInvalidGraph
	}
	if state.Values == nil {
		state.Values = make(map[string]any)
	}
	nodeName := state.CurrentNode
	if nodeName == "" {
		nodeName = g.start
	}
	for step := 0; step < g.maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return state, err
		}
		node, ok := g.nodes[nodeName]
		if !ok {
			return state, fmt.Errorf("%w: %s", ErrUnknownGraphNode, nodeName)
		}
		state.CurrentNode = nodeName
		transition, err := node.Run(ctx, state)
		if err != nil {
			return state, fmt.Errorf("run graph node %q: %w", nodeName, err)
		}
		if transition.Halt || transition.NextNode == "" {
			state.CurrentNode = ""
			return state, nil
		}
		nodeName = transition.NextNode
		state.CurrentNode = nodeName
	}
	return state, ErrGraphStepLimit
}
