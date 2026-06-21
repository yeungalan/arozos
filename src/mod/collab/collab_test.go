package collab

import (
	"testing"
	"time"
)

// drain reads all currently-queued frames from a client without blocking.
func drain(c *Client) []string {
	var out []string
	for {
		select {
		case msg, ok := <-c.Outbound():
			if !ok {
				return out
			}
			out = append(out, string(msg))
		default:
			return out
		}
	}
}

func TestRoomJoinLeave(t *testing.T) {
	m := NewManager()
	room := m.Room("doc-1")

	if got := room.Count(); got != 0 {
		t.Fatalf("new room should be empty, got %d", got)
	}

	a := NewClient("a", "Alice", "#111", 8)
	b := NewClient("b", "Bob", "#222", 8)
	room.Join(a)
	room.Join(b)

	if got := room.Count(); got != 2 {
		t.Fatalf("expected 2 members, got %d", got)
	}

	if empty := room.Leave(a); empty {
		t.Errorf("room should not be empty after removing one of two clients")
	}
	if empty := room.Leave(b); !empty {
		t.Errorf("room should be empty after removing the last client")
	}
}

func TestManagerRoomIsStable(t *testing.T) {
	m := NewManager()
	r1 := m.Room("same")
	r2 := m.Room("same")
	if r1 != r2 {
		t.Fatalf("Room() must return the same room for the same id")
	}
	if _, ok := m.Lookup("missing"); ok {
		t.Errorf("Lookup of unknown id should report not found")
	}
	if _, ok := m.Lookup("same"); !ok {
		t.Errorf("Lookup of known id should succeed")
	}
}

func TestPrimaryElection(t *testing.T) {
	tests := []struct {
		name        string
		joinOrder   []string
		leave       string
		wantPrimary string
	}{
		{"single member is primary", []string{"a"}, "", "a"},
		{"oldest member is primary", []string{"a", "b", "c"}, "", "a"},
		{"primary moves on leave", []string{"a", "b", "c"}, "a", "b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			room := NewManager().Room("d")
			clients := map[string]*Client{}
			for i, id := range tt.joinOrder {
				c := NewClient(id, id, "#000", 4)
				// Force a deterministic, distinct join time ordering.
				c.joined = time.Unix(0, int64(i+1))
				clients[id] = c
				room.Join(c)
			}
			if tt.leave != "" {
				room.Leave(clients[tt.leave])
			}

			var gotPrimary string
			for _, mem := range room.Members() {
				if mem.Primary {
					if gotPrimary != "" {
						t.Fatalf("more than one primary elected")
					}
					gotPrimary = mem.ID
				}
			}
			if gotPrimary != tt.wantPrimary {
				t.Errorf("primary = %q, want %q", gotPrimary, tt.wantPrimary)
			}
			if want := clients[tt.wantPrimary]; want != nil && !room.IsPrimary(want) {
				t.Errorf("IsPrimary(%q) should be true", tt.wantPrimary)
			}
		})
	}
}

func TestMembersReflectCursor(t *testing.T) {
	room := NewManager().Room("d")
	a := NewClient("a", "Alice", "#111", 4)
	room.Join(a)
	room.SetCursor(a, "block-42")

	members := room.Members()
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}
	if members[0].Cursor != "block-42" {
		t.Errorf("cursor = %q, want block-42", members[0].Cursor)
	}
	if members[0].Name != "Alice" || members[0].Color != "#111" {
		t.Errorf("member identity not reflected: %+v", members[0])
	}
}

func TestBroadcastExcludesSender(t *testing.T) {
	room := NewManager().Room("d")
	a := NewClient("a", "Alice", "#111", 8)
	b := NewClient("b", "Bob", "#222", 8)
	c := NewClient("c", "Carol", "#333", 8)
	room.Join(a)
	room.Join(b)
	room.Join(c)

	room.Broadcast([]byte("hello"), a)

	if got := drain(a); len(got) != 0 {
		t.Errorf("excluded sender should receive nothing, got %v", got)
	}
	for _, peer := range []*Client{b, c} {
		got := drain(peer)
		if len(got) != 1 || got[0] != "hello" {
			t.Errorf("peer %s = %v, want [hello]", peer.ID, got)
		}
	}
}

func TestBroadcastToAll(t *testing.T) {
	room := NewManager().Room("d")
	a := NewClient("a", "Alice", "#111", 8)
	room.Join(a)
	room.Broadcast([]byte("ping"), nil)
	if got := drain(a); len(got) != 1 || got[0] != "ping" {
		t.Errorf("broadcast to all = %v, want [ping]", got)
	}
}

func TestBroadcastDropsWhenBufferFull(t *testing.T) {
	room := NewManager().Room("d")
	a := NewClient("a", "Alice", "#111", 1) // tiny buffer
	room.Join(a)

	// First frame fills the buffer, the rest must be dropped (no panic / block).
	for i := 0; i < 5; i++ {
		room.Broadcast([]byte("x"), nil)
	}
	if got := len(drain(a)); got != 1 {
		t.Errorf("expected exactly 1 buffered frame, got %d", got)
	}
}

func TestSnapshotRevisionGating(t *testing.T) {
	tests := []struct {
		name     string
		applyRev []int64
		wantData string
		wantRev  int64
	}{
		{"first snapshot accepted", []int64{1}, "v1", 1},
		{"newer revision wins", []int64{1, 2}, "v2", 2},
		{"stale revision rejected", []int64{2, 1}, "v2", 2},
		{"equal revision overwrites", []int64{2, 2}, "v2b", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			room := NewManager().Room("d")
			for i, rev := range tt.applyRev {
				data := "v" + itoa(rev)
				// Make the final equal-revision write distinguishable.
				if tt.name == "equal revision overwrites" && i == len(tt.applyRev)-1 {
					data = "v2b"
				}
				room.SetSnapshot(data, rev)
			}
			gotData, gotRev := room.Snapshot()
			if gotData != tt.wantData || gotRev != tt.wantRev {
				t.Errorf("snapshot = (%q,%d), want (%q,%d)", gotData, gotRev, tt.wantData, tt.wantRev)
			}
		})
	}
}

func TestSweepRemovesEmptyIdleRooms(t *testing.T) {
	m := NewManager()
	live := m.Room("live")
	idle := m.Room("idle")

	// Keep one client in "live" so it is never swept.
	live.Join(NewClient("a", "Alice", "#111", 4))

	// Force "idle" to look stale.
	idle.mu.Lock()
	idle.lastActivity = time.Now().Add(-time.Hour)
	idle.mu.Unlock()

	removed := m.Sweep(time.Minute)
	if len(removed) != 1 || removed[0] != "idle" {
		t.Fatalf("sweep removed = %v, want [idle]", removed)
	}
	if _, ok := m.Lookup("idle"); ok {
		t.Errorf("idle room should have been removed")
	}
	if _, ok := m.Lookup("live"); !ok {
		t.Errorf("live room should remain")
	}
	if m.RoomCount() != 1 {
		t.Errorf("RoomCount = %d, want 1", m.RoomCount())
	}
}

func TestNextColorRotates(t *testing.T) {
	m := NewManager()
	first := m.NextColor()
	seen := map[string]bool{first: true}
	for i := 1; i < len(memberColors); i++ {
		seen[m.NextColor()] = true
	}
	if len(seen) != len(memberColors) {
		t.Errorf("expected %d distinct colours, got %d", len(memberColors), len(seen))
	}
	// After a full cycle the palette wraps back to the first colour.
	if got := m.NextColor(); got != first {
		t.Errorf("palette should wrap to %q, got %q", first, got)
	}
}

func TestRandomIDUniqueAndSized(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := RandomID()
		if len(id) != 32 {
			t.Fatalf("RandomID length = %d, want 32", len(id))
		}
		if seen[id] {
			t.Fatalf("RandomID produced a duplicate: %s", id)
		}
		seen[id] = true
	}
}

func TestClientSendUnicast(t *testing.T) {
	c := NewClient("a", "Alice", "#111", 4)
	if ok := c.Send([]byte("hi")); !ok {
		t.Fatalf("Send to an open client should succeed")
	}
	got := drain(c)
	if len(got) != 1 || got[0] != "hi" {
		t.Errorf("unicast = %v, want [hi]", got)
	}
}

func TestClientCloseIsIdempotent(t *testing.T) {
	c := NewClient("a", "Alice", "#111", 4)
	c.Close()
	c.Close() // must not panic on double close
	if c.enqueue([]byte("x")) {
		t.Errorf("enqueue after close should fail")
	}
}

// itoa is a tiny helper to avoid pulling strconv into table data above.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
