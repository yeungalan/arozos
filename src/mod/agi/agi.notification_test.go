package agi

import (
	"testing"

	"github.com/robertkrimen/otto"
	"imuslab.com/arozos/mod/agi/static"
	notification "imuslab.com/arozos/mod/notification"
)

// recordingAgent is a consumer agent that records the notifications it receives
// and reports a configurable delivery result.
type recordingAgent struct {
	name      string
	delivered bool
	received  []*notification.NotificationPayload
}

func (r *recordingAgent) Name() string     { return r.name }
func (r *recordingAgent) Desc() string     { return "recording agent" }
func (r *recordingAgent) IsConsumer() bool { return true }
func (r *recordingAgent) IsProducer() bool { return false }
func (r *recordingAgent) ConsumerNotification(p *notification.NotificationPayload) (bool, error) {
	r.received = append(r.received, p)
	return r.delivered, nil
}
func (r *recordingAgent) ProduceNotification(fn *notification.AgentProducerFunction) {}

// TestInjectNotificationLib_JSObjectExposed verifies the (single) JS function.
func TestInjectNotificationLib_JSObjectExposed(t *testing.T) {
	g := minimalGateway()
	vm := otto.New()
	g.injectNotificationLibFunctions(&static.AgiLibInjectionPayload{
		VM:   vm,
		User: stubUser("alice"),
	})

	val, err := vm.Run(`typeof notification.send`)
	if err != nil {
		t.Fatalf("evaluating typeof notification.send: %v", err)
	}
	if s, _ := val.ToString(); s != "function" {
		t.Errorf("notification.send should be a function, got %q", s)
	}
}

// TestInjectNotificationLib_SendReachesQueue runs notification.send() from a
// script and asserts a single notification, addressed to the current user, is
// routed through the queue.
func TestInjectNotificationLib_SendReachesQueue(t *testing.T) {
	g := minimalGateway()
	queue := notification.NewNotificationQueue()
	agent := &recordingAgent{name: notification.AgentWebSocket, delivered: true}
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
	//The AGI library must not pin a specific agent — delivery is automatic.
	if len(got.ReciverAgents) != 0 {
		t.Errorf("expected no pinned agents, got %v", got.ReciverAgents)
	}
}

// TestInjectNotificationLib_DefaultTitle verifies an empty title is defaulted.
func TestInjectNotificationLib_DefaultTitle(t *testing.T) {
	g := minimalGateway()
	queue := notification.NewNotificationQueue()
	agent := &recordingAgent{name: notification.AgentWebSocket, delivered: true}
	queue.RegisterNotificationAgent(agent)
	g.Option.NotificationQueue = queue

	vm := otto.New()
	g.injectNotificationLibFunctions(&static.AgiLibInjectionPayload{
		VM:   vm,
		User: stubUser("alice"),
	})

	if _, err := vm.Run(`notification.send("", "body only");`); err != nil {
		t.Fatalf("script error: %v", err)
	}
	if len(agent.received) != 1 {
		t.Fatalf("expected 1 received notification, got %d", len(agent.received))
	}
	if agent.received[0].Title != "Notification" {
		t.Errorf("expected default title 'Notification', got %q", agent.received[0].Title)
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
