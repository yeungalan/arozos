package wsn

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	notification "imuslab.com/arozos/mod/notification"
)

// TestAgentMetadata verifies the static agent description methods.
func TestAgentMetadata(t *testing.T) {
	a := NewWebSocketNotificationAgent()
	if a.Name() != notification.AgentWebSocket {
		t.Errorf("expected name %q, got %q", notification.AgentWebSocket, a.Name())
	}
	if !a.IsConsumer() {
		t.Error("websocket agent should be a consumer")
	}
	if a.IsProducer() {
		t.Error("websocket agent should not be a producer")
	}
	if a.Desc() == "" {
		t.Error("expected a non-empty description")
	}
	if a.CountConnections() != 0 {
		t.Errorf("expected 0 connections on a fresh agent, got %d", a.CountConnections())
	}
}

// TestStringInSlice is a table-driven test for the membership helper.
func TestStringInSlice(t *testing.T) {
	cases := []struct {
		target string
		list   []string
		want   bool
	}{
		{"alice", []string{"alice", "bob"}, true},
		{"carol", []string{"alice", "bob"}, false},
		{"", []string{}, false},
		{"alice", nil, false},
	}
	for _, c := range cases {
		if got := stringInSlice(c.target, c.list); got != c.want {
			t.Errorf("stringInSlice(%q, %v) = %v, want %v", c.target, c.list, got, c.want)
		}
	}
}

// TestConsumerNotification_NoClients verifies that broadcasting with no
// connected clients is a no-op that does not error or panic.
func TestConsumerNotification_NoClients(t *testing.T) {
	a := NewWebSocketNotificationAgent()
	err := a.ConsumerNotification(&notification.NotificationPayload{
		ID:       "1",
		Title:    "Hello",
		Message:  "World",
		Receiver: []string{"alice"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// HandleNotificationWebSocket should reject an empty username before upgrade.
func TestHandleNotificationWebSocket_EmptyUsername(t *testing.T) {
	a := NewWebSocketNotificationAgent()
	if err := a.HandleNotificationWebSocket("", httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)); err == nil {
		t.Error("expected an error for empty username")
	}
}

// dialTestAgent spins up an httptest server backed by the agent and returns a
// connected client for the given username.
func dialTestAgent(t *testing.T, a *Agent, username string) (*websocket.Conn, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = a.HandleNotificationWebSocket(r.URL.Query().Get("user"), w, r)
	}))
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?user=" + username
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		server.Close()
		t.Fatalf("dial failed: %v", err)
	}
	//Wait until the server-side registration has completed.
	waitFor(t, func() bool { return a.CountConnections() >= 1 }, time.Second)
	return conn, server
}

// waitFor polls cond until it returns true or the timeout elapses.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestConsumerNotification_DeliversToTargetUser verifies real-time delivery to
// the matching connected session and that the icon defaulting works.
func TestConsumerNotification_DeliversToTargetUser(t *testing.T) {
	a := NewWebSocketNotificationAgent()
	conn, server := dialTestAgent(t, a, "alice")
	defer server.Close()
	defer conn.Close()

	err := a.ConsumerNotification(&notification.NotificationPayload{
		ID:       "n1",
		Title:    "Hello",
		Message:  "World",
		Sender:   "Test",
		Receiver: []string{"alice"},
	})
	if err != nil {
		t.Fatalf("ConsumerNotification failed: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	var got wsNotification
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid payload JSON: %v", err)
	}
	if got.Title != "Hello" || got.Message != "World" || got.Sender != "Test" {
		t.Errorf("unexpected payload: %+v", got)
	}
	if got.Icon != defaultDesktopIcon {
		t.Errorf("expected default icon %q, got %q", defaultDesktopIcon, got.Icon)
	}
}

// TestConsumerNotification_RespectsReceiverFilter verifies a session only
// receives notifications addressed to its user (or system-wide broadcasts).
func TestConsumerNotification_RespectsReceiverFilter(t *testing.T) {
	a := NewWebSocketNotificationAgent()
	conn, server := dialTestAgent(t, a, "alice")
	defer server.Close()
	defer conn.Close()

	//A notification for "bob" must NOT reach alice; a subsequent system-wide
	//broadcast (empty Receiver) must. Because alice never receives bob's
	//message, the first (and only) frame she can read is the broadcast — which
	//deterministically proves the receiver filter works.
	_ = a.ConsumerNotification(&notification.NotificationPayload{
		ID:       "n2",
		Title:    "Secret",
		Receiver: []string{"bob"},
	})
	_ = a.ConsumerNotification(&notification.NotificationPayload{
		ID:       "n3",
		Title:    "Announcement",
		Icon:     "bullhorn",
		Receiver: []string{},
	})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected to receive a message: %v", err)
	}
	var got wsNotification
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid payload JSON: %v", err)
	}
	if got.Title == "Secret" {
		t.Fatal("alice received a notification addressed to bob")
	}
	if got.Title != "Announcement" || got.Icon != "bullhorn" {
		t.Errorf("unexpected broadcast payload: %+v", got)
	}
}

// TestConnectionLifecycle verifies that connections are tracked and cleaned up.
func TestConnectionLifecycle(t *testing.T) {
	a := NewWebSocketNotificationAgent()
	conn, server := dialTestAgent(t, a, "alice")
	defer server.Close()

	if a.CountConnections() != 1 {
		t.Fatalf("expected 1 connection, got %d", a.CountConnections())
	}

	//Closing the client should make the server-side reader exit and deregister.
	conn.Close()
	waitFor(t, func() bool { return a.CountConnections() == 0 }, 2*time.Second)
	if a.CountConnections() != 0 {
		t.Errorf("expected 0 connections after close, got %d", a.CountConnections())
	}
}
