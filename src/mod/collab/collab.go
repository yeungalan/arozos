/*
	Collaborative Document Hub

	A small, transport-agnostic room based pub/sub engine used by the
	NotionEditor webapp to provide real-time multi-user editing and presence.

	Design goals
	────────────
	  - The hub itself knows nothing about HTTP or WebSockets so it can be unit
	    tested in isolation. The main package wires gorilla/websocket read/write
	    loops on top of the Client.Outbound() channel and the Room methods here.
	  - A Room is keyed by an opaque document id (the NotionEditor uses a hash of
	    the markdown file path so two users opening the same file share a room).
	  - The hub stores one opaque latest snapshot per room so that a client which
	    joins a "warm" room receives the current document state immediately, while
	    low latency edits are relayed as operations between members.
	  - The oldest member of a room is elected "primary"; the front-end uses that
	    flag to decide which single client is responsible for persisting the
	    document back to disk (avoids every collaborator writing the same file).

	author: tobychui
*/

package collab

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

// memberColors is the palette assigned, round-robin, to joining clients so each
// collaborator gets a distinct presence colour. Values are plain hex strings so
// the front-end can use them directly in CSS.
var memberColors = []string{
	"#e03131", "#1971c2", "#2f9e44", "#f08c00",
	"#9c36b5", "#0c8599", "#e8590c", "#5f3dc4",
}

// Member is the public, broadcastable description of a participant in a room.
type Member struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
	Cursor  string `json:"cursor"`  // block id the participant's caret is in ("" if unknown)
	Primary bool   `json:"primary"` // true for the single client responsible for persistence
}

// Client is a single connected participant. The transport layer owns the read
// loop and ranges over Outbound() to write frames to the socket.
type Client struct {
	ID     string
	Name   string
	Color  string
	cursor string
	joined time.Time
	send   chan []byte
	once   sync.Once
	closed bool
	mu     sync.Mutex
}

// NewClient builds a client with a buffered outbound queue. sendBuffer caps the
// number of frames that may be queued before slow-consumer frames are dropped.
func NewClient(id, name, color string, sendBuffer int) *Client {
	if sendBuffer <= 0 {
		sendBuffer = 64
	}
	return &Client{
		ID:     id,
		Name:   name,
		Color:  color,
		joined: time.Now(),
		send:   make(chan []byte, sendBuffer),
	}
}

// Outbound returns the receive end of the client's frame queue for the writer
// goroutine to range over. The channel is closed when the client is closed.
func (c *Client) Outbound() <-chan []byte {
	return c.send
}

// enqueue performs a non-blocking send of a copy of msg. It returns false when
// the buffer is full (the frame is dropped) or the client is already closed.
func (c *Client) enqueue(msg []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	cp := make([]byte, len(msg))
	copy(cp, msg)
	select {
	case c.send <- cp:
		return true
	default:
		return false
	}
}

// Send queues a single frame to this client, returning false when the frame was
// dropped (buffer full or client closed). Used for unicast messages such as the
// per-connection welcome and snapshot replies.
func (c *Client) Send(msg []byte) bool {
	return c.enqueue(msg)
}

// Close closes the outbound channel exactly once, signalling the writer loop to
// terminate. Safe to call multiple times and from multiple goroutines.
func (c *Client) Close() {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		close(c.send)
		c.mu.Unlock()
	})
}

// Room holds every client sharing one document id plus the latest snapshot.
type Room struct {
	id           string
	clients      map[*Client]struct{}
	snapshot     string
	snapshotRev  int64
	lastActivity time.Time
	mu           sync.Mutex
}

// ID returns the room's document id.
func (r *Room) ID() string { return r.id }

// Join adds a client to the room and refreshes the activity timer.
func (r *Room) Join(c *Client) {
	r.mu.Lock()
	r.clients[c] = struct{}{}
	r.lastActivity = time.Now()
	r.mu.Unlock()
}

// Leave removes a client from the room. It reports whether the room is now empty.
func (r *Room) Leave(c *Client) bool {
	r.mu.Lock()
	delete(r.clients, c)
	r.lastActivity = time.Now()
	empty := len(r.clients) == 0
	r.mu.Unlock()
	return empty
}

// Count returns the number of currently connected clients.
func (r *Room) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.clients)
}

// primaryLocked returns the oldest client (earliest join time, id as tie-break),
// which is the elected "primary". Caller must hold r.mu. Returns nil when empty.
func (r *Room) primaryLocked() *Client {
	var primary *Client
	for c := range r.clients {
		if primary == nil ||
			c.joined.Before(primary.joined) ||
			(c.joined.Equal(primary.joined) && c.ID < primary.ID) {
			primary = c
		}
	}
	return primary
}

// IsPrimary reports whether c is the elected primary client of the room.
func (r *Room) IsPrimary(c *Client) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.primaryLocked() == c
}

// Members returns a snapshot of all participants sorted by join time, with the
// primary flag set on the oldest member.
func (r *Room) Members() []Member {
	r.mu.Lock()
	defer r.mu.Unlock()
	primary := r.primaryLocked()
	out := make([]Member, 0, len(r.clients))
	for c := range r.clients {
		out = append(out, Member{
			ID:      c.ID,
			Name:    c.Name,
			Color:   c.Color,
			Cursor:  c.cursor,
			Primary: c == primary,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// SetCursor records the block id a client's caret is in for presence display.
func (r *Room) SetCursor(c *Client, cursor string) {
	r.mu.Lock()
	c.cursor = cursor
	r.lastActivity = time.Now()
	r.mu.Unlock()
}

// Broadcast queues msg to every client except exclude (pass nil to send to all).
// Slow clients whose buffer is full simply drop the frame.
func (r *Room) Broadcast(msg []byte, exclude *Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastActivity = time.Now()
	for c := range r.clients {
		if c == exclude {
			continue
		}
		c.enqueue(msg)
	}
}

// SetSnapshot stores data as the room's latest snapshot when rev is newer than
// the stored revision. It returns true when the snapshot was accepted.
func (r *Room) SetSnapshot(data string, rev int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rev < r.snapshotRev {
		return false
	}
	r.snapshot = data
	r.snapshotRev = rev
	r.lastActivity = time.Now()
	return true
}

// Snapshot returns the room's latest stored snapshot and its revision.
func (r *Room) Snapshot() (string, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot, r.snapshotRev
}

// idle reports how long the room has had no activity.
func (r *Room) idle() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return time.Since(r.lastActivity)
}

// Manager owns every live room and hands out presence colours.
type Manager struct {
	rooms    map[string]*Room
	colorIdx int
	mu       sync.RWMutex
}

// NewManager creates an empty room manager.
func NewManager() *Manager {
	return &Manager{rooms: make(map[string]*Room)}
}

// Room returns the room for id, creating it on first use.
func (m *Manager) Room(id string) *Room {
	m.mu.Lock()
	defer m.mu.Unlock()
	room, ok := m.rooms[id]
	if !ok {
		room = &Room{
			id:           id,
			clients:      make(map[*Client]struct{}),
			lastActivity: time.Now(),
		}
		m.rooms[id] = room
	}
	return room
}

// Lookup returns an existing room without creating one.
func (m *Manager) Lookup(id string) (*Room, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	room, ok := m.rooms[id]
	return room, ok
}

// RoomCount returns the number of live rooms.
func (m *Manager) RoomCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.rooms)
}

// NextColor returns the next presence colour from the palette, round-robin.
func (m *Manager) NextColor() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	color := memberColors[m.colorIdx%len(memberColors)]
	m.colorIdx++
	return color
}

// Sweep removes rooms that have been empty and idle for at least idleTimeout.
// It returns the ids of the rooms it removed.
func (m *Manager) Sweep(idleTimeout time.Duration) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var removed []string
	for id, room := range m.rooms {
		if room.Count() == 0 && room.idle() >= idleTimeout {
			delete(m.rooms, id)
			removed = append(removed, id)
		}
	}
	return removed
}

// RandomID returns a cryptographically random 16-byte hex identifier, used for
// per-connection client ids. It falls back to a timestamp-derived value only if
// the system random source is unavailable.
func RandomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "c" + hex.EncodeToString([]byte(time.Now().String()))[:31]
	}
	return hex.EncodeToString(b)
}
