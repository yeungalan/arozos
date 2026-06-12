package wsn

/*

	WebSocket Notification Agent

	This agent delivers notifications to connected ArozOS desktop sessions in
	real time over a WebSocket connection. Each logged-in desktop opens a
	connection to the notification listen endpoint; when the notification queue
	broadcasts a message routed to this agent, the message is pushed to every
	connected session that belongs to the target user(s).

	If a notification has an empty Receiver list it is treated as a system-wide
	broadcast and delivered to every connected session.

*/

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	notification "imuslab.com/arozos/mod/notification"
)

const (
	writeWait          = 10 * time.Second    //Time allowed to write a message to the peer
	pongWait           = 60 * time.Second    //Time allowed to read the next pong from the peer
	pingPeriod         = (pongWait * 9) / 10 //Send pings to peer with this period; must be < pongWait
	defaultDesktopIcon = "info circle"       //Fallback icon when none is provided
)

// wsNotification is the JSON structure delivered to the browser notification center.
type wsNotification struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Message string `json:"message"`
	Sender  string `json:"sender"`
	Icon    string `json:"icon"`
}

// client represents a single connected desktop session.
type client struct {
	conn     *websocket.Conn
	username string
	writeMu  sync.Mutex //gorilla/websocket does not allow concurrent writers
}

// writeJSON safely writes a JSON message to the client with a write deadline.
func (c *client) writeJSON(payload interface{}) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return c.conn.WriteJSON(payload)
}

// ping sends a WebSocket ping control frame to keep the connection alive.
func (c *client) ping() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return c.conn.WriteMessage(websocket.PingMessage, nil)
}

// Agent is the WebSocket notification consumer agent.
type Agent struct {
	clients  map[*client]bool //Set of currently connected desktop sessions
	mu       sync.RWMutex     //Guards clients
	upgrader websocket.Upgrader
}

// NewWebSocketNotificationAgent creates a new, ready-to-use WebSocket notification agent.
func NewWebSocketNotificationAgent() *Agent {
	return &Agent{
		clients: map[*client]bool{},
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}
}

func (a *Agent) Name() string     { return notification.AgentWebSocket }
func (a *Agent) Desc() string     { return "Notify user on their connected desktop in real time" }
func (a *Agent) IsConsumer() bool { return true }
func (a *Agent) IsProducer() bool { return false }

// CountConnections returns the number of currently connected desktop sessions.
func (a *Agent) CountConnections() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.clients)
}

// addClient registers a connected client.
func (a *Agent) addClient(c *client) {
	a.mu.Lock()
	a.clients[c] = true
	a.mu.Unlock()
}

// removeClient unregisters a client and closes its connection. It is safe to
// call multiple times for the same client; the underlying connection is only
// closed once.
func (a *Agent) removeClient(c *client) {
	a.mu.Lock()
	_, existed := a.clients[c]
	if existed {
		delete(a.clients, c)
	}
	a.mu.Unlock()
	if existed {
		c.conn.Close()
	}
}

// ConsumerNotification delivers an incoming notification to all matching
// connected sessions. A notification with an empty Receiver list is delivered
// to every connected session. It reports delivered=true when at least one
// session actually received the message (i.e. the target user is online), so
// the queue knows it does not need to fall back to email.
func (a *Agent) ConsumerNotification(incomingNotification *notification.NotificationPayload) (bool, error) {
	icon := incomingNotification.Icon
	if icon == "" {
		icon = defaultDesktopIcon
	}
	payload := wsNotification{
		ID:      incomingNotification.ID,
		Title:   incomingNotification.Title,
		Message: incomingNotification.Message,
		Sender:  incomingNotification.Sender,
		Icon:    icon,
	}

	//Snapshot the matching clients under a read lock so we never hold the lock
	//while performing (potentially blocking) network writes.
	a.mu.RLock()
	targets := []*client{}
	for c := range a.clients {
		if len(incomingNotification.Receiver) == 0 || stringInSlice(c.username, incomingNotification.Receiver) {
			targets = append(targets, c)
		}
	}
	a.mu.RUnlock()

	deliveredCount := 0
	for _, c := range targets {
		if err := c.writeJSON(payload); err != nil {
			//Dead connection, drop it
			a.removeClient(c)
			continue
		}
		deliveredCount++
	}
	return deliveredCount > 0, nil
}

// ProduceNotification is unused; this agent is consumer-only.
func (a *Agent) ProduceNotification(producerListeningEndpoint *notification.AgentProducerFunction) {
}

// HandleNotificationWebSocket upgrades an (already authenticated) HTTP request
// to a WebSocket connection and registers it as a desktop session for the given
// username. It blocks until the connection is closed.
func (a *Agent) HandleNotificationWebSocket(username string, w http.ResponseWriter, r *http.Request) error {
	if username == "" {
		return errors.New("username cannot be empty")
	}
	conn, err := a.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}

	thisClient := &client{conn: conn, username: username}
	a.addClient(thisClient)
	defer a.removeClient(thisClient)

	//Configure read deadline + pong handler so dead peers are detected.
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	//Ping ticker to keep the connection alive and detect dead peers.
	stopPing := make(chan struct{})
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := thisClient.ping(); err != nil {
					a.removeClient(thisClient)
					return
				}
			case <-stopPing:
				return
			}
		}
	}()

	//Reader loop: we do not expect inbound data, but reading lets us process
	//control frames (pong) and detect when the connection is closed. Blocks
	//until the connection is closed or times out.
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	close(stopPing)
	return nil
}

// stringInSlice reports whether target is present in list.
func stringInSlice(target string, list []string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
