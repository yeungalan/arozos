package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	prout "imuslab.com/arozos/mod/prouter"
	imapnotes "imuslab.com/arozos/mod/storage/imapnotes"
	"imuslab.com/arozos/mod/utils"
)

const imapNotesLogTag = "IMAPNotes"

var imapNotesServer *imapnotes.Server

// IMAPNotesInit starts the Apple Notes IMAP sync service if enabled in the database.
// It registers the admin settings page under the Network group.
func IMAPNotesInit() {
	sysdb.NewTable("imapnotes")

	// Wire up the settings UI
	registerSetting(settingModule{
		Name:         "Apple Notes Sync",
		Desc:         "Sync arozos Notes with iPhone Notes via IMAP",
		IconPath:     "SystemAO/notes_sync/img/icon.png",
		Group:        "Network",
		StartDir:     "SystemAO/notes_sync/index.html",
		RequireAdmin: true,
	})

	// Admin-only API router
	adminRouter := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "System Setting",
		AdminOnly:   true,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})

	adminRouter.HandleFunc("/system/imap_notes/enable", imapNotesHandleSetEnabled)
	adminRouter.HandleFunc("/system/imap_notes/status", imapNotesHandleGetStatus)

	// Non-admin: let any authenticated user fetch/manage their own token
	userRouter := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "System Setting",
		AdminOnly:   false,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})
	userRouter.HandleFunc("/system/imap_notes/token", imapNotesHandleToken)

	// Auto-start if previously enabled
	var enabled bool
	sysdb.Read("imapnotes", "enabled", &enabled)
	if enabled {
		systemWideLogger.PrintAndLog(imapNotesLogTag, "Feature was enabled at last shutdown — starting IMAP Notes server", nil)
		startIMAPNotesServer()
	} else {
		systemWideLogger.PrintAndLog(imapNotesLogTag, "IMAP Notes sync is disabled (enable it in System Settings → Network)", nil)
	}
}

func startIMAPNotesServer() {
	var port int
	sysdb.Read("imapnotes", "port", &port)
	if port <= 0 {
		port = 1143
	}

	systemWideLogger.PrintAndLog(imapNotesLogTag, fmt.Sprintf("Starting IMAP Notes server on port %d", port), nil)

	imapNotesServer = imapnotes.NewServer(imapnotes.Config{
		Port:        port,
		UserHandler: userHandler,
		AuthAgent:   authAgent,
		Database:    sysdb,
		Logger:      systemWideLogger,
	})

	if err := imapNotesServer.Start(); err != nil {
		systemWideLogger.PrintAndLog(imapNotesLogTag, "Failed to start IMAP Notes server: "+err.Error(), err)
		imapNotesServer = nil
		return
	}
	systemWideLogger.PrintAndLog(imapNotesLogTag, fmt.Sprintf("IMAP Notes server is now listening on port %d", port), nil)
}

// POST /system/imap_notes/enable?enabled=true&port=1143
func imapNotesHandleSetEnabled(w http.ResponseWriter, r *http.Request) {
	enabledStr, _ := utils.GetPara(r, "enabled")
	portStr, _ := utils.GetPara(r, "port")

	enabled := enabledStr == "true"

	var port int = 1143
	if portStr != "" {
		if p, err := parseInt(portStr); err == nil && p > 0 && p < 65536 {
			port = p
		}
	}

	sysdb.Write("imapnotes", "enabled", enabled)
	sysdb.Write("imapnotes", "port", port)

	if enabled {
		if imapNotesServer != nil && imapNotesServer.Running {
			systemWideLogger.PrintAndLog(imapNotesLogTag, "Restarting IMAP Notes server due to settings change", nil)
			imapNotesServer.Stop()
		}
		startIMAPNotesServer()
		if imapNotesServer == nil {
			utils.SendErrorResponse(w, "Server failed to start - check system logs")
			return
		}
	} else {
		if imapNotesServer != nil {
			systemWideLogger.PrintAndLog(imapNotesLogTag, "Stopping IMAP Notes server (disabled by admin)", nil)
			imapNotesServer.Stop()
			imapNotesServer = nil
			systemWideLogger.PrintAndLog(imapNotesLogTag, "IMAP Notes server stopped", nil)
		}
	}

	utils.SendTextResponse(w, "ok")
}

// GET /system/imap_notes/status
func imapNotesHandleGetStatus(w http.ResponseWriter, r *http.Request) {
	var enabled bool
	var port int
	sysdb.Read("imapnotes", "enabled", &enabled)
	sysdb.Read("imapnotes", "port", &port)
	if port <= 0 {
		port = 1143
	}

	running := imapNotesServer != nil && imapNotesServer.Running

	type statusResp struct {
		Enabled bool `json:"enabled"`
		Running bool `json:"running"`
		Port    int  `json:"port"`
	}
	resp := statusResp{
		Enabled: enabled,
		Running: running,
		Port:    port,
	}
	j, _ := json.Marshal(resp)
	utils.SendJSONResponse(w, string(j))
}

// GET  /system/imap_notes/token          — return the user's current token (if any)
// POST /system/imap_notes/token?action=generate  — create a new one
// POST /system/imap_notes/token?action=revoke&token=<t> — remove a specific token
func imapNotesHandleToken(w http.ResponseWriter, r *http.Request) {
	userinfo, err := userHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "User not logged in")
		return
	}

	action, _ := utils.GetPara(r, "action")

	switch action {
	case "generate":
		token := authAgent.NewAutologinToken(userinfo.Username)
		type tokenResp struct {
			Token string `json:"token"`
		}
		j, _ := json.Marshal(tokenResp{Token: token})
		utils.SendJSONResponse(w, string(j))

	case "revoke":
		token, err := utils.GetPara(r, "token")
		if err != nil {
			utils.SendErrorResponse(w, "Missing token parameter")
			return
		}
		authAgent.RemoveAutologinToken(token)
		utils.SendTextResponse(w, "ok")

	default:
		// List existing tokens for this user
		tokens := authAgent.GetTokensFromUsername(userinfo.Username)
		type tokenEntry struct {
			Token string `json:"token"`
		}
		var list []tokenEntry
		for _, t := range tokens {
			list = append(list, tokenEntry{Token: t.Token})
		}
		if list == nil {
			list = []tokenEntry{}
		}
		j, _ := json.Marshal(list)
		utils.SendJSONResponse(w, string(j))
	}
}

func parseInt(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit character: %c", c)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
