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
func IMAPNotesInit() {
	sysdb.NewTable("imapnotes")

	registerSetting(settingModule{
		Name:         "Apple Notes Sync",
		Desc:         "Sync arozos Notes with iPhone Notes via IMAP",
		IconPath:     "SystemAO/notes_sync/img/icon.png",
		Group:        "Network",
		StartDir:     "SystemAO/notes_sync/index.html",
		RequireAdmin: true,
	})

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

	userRouter := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "System Setting",
		AdminOnly:   false,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})
	userRouter.HandleFunc("/system/imap_notes/token", imapNotesHandleToken)

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

	certFile := ""
	keyFile := ""
	if *use_tls {
		certFile = *tls_cert
		keyFile = *tls_key
	}

	systemWideLogger.PrintAndLog(imapNotesLogTag, fmt.Sprintf("Starting IMAP Notes server on port %d (TLS cert: %q)", port, certFile), nil)

	imapNotesServer = imapnotes.NewServer(imapnotes.Config{
		Port:        port,
		UserHandler: userHandler,
		AuthAgent:   authAgent,
		Database:    sysdb,
		Logger:      systemWideLogger,
		TLSCertFile: certFile,
		TLSKeyFile:  keyFile,
	})

	if err := imapNotesServer.Start(); err != nil {
		systemWideLogger.PrintAndLog(imapNotesLogTag, "Failed to start IMAP Notes server: "+err.Error(), err)
		imapNotesServer = nil
		return
	}
	tlsNote := "plain IMAP (SSL disabled on iPhone required)"
	if imapNotesServer.TLSEnabled {
		tlsNote = "IMAPS with TLS"
	}
	systemWideLogger.PrintAndLog(imapNotesLogTag,
		fmt.Sprintf("IMAP Notes server is now listening on port %d (%s)", port, tlsNote), nil)
}

// POST /system/imap_notes/enable   body: enabled=true&port=1143
// NOTE: uses r.FormValue (not utils.GetPara) because jQuery $.post sends
// parameters in the request body, not the URL query string.
func imapNotesHandleSetEnabled(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	enabledStr := r.FormValue("enabled")
	portStr := r.FormValue("port")

	enabled := enabledStr == "true"

	port := 1143
	if portStr != "" {
		if p, err := fmt.Sscanf(portStr, "%d", new(int)); p == 1 && err == nil {
			var pv int
			fmt.Sscanf(portStr, "%d", &pv)
			if pv > 0 && pv < 65536 {
				port = pv
			}
		}
	}

	systemWideLogger.PrintAndLog(imapNotesLogTag,
		fmt.Sprintf("Admin requested IMAP Notes server enabled=%v port=%d", enabled, port), nil)

	sysdb.Write("imapnotes", "enabled", enabled)
	sysdb.Write("imapnotes", "port", port)

	if enabled {
		if imapNotesServer != nil && imapNotesServer.Running {
			systemWideLogger.PrintAndLog(imapNotesLogTag, "Restarting IMAP Notes server due to settings change", nil)
			imapNotesServer.Stop()
			imapNotesServer = nil
		}
		startIMAPNotesServer()
		if imapNotesServer == nil {
			utils.SendErrorResponse(w, "Server failed to start — check system logs for details")
			return
		}
	} else {
		if imapNotesServer != nil {
			systemWideLogger.PrintAndLog(imapNotesLogTag, "Stopping IMAP Notes server (disabled by admin)", nil)
			imapNotesServer.Stop()
			imapNotesServer = nil
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
	tlsEnabled := imapNotesServer != nil && imapNotesServer.TLSEnabled

	type statusResp struct {
		Enabled    bool `json:"enabled"`
		Running    bool `json:"running"`
		Port       int  `json:"port"`
		TLSEnabled bool `json:"tlsEnabled"`
	}
	j, _ := json.Marshal(statusResp{
		Enabled:    enabled,
		Running:    running,
		Port:       port,
		TLSEnabled: tlsEnabled,
	})
	utils.SendJSONResponse(w, string(j))
}

// GET  /system/imap_notes/token          — list tokens + current username
// POST /system/imap_notes/token?action=generate
// POST /system/imap_notes/token?action=revoke&token=<t>
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
		tokens := authAgent.GetTokensFromUsername(userinfo.Username)
		type tokenEntry struct {
			Token string `json:"token"`
		}
		type listResp struct {
			Username string       `json:"username"`
			Tokens   []tokenEntry `json:"tokens"`
		}
		var list []tokenEntry
		for _, t := range tokens {
			list = append(list, tokenEntry{Token: t.Token})
		}
		if list == nil {
			list = []tokenEntry{}
		}
		j, _ := json.Marshal(listResp{Username: userinfo.Username, Tokens: list})
		utils.SendJSONResponse(w, string(j))
	}
}
