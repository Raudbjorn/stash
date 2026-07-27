package task

import "sync"

// EventType names a task lifecycle transition. These become websocket message
// types as "task.<event>", which is the contract the frontend switches on.
type EventType string

const (
	EventQueued    EventType = "queued"
	EventStarted   EventType = "started"
	EventProgress  EventType = "progress"
	EventCompleted EventType = "completed"
	EventFailed    EventType = "failed"
	EventCancelled EventType = "cancelled"
)

// Event is a task state change.
type Event struct {
	Type EventType
	// Task is a snapshot taken while the manager's lock was held, so
	// subscribers never observe a torn record.
	Task Record
	// Extra carries progress payloads. The websocket layer drops it - the
	// original only ever forwarded the task summary - but it is kept here so
	// in-process consumers can use it.
	Extra map[string]any
}

// Subscription is a stream of task events.
//
// The channel is closed on Close, so consumers can range over it. Closing and
// publishing are serialised through the bus mutex: without that, a publish
// racing a Close would send on a closed channel and panic.
type Subscription struct {
	// Events receives task events. It is buffered; see publish for what happens
	// when a slow consumer fills it.
	Events <-chan Event

	events chan Event
	closed bool
	unsub  func()
}

// Close ends the subscription. Safe to call more than once.
func (s *Subscription) Close() { s.unsub() }

// eventBus fans events out to subscribers.
//
// Publishing never blocks: a subscriber whose buffer is full misses the event
// rather than stalling the scheduler. This mirrors pkg/job/subscribe.go, and
// the tradeoff is deliberate - a wedged websocket client must not be able to
// halt AI task processing.
type eventBus struct {
	mu          sync.Mutex
	subscribers map[*Subscription]struct{}
}

func newEventBus() *eventBus {
	return &eventBus{subscribers: make(map[*Subscription]struct{})}
}

// subscribe registers a new subscriber with the given buffer depth.
func (b *eventBus) subscribe(buffer int) *Subscription {
	if buffer <= 0 {
		buffer = 64
	}

	ch := make(chan Event, buffer)
	sub := &Subscription{Events: ch, events: ch}
	sub.unsub = func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if sub.closed {
			return
		}
		sub.closed = true
		delete(b.subscribers, sub)
		close(sub.events)
	}

	b.mu.Lock()
	b.subscribers[sub] = struct{}{}
	b.mu.Unlock()

	return sub
}

// publish delivers an event to every subscriber, dropping it for any whose
// buffer is full.
//
// The sends happen while the bus mutex is held. That is safe because each send
// is non-blocking, and it is necessary: it is what makes closing a subscription
// concurrently with a publish impossible rather than merely unlikely.
func (b *eventBus) publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for s := range b.subscribers {
		select {
		case s.events <- ev:
		default:
			// Slow consumer: drop rather than block the scheduler.
		}
	}
}

// count reports the number of active subscribers, for diagnostics.
func (b *eventBus) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}
