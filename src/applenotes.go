package main

/*
	Apple Notes Sync for arozos Notes module.
	Provides bidirectional sync between a user's arozos Notes and Apple Notes via IMAP.
*/

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"

	applenotes "imuslab.com/arozos/mod/applenotes"
	"imuslab.com/arozos/mod/filesystem"
	prout "imuslab.com/arozos/mod/prouter"
	"imuslab.com/arozos/mod/utils"
)

const appleNotesDBTable = "applenotes"

func AppleNotesInit() {
	_ = sysdb.NewTable(appleNotesDBTable)

	router := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "Notes",
		AdminOnly:   false,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			errorHandlePermissionDenied(w, r)
		},
	})

	router.HandleFunc("/system/notes/apple/config", appleNotesHandleConfig)
	router.HandleFunc("/system/notes/apple/sync", appleNotesHandleSync)
	router.HandleFunc("/system/notes/apple/status", appleNotesHandleStatus)
}

// appleNotesHandleConfig handles GET (read) and POST (save) of IMAP configuration.
func appleNotesHandleConfig(w http.ResponseWriter, r *http.Request) {
	userinfo, err := userHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "user not found")
		return
	}

	if r.Method == http.MethodGet {
		cfg := applenotes.DefaultConfig()
		_ = sysdb.Read(appleNotesDBTable, userinfo.Username+"/config", &cfg)
		// Never return the password to the client.
		safe := cfg
		safe.Password = ""
		js, _ := json.Marshal(safe)
		utils.SendJSONResponse(w, string(js))
		return
	}

	if r.Method == http.MethodPost {
		email, err := utils.PostPara(r, "email")
		if err != nil || email == "" {
			utils.SendErrorResponse(w, "email is required")
			return
		}
		password, _ := utils.PostPara(r, "password")
		server, _ := utils.PostPara(r, "server")
		portStr, _ := utils.PostPara(r, "port")
		folder, _ := utils.PostPara(r, "folder")
		enabledStr, _ := utils.PostPara(r, "enabled")

		// Preserve the existing saved password when the client sends an empty one.
		existing := applenotes.DefaultConfig()
		_ = sysdb.Read(appleNotesDBTable, userinfo.Username+"/config", &existing)

		cfg := applenotes.SyncConfig{
			Email:    email,
			Password: existing.Password,
			Server:   applenotes.DefaultIMAPServer,
			Port:     applenotes.DefaultIMAPPort,
			Folder:   applenotes.DefaultIMAPFolder,
			Enabled:  enabledStr == "true",
		}
		if password != "" {
			cfg.Password = password
		}
		if server != "" {
			cfg.Server = server
		}
		if portStr != "" {
			if p, e := strconv.Atoi(portStr); e == nil && p > 0 {
				cfg.Port = p
			}
		}
		if folder != "" {
			cfg.Folder = folder
		}

		if err := sysdb.Write(appleNotesDBTable, userinfo.Username+"/config", cfg); err != nil {
			utils.SendErrorResponse(w, "failed to save config")
			return
		}
		utils.SendOK(w)
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// appleNotesHandleSync triggers an immediate bidirectional sync for the calling user.
func appleNotesHandleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userinfo, err := userHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "user not found")
		return
	}

	cfg := applenotes.DefaultConfig()
	if err := sysdb.Read(appleNotesDBTable, userinfo.Username+"/config", &cfg); err != nil {
		utils.SendErrorResponse(w, "Apple Notes sync not configured — please add credentials first")
		return
	}
	if cfg.Email == "" || cfg.Password == "" {
		utils.SendErrorResponse(w, "Apple Notes sync not configured: missing email or app-specific password")
		return
	}

	notesDir, err := appleNotesResolveDir(userinfo.Username)
	if err != nil {
		utils.SendErrorResponse(w, "failed to resolve notes directory: "+err.Error())
		return
	}

	var prevState *applenotes.SyncState
	var stored applenotes.SyncState
	if sysdb.Read(appleNotesDBTable, userinfo.Username+"/state", &stored) == nil {
		prevState = &stored
	}

	result, newState, _ := applenotes.PerformSync(cfg, notesDir, prevState)

	if newState != nil {
		_ = sysdb.Write(appleNotesDBTable, userinfo.Username+"/state", newState)
	}
	_ = sysdb.Write(appleNotesDBTable, userinfo.Username+"/status", result)

	js, _ := json.Marshal(result)
	utils.SendJSONResponse(w, string(js))
}

// appleNotesHandleStatus returns the result of the most recent sync run.
func appleNotesHandleStatus(w http.ResponseWriter, r *http.Request) {
	userinfo, err := userHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "user not found")
		return
	}

	var status applenotes.SyncResult
	if sysdb.Read(appleNotesDBTable, userinfo.Username+"/status", &status) != nil {
		utils.SendJSONResponse(w, `{"success":false,"message":"No sync has been performed yet."}`)
		return
	}
	js, _ := json.Marshal(status)
	utils.SendJSONResponse(w, string(js))
}

// appleNotesResolveDir resolves "user:/Document/Notes" to an absolute OS path.
func appleNotesResolveDir(username string) (string, error) {
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
	return filepath.Clean(realPath), nil
}
