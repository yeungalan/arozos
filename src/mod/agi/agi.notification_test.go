package agi

import (
	"testing"

	"github.com/robertkrimen/otto"
	"imuslab.com/arozos/mod/agi/static"
	notification "imuslab.com/arozos/mod/notification"
)

// ─── resolveReceivers ─────────────────────────────────────────────────────────

func TestResolveReceivers(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want []string
	}{
		{"nil", nil, []string{}},
		{"empty string", "", []string{}},
		{"single string", "alice", []string{"alice"}},
		{"interface slice", []interface{}{"alice", "bob"}, []string{"alice", "bob"}},
		{"interface slice with blanks", []interface{}{"alice", "", 42}, []string{"alice"}},
		{"string slice", []string{"alice", "bob"}, []string{"alice", "bob"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveReceivers(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("resolveReceivers(%v) = %v, want %v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// ─── buildNotificationPayload ─────────────────────────────────────────────────

func TestBuildNotificationPayload(t *testing.T) {
	cases := []struct {
		name        string
		currentUser string
		isAdmin     bool
		optJSON     string
		wantErr     bool
		wantRcv     []string
		wantAgents  []string
		wantTitle   string
		wantIcon    string
		wantSender  string
	}{
		{
			name:        "defaults to self and websocket agent",
			currentUser: "alice",
			optJSON:     `{"message":"hi"}`,
			wantRcv:     []string{"alice"},
			wantAgents:  []string{notification.AgentWebSocket},
			wantTitle:   "Notification",
			wantSender:  "ArozOS Notification",
		},
		{
			name:        "explicit email agent and icon",
			currentUser: "alice",
			optJSON:     `{"title":"Hi","message":"there","icon":"warning circle","agents":["smtpn"]}`,
			wantRcv:     []string{"alice"},
			wantAgents:  []string{notification.AgentSMTP},
			wantTitle:   "Hi",
			wantIcon:    "warning circle",
			wantSender:  "ArozOS Notification",
		},
		{
			name:        "custom sender preserved",
			currentUser: "alice",
			optJSON:     `{"title":"Hi","sender":"My App"}`,
			wantRcv:     []string{"alice"},
			wantAgents:  []string{notification.AgentWebSocket},
			wantTitle:   "Hi",
			wantSender:  "My App",
		},
		{
			name:        "non-admin to self allowed",
			currentUser: "alice",
			optJSON:     `{"title":"Hi","user":"alice"}`,
			wantRcv:     []string{"alice"},
			wantAgents:  []string{notification.AgentWebSocket},
			wantTitle:   "Hi",
			wantSender:  "ArozOS Notification",
		},
		{
			name:        "non-admin to other denied",
			currentUser: "alice",
			optJSON:     `{"title":"Hi","user":"bob"}`,
			wantErr:     true,
		},
		{
			name:        "admin to other allowed",
			currentUser: "admin",
			isAdmin:     true,
			optJSON:     `{"title":"Hi","user":"bob"}`,
			wantRcv:     []string{"bob"},
			wantAgents:  []string{notification.AgentWebSocket},
			wantTitle:   "Hi",
			wantSender:  "ArozOS Notification",
		},
		{
			name:        "admin to multiple users",
			currentUser: "admin",
			isAdmin:     true,
			optJSON:     `{"title":"Hi","user":["bob","carol"]}`,
			wantRcv:     []string{"bob", "carol"},
			wantAgents:  []string{notification.AgentWebSocket},
			wantTitle:   "Hi",
			wantSender:  "ArozOS Notification",
		},
		{
			name:        "invalid json",
			currentUser: "alice",
			optJSON:     `{not json}`,
			wantErr:     true,
		},
		{
			name:        "no receiver and no current user",
			currentUser: "",
			optJSON:     `{"title":"Hi"}`,
			wantErr:     true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload, err := buildNotificationPayload(c.currentUser, c.isAdmin, c.optJSON)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got payload %+v", payload)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if payload.ID == "" {
				t.Error("expected a generated ID")
			}
			if payload.Title != c.wantTitle {
				t.Errorf("title: got %q, want %q", payload.Title, c.wantTitle)
			}
			if payload.Icon != c.wantIcon {
				t.Errorf("icon: got %q, want %q", payload.Icon, c.wantIcon)
			}
			if payload.Sender != c.wantSender {
				t.Errorf("sender: got %q, want %q", payload.Sender, c.wantSender)
			}
			assertStringSliceEqual(t, "receivers", payload.Receiver, c.wantRcv)
			assertStringSliceEqual(t, "agents", payload.ReciverAgents, c.wantAgents)
		})
	}
}

func assertStringSliceEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s[%d]: got %q, want %q", label, i, got[i], want[i])
		}
	}
}

// ─── otto integration ─────────────────────────────────────────────────────────

// recordingAgent is a consumer agent that records the notifications it receives.
type recordingAgent struct {
	name     string
	received []*notification.NotificationPayload
}

func (r *recordingAgent) Name() string     { return r.name }
func (r *recordingAgent) Desc() string     { return "recording agent" }
func (r *recordingAgent) IsConsumer() bool { return true }
func (r *recordingAgent) IsProducer() bool { return false }
func (r *recordingAgent) ConsumerNotification(p *notification.NotificationPayload) error {
	r.received = append(r.received, p)
	return nil
}
func (r *recordingAgent) ProduceNotification(fn *notification.AgentProducerFunction) {}

// TestInjectNotificationLib_JSObjectExposed verifies the JS object surface.
func TestInjectNotificationLib_JSObjectExposed(t *testing.T) {
	g := minimalGateway()
	vm := otto.New()
	g.injectNotificationLibFunctions(&static.AgiLibInjectionPayload{
		VM:   vm,
		User: stubUser("alice"),
	})

	for _, fn := range []string{"notification.push", "notification.send", "notification.email", "notification.sendToUser", "notification.emailToUser"} {
		val, err := vm.Run("typeof " + fn)
		if err != nil {
			t.Fatalf("evaluating typeof %s: %v", fn, err)
		}
		s, _ := val.ToString()
		if s != "function" {
			t.Errorf("%s should be a function, got %q", fn, s)
		}
	}
}

// TestInjectNotificationLib_SendReachesQueue runs notification.send() from a
// script and asserts the notification is routed through the queue to the agent.
func TestInjectNotificationLib_SendReachesQueue(t *testing.T) {
	g := minimalGateway()
	queue := notification.NewNotificationQueue()
	agent := &recordingAgent{name: notification.AgentWebSocket}
	queue.RegisterNotificationAgent(agent)
	g.Option.NotificationQueue = queue

	vm := otto.New()
	g.injectNotificationLibFunctions(&static.AgiLibInjectionPayload{
		VM:   vm,
		User: stubUser("alice"),
	})

	val, err := vm.Run(`notification.send("Backup done", "All good", "check circle");`)
	if err != nil {
		t.Fatalf("script error: %v", err)
	}
	if ok, _ := val.ToBoolean(); !ok {
		t.Fatal("notification.send should return true")
	}

	if len(agent.received) != 1 {
		t.Fatalf("expected 1 received notification, got %d", len(agent.received))
	}
	got := agent.received[0]
	if got.Title != "Backup done" || got.Message != "All good" || got.Icon != "check circle" {
		t.Errorf("unexpected notification: %+v", got)
	}
	if len(got.Receiver) != 1 || got.Receiver[0] != "alice" {
		t.Errorf("expected receiver [alice], got %v", got.Receiver)
	}
}

// TestInjectNotificationLib_NoQueue verifies a missing queue fails gracefully
// (returns false) instead of panicking.
func TestInjectNotificationLib_NoQueue(t *testing.T) {
	g := minimalGateway() // Option.NotificationQueue is nil
	vm := otto.New()
	g.injectNotificationLibFunctions(&static.AgiLibInjectionPayload{
		VM:   vm,
		User: stubUser("alice"),
	})

	val, err := vm.Run(`notification.send("x", "y");`)
	if err != nil {
		t.Fatalf("script error: %v", err)
	}
	if ok, _ := val.ToBoolean(); ok {
		t.Error("notification.send should return false when the queue is unavailable")
	}
}
