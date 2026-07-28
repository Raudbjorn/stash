package task

import "container/heap"

// queueEntry is one heap element. seq is a monotonic counter that makes the
// ordering stable: tasks of equal priority run in submission order.
type queueEntry struct {
	priority Priority
	seq      uint64
	taskID   string
}

// entryHeap implements heap.Interface over queueEntry.
type entryHeap []queueEntry

func (h entryHeap) Len() int { return len(h) }

func (h entryHeap) Less(i, j int) bool {
	if h[i].priority != h[j].priority {
		return h[i].priority < h[j].priority
	}
	return h[i].seq < h[j].seq
}

func (h entryHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *entryHeap) Push(x any) { *h = append(*h, x.(queueEntry)) }

func (h *entryHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// priorityQueue is a per-service queue ordered by (priority, submission order).
//
// Not safe for concurrent use on its own; the manager's mutex guards it.
type priorityQueue struct {
	h   entryHeap
	seq uint64
}

func newPriorityQueue() *priorityQueue {
	return &priorityQueue{h: entryHeap{}}
}

// Push enqueues a task.
func (q *priorityQueue) Push(priority Priority, taskID string) {
	heap.Push(&q.h, queueEntry{priority: priority, seq: q.seq, taskID: taskID})
	q.seq++
}

// Pop removes and returns the next task id, or "" when empty.
func (q *priorityQueue) Pop() string {
	if q.h.Len() == 0 {
		return ""
	}
	return heap.Pop(&q.h).(queueEntry).taskID
}

// Peek returns the next task id without removing it.
func (q *priorityQueue) Peek() string {
	if q.h.Len() == 0 {
		return ""
	}
	return q.h[0].taskID
}

// Remove drops a task from the queue, used when a queued task is cancelled.
func (q *priorityQueue) Remove(taskID string) {
	for i := range q.h {
		if q.h[i].taskID == taskID {
			heap.Remove(&q.h, i)
			return
		}
	}
}

// Len reports the queue depth.
func (q *priorityQueue) Len() int { return q.h.Len() }
