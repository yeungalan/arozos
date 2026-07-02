package main

/*
	notesimap_init.go - Notes IMAP sync server initialisation

	Wires the notesimap package into the system settings UI (Network group)
	and exposes:
	  - /system/notes/imap/status, /toggle, /port  (admin only)
	  - /api/notes/imap/credentials                (any logged-in user)

	Must be called after AuthInit() and UserSystemInit().
*/

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"imuslab.com/arozos/mod/info/logger"
	"imuslab.com/arozos/mod/notesimap"
	prout "imuslab.com/arozos/mod/prouter"
	"imuslab.com/arozos/mod/utils"
)

// NotesIMAPManager is the global Notes IMAP sync server manager.
var NotesIMAPManager *notesimap.Manager

// NotesIMAPInit initialises the Notes IMAP sync manager and registers its
// system setting page and API endpoints.
func NotesIMAPInit() {
	NotesIMAPManager = notesimap.NewManager(notesimap.ManagerOption{
		AuthAgent:   authAgent,
		UserHandler: userHandler,
		Sysdb:       sysdb,
		Logger:      systemWideLogger,
		TLSCertFile: *tls_cert,
		TLSKeyFile:  *tls_key,
	})

	registerSetting(settingModule{
		Name:         "Notes Sync",
		Desc:         "Sync the Notes app with Apple Notes over IMAP",
		IconPath:     "SystemAO/system_setting/img/network.svg",
		Group:        "Network",
		StartDir:     "SystemAO/network/notesimap.html",
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
	adminRouter.HandleFunc("/system/notes/imap/status", handleNotesIMAPStatus)
	adminRouter.HandleFunc("/system/notes/imap/toggle", handleNotesIMAPToggle)
	adminRouter.HandleFunc("/system/notes/imap/port", handleNotesIMAPSetPort)

	userRouter := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "Notes",
		AdminOnly:   false,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})
	userRouter.HandleFunc("/api/notes/imap/credentials", handleNotesIMAPCredentials)

	logger.PrintAndLog("NotesSync", "Notes IMAP sync service initialized", nil)
}

type notesIMAPStatus struct {
	Enabled bool `json:"enabled"`
	Port    int  `json:"port"`
	TLS     bool `json:"tls"`
}

func handleNotesIMAPStatus(w http.ResponseWriter, r *http.Request) {
	status := notesIMAPStatus{
		Enabled: NotesIMAPManager.IsRunning(),
		Port:    NotesIMAPManager.GetPort(),
		TLS:     NotesIMAPManager.UsesTLS(),
	}
	jsonString, _ := json.Marshal(status)
	utils.SendJSONResponse(w, string(jsonString))
}

func handleNotesIMAPToggle(w http.ResponseWriter, r *http.Request) {
	enable, err := utils.PostPara(r, "enable")
	if err != nil {
		utils.SendErrorResponse(w, "undefined enable state")
		return
	}

	var opErr error
	if enable == "true" {
		opErr = NotesIMAPManager.Start()
	} else {
		opErr = NotesIMAPManager.Stop()
	}
	if opErr != nil {
		utils.SendErrorResponse(w, opErr.Error())
		return
	}
	utils.SendOK(w)
}

func handleNotesIMAPSetPort(w http.ResponseWriter, r *http.Request) {
	portStr, err := utils.PostPara(r, "port")
	if err != nil {
		utils.SendErrorResponse(w, "invalid port")
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		utils.SendErrorResponse(w, "invalid port")
		return
	}

	if err := NotesIMAPManager.SetPort(port); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	utils.SendOK(w)
}

type notesIMAPCredentials struct {
	Enabled    bool   `json:"enabled"`
	TLS        bool   `json:"tls"`
	ServerHost string `json:"serverHost"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Token      string `json:"token"`
}

// handleNotesIMAPCredentials returns the IMAP connection details and an
// auto-login token the caller can use as the IMAP password when adding
// ArozOS as an "Other" mail account in the Notes app on iOS/macOS.
func handleNotesIMAPCredentials(w http.ResponseWriter, r *http.Request) {
	username, err := authAgent.GetUserName(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "Unable to get username")
		return
	}

	existing := authAgent.GetTokensFromUsername(username)
	var token string
	if len(existing) > 0 {
		token = existing[0].Token
	} else {
		token = authAgent.NewAutologinToken(username)
	}

	host := r.Host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}

	creds := notesIMAPCredentials{
		Enabled:    NotesIMAPManager.IsRunning(),
		TLS:        NotesIMAPManager.UsesTLS(),
		ServerHost: host,
		Port:       NotesIMAPManager.GetPort(),
		Username:   username,
		Token:      token,
	}
	jsonString, _ := json.Marshal(creds)
	utils.SendJSONResponse(w, string(jsonString))
}
