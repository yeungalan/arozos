package agi

import (
	"encoding/json"
	"errors"

	"github.com/robertkrimen/otto"
	uuid "github.com/satori/go.uuid"

	"imuslab.com/arozos/mod/agi/static"
	"imuslab.com/arozos/mod/info/logger"
	notification "imuslab.com/arozos/mod/notification"
)

/*
	AGI Notification Library
	Author: tobychui

	Lets AGI scripts push notifications to the user. Notifications can be
	delivered to the user's connected desktop notification center in real time
	and/or sent as an email.

	Usage in AGI:
		requirelib("notification");

		// Desktop notification to the current user (icon is optional)
		notification.send("Backup done", "Your nightly backup has completed");
		notification.send("Disk full", "Please free up space", "warning circle");

		// Email notification to the current user
		notification.email("Backup done", "Your nightly backup has completed");

		// Admin only: target another user
		notification.sendToUser("alice", "Hello", "A message for you");
		notification.emailToUser("alice", "Hello", "A message for you");

		// Full control
		notification.push({
			title: "Hello",
			message: "World",
			icon: "info circle",            // optional, desktop only
			sender: "My App",               // optional
			user: "alice" | ["a", "b"],     // optional, defaults to current user (admin needed for others)
			agents: ["websocket", "smtpn"]  // optional, defaults to ["websocket"]
		});
*/

// notificationOptions is the option object accepted by notification.push().
// User may be a single username (string) or a list of usernames ([]string).
type notificationOptions struct {
	Title   string      `json:"title"`
	Message string      `json:"message"`
	Icon    string      `json:"icon"`
	Sender  string      `json:"sender"`
	User    interface{} `json:"user"`
	Agents  []string    `json:"agents"`
}

func (g *Gateway) NotificationLibRegister() {
	err := g.RegisterLib("notification", g.injectNotificationLibFunctions)
	if err != nil {
		logger.PrintAndLog("Agi", "Failed to register notification lib: "+err.Error(), nil)
	}
}

func (g *Gateway) injectNotificationLibFunctions(payload *static.AgiLibInjectionPayload) {
	vm := payload.VM
	u := payload.User

	// _notification_push(optionsJSON) → bool
	vm.Set("_notification_push", func(call otto.FunctionCall) otto.Value {
		optJSON, err := call.Argument(0).ToString()
		if err != nil {
			g.RaiseError(err)
			return otto.FalseValue()
		}

		if g.Option == nil || g.Option.NotificationQueue == nil {
			g.RaiseError(errors.New("notification queue is not available"))
			return otto.FalseValue()
		}

		username := ""
		isAdmin := false
		if u != nil {
			username = u.Username
			isAdmin = u.IsAdmin()
		}

		notificationPayload, err := buildNotificationPayload(username, isAdmin, optJSON)
		if err != nil {
			g.RaiseError(err)
			return otto.FalseValue()
		}

		if err := g.Option.NotificationQueue.BroadcastNotification(notificationPayload); err != nil {
			g.RaiseError(err)
			return otto.FalseValue()
		}
		return otto.TrueValue()
	})

	//nolint:errcheck
	vm.Run(`
		var notification = {};
		notification.push = function(opt) {
			if (typeof opt !== "object" || opt === null) { opt = {}; }
			return _notification_push(JSON.stringify(opt));
		};
		notification.send = function(title, message, icon) {
			return notification.push({title: title, message: message, icon: icon || "info circle", agents: ["websocket"]});
		};
		notification.email = function(title, message) {
			return notification.push({title: title, message: message, agents: ["smtpn"]});
		};
		notification.sendToUser = function(username, title, message, icon) {
			return notification.push({user: username, title: title, message: message, icon: icon || "info circle", agents: ["websocket"]});
		};
		notification.emailToUser = function(username, title, message) {
			return notification.push({user: username, title: title, message: message, agents: ["smtpn"]});
		};
	`)
}

// buildNotificationPayload validates the option JSON sent from an AGI script and
// turns it into a notification.NotificationPayload. It enforces that only
// administrators may target users other than themselves. This is kept as a pure
// function (no VM / user dependencies) so it can be unit tested in isolation.
func buildNotificationPayload(currentUsername string, isAdmin bool, optJSON string) (*notification.NotificationPayload, error) {
	var opt notificationOptions
	if err := json.Unmarshal([]byte(optJSON), &opt); err != nil {
		return nil, errors.New("invalid notification options: " + err.Error())
	}

	//Resolve the list of receivers, defaulting to the current user.
	receivers := resolveReceivers(opt.User)
	if len(receivers) == 0 {
		if currentUsername == "" {
			return nil, errors.New("no notification receiver specified")
		}
		receivers = []string{currentUsername}
	}

	//Permission check: non-admins may only notify themselves.
	if !isAdmin {
		for _, receiver := range receivers {
			if receiver != currentUsername {
				return nil, errors.New("permission denied: only administrators can send notifications to other users")
			}
		}
	}

	//Default delivery agent is the real-time desktop notification center.
	agents := opt.Agents
	if len(agents) == 0 {
		agents = []string{notification.AgentWebSocket}
	}

	title := opt.Title
	if title == "" {
		title = "Notification"
	}

	sender := opt.Sender
	if sender == "" {
		sender = "ArozOS Notification"
	}

	return &notification.NotificationPayload{
		ID:            uuid.NewV4().String(),
		Title:         title,
		Message:       opt.Message,
		Icon:          opt.Icon,
		Receiver:      receivers,
		Sender:        sender,
		ReciverAgents: agents,
	}, nil
}

// resolveReceivers extracts a list of usernames from the polymorphic "user"
// option, which may be a single string or an array of strings. Empty / invalid
// entries are skipped.
func resolveReceivers(user interface{}) []string {
	receivers := []string{}
	switch v := user.(type) {
	case string:
		if v != "" {
			receivers = append(receivers, v)
		}
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				receivers = append(receivers, s)
			}
		}
	case []string:
		for _, s := range v {
			if s != "" {
				receivers = append(receivers, s)
			}
		}
	}
	return receivers
}
