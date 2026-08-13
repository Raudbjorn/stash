package manager

import (
	"context"
	"testing"

	"github.com/stashapp/stash/pkg/job"
)

type testGenerateTask struct {
	description string
	start       func()
}

func (t testGenerateTask) GetDescription() string { return t.description }
func (t testGenerateTask) Start(context.Context)  { t.start() }

// A failed item must not cancel sibling generation work or strand the worker
// wait group. GenerateJob.Execute isolates panics at the same boundary used by
// every concrete generation task.
func TestGenerateJobContinuesAfterTaskPanic(t *testing.T) {
	completed := make(chan struct{}, 1)
	tasks := []Task{
		testGenerateTask{description: "broken", start: func() { panic("broken item") }},
		testGenerateTask{description: "healthy", start: func() { completed <- struct{}{} }},
	}
	progress := &job.Progress{}
	for _, current := range tasks {
		executeGenerateTask(context.Background(), progress, current)
	}
	select {
	case <-completed:
	default:
		t.Fatal("healthy generation task did not run after sibling panic")
	}
}
