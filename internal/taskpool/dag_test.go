package taskpool

import (
	"fmt"
	"reflect"
	"testing"
)

func TestDAGPriorityFailureAndMutationIsolation(t *testing.T) {
	tasks := []Task{{ID: "isolated"}, {ID: "a"}, {"b", []string{"a"}}, {"c", []string{"a"}}, {"d", []string{"b", "c"}}}
	g, err := NewGraph(tasks)
	if err != nil {
		t.Fatal(err)
	}
	id, err := g.ID()
	if err != nil {
		t.Fatal(err)
	}
	tasks[2].DependsOn[0] = "missing"
	after, err := g.ID()
	if err != nil || after != id {
		t.Fatal("caller changed graph", err)
	}
	ready, err := g.Ready(nil)
	if err != nil || !reflect.DeepEqual(ready, []string{"a", "isolated"}) {
		t.Fatal(ready, err)
	}
	ready, err = g.Ready(map[string]string{"a": "succeeded", "b": "failed", "c": "succeeded", "isolated": "running"})
	if err != nil || len(ready) != 0 {
		t.Fatal("failed dependency unblocked task", ready, err)
	}
}
func TestDAGRejectsInvalidReferences(t *testing.T) {
	for _, tasks := range [][]Task{{{ID: "a"}, {ID: "a"}}, {{"a", []string{"a"}}}, {{"a", []string{"b"}}}, {{ID: "a"}, {"b", []string{"a", "a"}}}, {{"a", []string{"b"}}, {"b", []string{"a"}}}} {
		if _, err := NewGraph(tasks); err == nil {
			t.Fatal("invalid graph accepted", tasks)
		}
	}
}

func TestDAGFanOutFanInRetainsUnknownDependency(t *testing.T) {
	tasks := []Task{{ID: "root"}}
	join := Task{ID: "join"}
	states := map[string]string{"root": "succeeded"}
	for i := 0; i < 128; i++ {
		id := fmt.Sprintf("child-%03d", i)
		tasks = append(tasks, Task{ID: id, DependsOn: []string{"root"}})
		join.DependsOn = append(join.DependsOn, id)
		states[id] = "succeeded"
	}
	tasks = append(tasks, join)
	g, err := NewGraph(tasks)
	if err != nil {
		t.Fatal(err)
	}
	if g.downstream[0] != 129 {
		t.Fatalf("shared join counted repeatedly: %d", g.downstream[0])
	}
	states["child-127"] = "unknown"
	ready, err := g.Ready(states)
	if err != nil || len(ready) != 0 {
		t.Fatal("unknown dependency admitted join", ready, err)
	}
	states["child-127"] = "succeeded"
	ready, err = g.Ready(states)
	if err != nil || !reflect.DeepEqual(ready, []string{"join"}) {
		t.Fatal("settled fan-in did not admit join", ready, err)
	}
}
func BenchmarkDAGChain(b *testing.B) {
	for _, count := range []int{32, 1024, 16384} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			tasks := make([]Task, count)
			for i := range tasks {
				tasks[i].ID = fmt.Sprint(i)
				if i > 0 {
					tasks[i].DependsOn = []string{fmt.Sprint(i - 1)}
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				g, err := NewGraph(tasks)
				if err != nil || g.downstream[0] != count-1 {
					b.Fatal("incorrect graph", err)
				}
			}
		})
	}
}
