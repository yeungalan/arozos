package agi

import (
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

	Lets AGI scripts notify the user that is running the script. A single
	notification is produced; the notification system then decides how to deliver
	it, trying the registered agents in priority order (the user's connected
	desktop first, falling back to email when they are offline).

	Usage in AGI:
		requirelib("notification");
		notification.send("Backup done", "Your nightly backup has completed");
		notification.send("Disk full", "Please free up space", "warning circle");
*/

func (g *Gateway) NotificationLibRegister() {
	err := g.RegisterLib("notification", g.injectNotificationLibFunctions)
	if err != nil {
		logger.PrintAndLog("Agi", "Failed to register notification lib: "+err.Error(), nil)
	}
}

func (g *Gateway) injectNotificationLibFunctions(payload *static.AgiLibInjectionPayload) {
	vm := payload.VM
	u := payload.User

	// _notification_send(title, message, icon) → bool
	vm.Set("_notification_send", func(call otto.FunctionCall) otto.Value {
		title, _ := call.Argument(0).ToString()
		message, _ := call.Argument(1).ToString()
		icon, _ := call.Argument(2).ToString()

		if g.Option == nil || g.Option.NotificationQueue == nil {
			g.RaiseError(errors.New("notification queue is not available"))
			return otto.FalseValue()
		}
		if u == nil || u.Username == "" {
			g.RaiseError(errors.New("notification requires a user context"))
			return otto.FalseValue()
		}

		if title == "" {
			title = "Notification"
		}

		//Produce a single notification addressed to the current user. The agent
		//list is intentionally left empty so the queue delivers it automatically
		//based on agent order (desktop first, email fallback).
		g.Option.NotificationQueue.BroadcastNotification(&notification.NotificationPayload{
			ID:       uuid.NewV4().String(),
			Title:    title,
			Message:  message,
			Icon:     icon,
			Receiver: []string{u.Username},
			Sender:   "ArozOS Notification",
		})
		return otto.TrueValue()
	})

	//nolint:errcheck
	vm.Run(`
		var notification = {};
		notification.send = function(title, message, icon) {
			return _notification_send(title, message, icon || "");
		};
	`)
}
