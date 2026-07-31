package httpapi

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stashapp/stash/internal/aiserver/task"
	"github.com/stashapp/stash/pkg/logger"
)

// The task websocket pushes lifecycle events to the dashboard. The message
// shape is {"type": "task.<event>", "task": <summary>} and the frontend
// switches on that type string.
//
// It runs through Stash's normal middleware chain: chi's compressor and the
// request logger both implement http.Hijacker, and Stash already serves its
// GraphQL subscriptions over the same chain, so no bypass is needed.

const (
	// writeWait bounds a single write to a client.
	writeWait = 10 * time.Second
	// pongWait is how long to wait for a pong before assuming the peer is gone.
	pongWait = 60 * time.Second
	// pingPeriod must be comfortably shorter than pongWait.
	pingPeriod = 25 * time.Second
	// sendBuffer is how far a slow client may lag before being dropped.
	sendBuffer = 64
)

var upgrader = websocket.Upgrader{
	// Same policy as Stash's GraphQL websocket transport: this endpoint is
	// behind session auth, so origin is not the control.
	CheckOrigin: func(*http.Request) bool { return true },
}

type wsClient struct {
	conn *websocket.Conn
	send chan []byte
	once sync.Once
}

// close shuts a client down exactly once.
func (c *wsClient) close() {
	c.once.Do(func() {
		close(c.send)
		_ = c.conn.Close()
	})
}

// wsHub fans task events out to connected websocket clients.
type wsHub struct {
	backend Backend

	mu      sync.Mutex
	clients map[*wsClient]struct{}

	stopOnce sync.Once
	quit     chan struct{}
	started  bool
}

func newWSHub(backend Backend) *wsHub {
	return &wsHub{
		backend: backend,
		clients: make(map[*wsClient]struct{}),
		quit:    make(chan struct{}),
	}
}

// start begins forwarding scheduler events. Idempotent.
func (h *wsHub) start() {
	h.mu.Lock()
	if h.started {
		h.mu.Unlock()
		return
	}
	h.started = true
	h.mu.Unlock()

	go h.forward()
}

// stop closes the hub and disconnects every client.
func (h *wsHub) stop() {
	h.stopOnce.Do(func() {
		close(h.quit)

		h.mu.Lock()
		clients := make([]*wsClient, 0, len(h.clients))
		for c := range h.clients {
			clients = append(clients, c)
		}
		h.clients = make(map[*wsClient]struct{})
		h.mu.Unlock()

		for _, c := range clients {
			c.close()
		}
	})
}

// forward subscribes to the scheduler and broadcasts each event.
//
// The subscription is established lazily because the scheduler may not exist
// when the hub starts (the AI server can be disabled at boot and enabled
// later).
func (h *wsHub) forward() {
	var sub *task.Subscription
	defer func() {
		if sub != nil {
			sub.Close()
		}
	}()

	retry := time.NewTicker(time.Second)
	defer retry.Stop()

	for {
		if sub == nil {
			if tasks := h.backend.Tasks(); tasks != nil {
				sub = tasks.Subscribe(256)
			} else {
				select {
				case <-h.quit:
					return
				case <-retry.C:
					continue
				}
			}
		}

		select {
		case <-h.quit:
			return
		case ev, ok := <-sub.Events:
			if !ok {
				sub = nil
				continue
			}
			h.broadcast(ev)
		}
	}
}

// broadcast sends one event to every client.
func (h *wsHub) broadcast(ev task.Event) {
	// The progress payload is deliberately dropped: the original forwarded only
	// the task summary, and the dashboard derives progress from the task cache
	// rather than from these events. Forwarding it would flood the socket.
	payload := map[string]any{
		"type": "task." + string(ev.Type),
		"task": ev.Task.Summary(),
	}
	h.send(payload)
}

func (h *wsHub) send(payload any) {
	raw, err := encodeJSON(payload)
	if err != nil {
		logger.Errorf("encoding AI task event: %v", err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for c := range h.clients {
		select {
		case c.send <- raw:
		default:
			// A client that cannot keep up is dropped rather than allowed to
			// stall the hub.
			delete(h.clients, c)
			go c.close()
		}
	}
}

// handleWS upgrades a request and streams task events.
func (h *wsHub) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written a response.
		logger.Debugf("AI task websocket upgrade failed: %v", err)
		return
	}

	client := &wsClient{conn: conn, send: make(chan []byte, sendBuffer)}

	h.mu.Lock()
	select {
	case <-h.quit:
		h.mu.Unlock()
		client.close()
		return
	default:
	}
	h.clients[client] = struct{}{}
	h.mu.Unlock()

	go h.writePump(client)
	go h.readPump(client)

	// Send the current task list so a reconnecting dashboard is immediately
	// populated rather than waiting for the next transition.
	if tasks := h.backend.Tasks(); tasks != nil {
		for _, rec := range tasks.List(task.ListFilter{}) {
			raw, err := encodeJSON(map[string]any{
				"type": "task.snapshot",
				"task": rec.Summary(),
			})
			if err != nil {
				continue
			}
			select {
			case client.send <- raw:
			default:
			}
		}
	}
}

// writePump owns all writes to a connection: gorilla/websocket forbids
// concurrent writers, so every message goes through this one goroutine.
func (h *wsHub) writePump(c *wsClient) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		h.removeClient(c)
	}()

	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// readPump discards client messages and keeps the liveness deadline fresh. The
// dashboard never sends anything meaningful; reading is how a dropped
// connection is noticed.
func (h *wsHub) readPump(c *wsClient) {
	defer h.removeClient(c)

	c.conn.SetReadLimit(4096)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (h *wsHub) removeClient(c *wsClient) {
	h.mu.Lock()
	_, present := h.clients[c]
	delete(h.clients, c)
	h.mu.Unlock()

	if present {
		c.close()
	}
}

// clientCount reports connected clients, for tests and diagnostics.
func (h *wsHub) clientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}
