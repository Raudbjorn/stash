package python

import (
	"context"
	"sync"
)

type leaseRequest struct {
	write   bool
	granted chan struct{}
}

type leaseGate struct {
	mu      sync.Mutex
	readers int
	writer  bool
	queue   []*leaseRequest
}

var environmentGate leaseGate

func AcquireExecution(ctx context.Context) (func(), error) {
	return environmentGate.acquire(ctx, false)
}

func AcquireMutation(ctx context.Context) (func(), error) {
	return environmentGate.acquire(ctx, true)
}

func (g *leaseGate) acquire(ctx context.Context, write bool) (func(), error) {
	req := &leaseRequest{write: write, granted: make(chan struct{})}
	g.mu.Lock()
	g.queue = append(g.queue, req)
	g.grantLocked()
	g.mu.Unlock()

	select {
	case <-req.granted:
		var once sync.Once
		return func() {
			once.Do(func() {
				g.mu.Lock()
				if write {
					g.writer = false
				} else {
					g.readers--
				}
				g.grantLocked()
				g.mu.Unlock()
			})
		}, nil
	case <-ctx.Done():
		g.mu.Lock()
		for n, queued := range g.queue {
			if queued == req {
				g.queue = append(g.queue[:n], g.queue[n+1:]...)
				g.grantLocked()
				g.mu.Unlock()
				return nil, ctx.Err()
			}
		}
		g.mu.Unlock()

		// The grant won the race with cancellation. Return a lease so callers can
		// release the state consistently, then report cancellation after release.
		<-req.granted
		g.mu.Lock()
		if write {
			g.writer = false
		} else {
			g.readers--
		}
		g.grantLocked()
		g.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (g *leaseGate) grantLocked() {
	if g.writer || len(g.queue) == 0 {
		return
	}
	if g.queue[0].write {
		if g.readers != 0 {
			return
		}
		req := g.queue[0]
		g.queue = g.queue[1:]
		g.writer = true
		close(req.granted)
		return
	}

	for len(g.queue) > 0 && !g.queue[0].write {
		req := g.queue[0]
		g.queue = g.queue[1:]
		g.readers++
		close(req.granted)
	}
}
