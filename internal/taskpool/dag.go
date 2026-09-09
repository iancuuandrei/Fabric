package taskpool

import (
	"errors"
	"math/bits"
	"sort"

	"harness.local/engorch/internal/canonical"
)

// Task declares exact dependency IDs; human titles never resolve dependencies.
type Task struct {
	ID        string   `json:"id"`
	DependsOn []string `json:"depends_on,omitempty"`
}

// Graph is an immutable validated DAG with deterministic downstream priority.
// It adapts pi-subagent-tasks dag.ts; iterative topology and bitsets replace
// recursive traversals. Limits are 16,384 tasks and 524,288 edges.
type Graph struct {
	tasks      []Task
	index      map[string]int
	downstream []int
}

// NewGraph rejects missing, duplicate, self or cyclic dependencies before returning a graph.
func NewGraph(tasks []Task) (*Graph, error) {
	if len(tasks) == 0 || len(tasks) > 16384 {
		return nil, errors.New("invalid task count")
	}
	g := &Graph{tasks: make([]Task, len(tasks)), index: map[string]int{}, downstream: make([]int, len(tasks))}
	for i, task := range tasks {
		if !validName(task.ID) {
			return nil, errors.New("invalid task ID")
		}
		if _, ok := g.index[task.ID]; ok {
			return nil, errors.New("duplicate task ID")
		}
		g.index[task.ID] = i
		g.tasks[i] = Task{task.ID, append([]string(nil), task.DependsOn...)}
	}
	children := make([][]int, len(tasks))
	remaining := make([]int, len(tasks))
	edges := 0
	for i, task := range g.tasks {
		seen := map[string]bool{}
		for _, id := range task.DependsOn {
			dep, ok := g.index[id]
			if !ok || dep == i || seen[id] {
				return nil, errors.New("invalid dependency reference")
			}
			seen[id] = true
			edges++
			if edges > 524288 {
				return nil, errors.New("edge bound exceeded")
			}
			children[dep] = append(children[dep], i)
			remaining[i]++
		}
	}
	order := make([]int, 0, len(tasks))
	for i, count := range remaining {
		if count == 0 {
			order = append(order, i)
		}
	}
	for cursor := 0; cursor < len(order); cursor++ {
		for _, child := range children[order[cursor]] {
			remaining[child]--
			if remaining[child] == 0 {
				order = append(order, child)
			}
		}
	}
	if len(order) != len(tasks) {
		return nil, errors.New("dependency cycle")
	}
	words := (len(tasks) + 63) / 64
	reach := make([]uint64, len(tasks)*words)
	for cursor := len(order) - 1; cursor >= 0; cursor-- {
		node := order[cursor]
		dest := reach[node*words : (node+1)*words]
		for _, child := range children[node] {
			dest[child/64] |= uint64(1) << uint(child%64)
			source := reach[child*words : (child+1)*words]
			for i, value := range source {
				dest[i] |= value
			}
		}
		for _, value := range dest {
			g.downstream[node] += bits.OnesCount64(value)
		}
	}
	return g, nil
}

// ID binds exact task declarations and dependency order, not mutable task status.
func (g *Graph) ID() (string, error) {
	if g == nil {
		return "", errors.New("graph required")
	}
	return canonical.Hash("harness.taskpool-dag.v1", g.tasks)
}

// Ready returns pending IDs whose dependencies succeeded, prioritizing unique
// downstream dependents then declaration order. Other states never count as success.
// Missing state means pending; unknown IDs or state values reject the whole input.
func (g *Graph) Ready(states map[string]string) ([]string, error) {
	if g == nil {
		return nil, errors.New("graph required")
	}
	for id, state := range states {
		if _, ok := g.index[id]; !ok {
			return nil, errors.New("state references unknown task")
		}
		switch state {
		case "pending", "running", "succeeded", "failed", "cancelled", "unknown":
		default:
			return nil, errors.New("invalid task state")
		}
	}
	ready := []int{}
	for i, task := range g.tasks {
		if state := states[task.ID]; state != "" && state != "pending" {
			continue
		}
		ok := true
		for _, dep := range task.DependsOn {
			if states[dep] != "succeeded" {
				ok = false
				break
			}
		}
		if ok {
			ready = append(ready, i)
		}
	}
	sort.SliceStable(ready, func(i, j int) bool { return g.downstream[ready[i]] > g.downstream[ready[j]] })
	result := make([]string, len(ready))
	for i, index := range ready {
		result[i] = g.tasks[index].ID
	}
	return result, nil
}
