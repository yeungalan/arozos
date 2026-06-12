package main

import (
	"net/http"

	fs "imuslab.com/arozos/mod/filesystem"
	notification "imuslab.com/arozos/mod/notification"
	"imuslab.com/arozos/mod/notification/agents/smtpn"
	"imuslab.com/arozos/mod/notification/agents/wsn"
	prout "imuslab.com/arozos/mod/prouter"
	"imuslab.com/arozos/mod/utils"
)

var (
	notificationQueue   *notification.NotificationQueue
	wsNotificationAgent *wsn.Agent
)

func notificationInit() {
	//Create a new notification queue
	notificationQueue = notification.NewNotificationQueue()

	//Register the notification agents. Registration order defines the delivery
	//priority: the queue tries each agent in turn and stops at the first that
	//reaches the user. The desktop agent is registered first so notifications go
	//to a connected desktop in real time, and only fall back to email when the
	//user is offline.

	/*
		WebSocket Notification Agent
		For real-time delivery to the user's connected desktop notification center
	*/
	wsNotificationAgent = wsn.NewWebSocketNotificationAgent()
	notificationQueue.RegisterNotificationAgent(wsNotificationAgent)

	//Register the desktop notification listener endpoint. Any logged-in user may
	//open a stream and will only receive notifications addressed to them.
	notificationRouter := prout.NewModuleRouter(prout.RouterOption{
		AdminOnly:   false,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})
	notificationRouter.HandleFunc("/system/notification/listen", func(w http.ResponseWriter, r *http.Request) {
		username, err := authAgent.GetUserName(w, r)
		if err != nil {
			utils.SendErrorResponse(w, "User not logged in")
			return
		}
		if err := wsNotificationAgent.HandleNotificationWebSocket(username, w, r); err != nil {
			systemWideLogger.PrintAndLog("Notification", "WebSocket notification listener error", err)
		}
	})

	/*
		SMTP Notification Agent
		For handling notification sending via Mail
	*/
	smtpnConfigPath := "./system/smtp_conf.json"
	if !fs.FileExists(smtpnConfigPath) {
		//Create an empty one
		smtpn.GenerateEmptyConfigFile(smtpnConfigPath)
	}

	smtpAgent, err := smtpn.NewSMTPNotificationAgent(*host_name, smtpnConfigPath,
		func(username string) (string, error) {
			//Translate username to email
			return registerHandler.GetUserEmail(username)
		})

	if err != nil {
		systemWideLogger.PrintAndLog("Notification", "Unable to start smtpn agent: "+err.Error(), nil)
	} else {
		notificationQueue.RegisterNotificationAgent(smtpAgent)
	}
}
