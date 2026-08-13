package job

import (
	"context"
	"testing"
)

func TestTaskQueueContinuesAfterTaskPanic(t *testing.T) {
	progress := &Progress{}
	queue := NewTaskQueue(context.Background(), progress, 2, 1)
	completed := make(chan struct{}, 1)
	queue.Add("broken", func(context.Context) { panic("broken item") })
	queue.Add("healthy", func(context.Context) { completed <- struct{}{} })
	queue.Close()

	select {
	case <-completed:
	default:
		t.Fatal("healthy task did not run after sibling panic")
	}
}
