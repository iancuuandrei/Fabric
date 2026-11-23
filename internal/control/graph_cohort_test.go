package control

import (
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"testing"
)

func TestGraphDependencyWavesHaveDistinctFrozenSchedules(t *testing.T) {
	a := taskscheduler.TaskSpec{ID: "research", InvocationID: "first"}
	b := taskscheduler.TaskSpec{ID: "design", InvocationID: "second"}
	first, err := graphCohortID("graph", 1, []taskscheduler.TaskSpec{a})
	if err != nil {
		t.Fatal(err)
	}
	second, err := graphCohortID("graph", 1, []taskscheduler.TaskSpec{b})
	if err != nil || first == second {
		t.Fatal("dependency waves reuse a schedule", err)
	}
	again, err := graphCohortID("graph", 1, []taskscheduler.TaskSpec{a})
	if err != nil || first != again {
		t.Fatal("same cohort changed on restart", err)
	}
	combined, _ := graphCohortID("graph", 1, []taskscheduler.TaskSpec{a, b})
	reordered, _ := graphCohortID("graph", 1, []taskscheduler.TaskSpec{b, a})
	if combined != reordered {
		t.Fatal("task ordering changed cohort identity")
	}
	a.InvocationID = "different"
	changed, _ := graphCohortID("graph", 1, []taskscheduler.TaskSpec{a})
	if changed == first {
		t.Fatal("invocation substitution reused a schedule")
	}
}
