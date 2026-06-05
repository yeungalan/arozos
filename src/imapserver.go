package main

/*
	IMAP server for Apple Notes sync.
	Exposes arozos Notes as an IMAP4rev1 mailbox so Apple Notes (iOS/macOS) and
	any other IMAP client can connect and sync notes directly.

	Setup on Apple device:
	  Settings > Mail > Accounts > Add Account > Other > Add Mail Account
	  Host: <arozos IP>, Port: 1143 (plain) or 1993 (TLS), Username/Password: arozos credentials
	  Then in Notes app > Accounts, enable the new account.
*/

import (
	"encoding/json"
	"net/http"
	"strconv"

	"imuslab.com/arozos/mod/filesystem"
	imapsrv "imuslab.com/arozos/mod/imapserver"
	prout "imuslab.com/arozos/mod/prouter"
	"imuslab.com/arozos/mod/utils"
)

const imapDBTable = "imapserver"

var activeIMAPServer *imapsrv.Server

func IMAPServerInit() {
	_ = sysdb.NewTable(imapDBTable)

	// Load saved config and start the server if enabled.
	cfg := imapsrv.DefaultConfig()
	_ = sysdb.Read(imapDBTable, "config", &cfg)
	imapStartServer(cfg)

	// HTTP settings API — restricted to Notes module users.
	router := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "Notes",
		AdminOnly:   false,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			errorHandlePermissionDenied(w, r)
		},
	})
	router.HandleFunc("/system/notes/imap/config", imapHandleConfig)
	router.HandleFunc("/system/notes/imap/start", imapHandleStart)
	router.HandleFunc("/system/notes/imap/stop", imapHandleStop)
}

// imapHandleConfig returns (GET) or saves (POST) the IMAP server configuration.
func imapHandleConfig(w http.ResponseWriter, r *http.Request) {
	_, err := userHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "user not found")
		return
	}

	if r.Method == http.MethodGet {
		cfg := imapsrv.DefaultConfig()
		_ = sysdb.Read(imapDBTable, "config", &cfg)
		js, _ := json.Marshal(cfg)
		utils.SendJSONResponse(w, string(js))
		return
	}

	if r.Method == http.MethodPost {
		cfg := imapsrv.DefaultConfig()
		_ = sysdb.Read(imapDBTable, "config", &cfg)

		if v, _ := utils.PostPara(r, "port"); v != "" {
			if p, e := strconv.Atoi(v); e == nil && p > 0 {
				cfg.Port = p
			}
		}
		if v, _ := utils.PostPara(r, "tlsPort"); v != "" {
			if p, e := strconv.Atoi(v); e == nil && p > 0 {
				cfg.TLSPort = p
			}
		}
		if v, _ := utils.PostPara(r, "useTLS"); v != "" {
			cfg.UseTLS = v == "true"
		}
		if v, _ := utils.PostPara(r, "certFile"); v != "" {
			cfg.CertFile = v
		}
		if v, _ := utils.PostPara(r, "keyFile"); v != "" {
			cfg.KeyFile = v
		}
		if v, _ := utils.PostPara(r, "enabled"); v != "" {
			cfg.Enabled = v == "true"
		}

		if err := sysdb.Write(imapDBTable, "config", cfg); err != nil {
			utils.SendErrorResponse(w, "failed to save config")
			return
		}

		// Restart server with new config.
		activeIMAPServer.Close()
		imapStartServer(cfg)
		utils.SendOK(w)
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// imapHandleStart starts (or restarts) the IMAP server.
func imapHandleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := imapsrv.DefaultConfig()
	_ = sysdb.Read(imapDBTable, "config", &cfg)
	cfg.Enabled = true
	_ = sysdb.Write(imapDBTable, "config", cfg)
	activeIMAPServer.Close()
	imapStartServer(cfg)
	utils.SendOK(w)
}

// imapHandleStop stops the running IMAP server.
func imapHandleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	activeIMAPServer.Close()
	activeIMAPServer = nil

	cfg := imapsrv.DefaultConfig()
	_ = sysdb.Read(imapDBTable, "config", &cfg)
	cfg.Enabled = false
	_ = sysdb.Write(imapDBTable, "config", cfg)
	utils.SendOK(w)
}

// imapStartServer starts the IMAP server with the given config.
func imapStartServer(cfg imapsrv.Config) {
	srv, err := imapsrv.Start(cfg, imapAuthenticate, imapResolveNotesDir, "system")
	if err != nil {
		systemWideLogger.PrintAndLog("IMAP", "Failed to start IMAP server: "+err.Error(), err)
		return
	}
	activeIMAPServer = srv
	if srv != nil {
		systemWideLogger.PrintAndLog("IMAP", "IMAP server started", nil)
	}
}

// imapAuthenticate validates credentials against the arozos user system.
func imapAuthenticate(username, password string) bool {
	return authAgent.ValidateUsernameAndPassword(username, password)
}

// imapResolveNotesDir maps "user:/Document/Notes" to a real filesystem path.
func imapResolveNotesDir(username string) (string, error) {
	const vpath = "user:/Document/Notes"
	vrootID, _, err := filesystem.GetIDFromVirtualPath(vpath)
	if err != nil {
		return "", err
	}
	fsh, err := GetFsHandlerByUUID(vrootID)
	if err != nil {
		return "", err
	}
	realPath, err := fsh.FileSystemAbstraction.VirtualPathToRealPath(vpath, username)
	if err != nil {
		return "", err
	}
	return realPath, nil
}
