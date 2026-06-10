package main

import (
	"net/http"

	sync "imuslab.com/arozos/mod/applenotessync"
	prout "imuslab.com/arozos/mod/prouter"
	"imuslab.com/arozos/mod/utils"
)

/*
	Apple Notes Sync

	Runs an IMAP server that exposes each user's Notes app data as the
	"Notes" mailbox, so Apple devices can sync against arozos directly.
	See mod/applenotessync for the implementation.
*/

func AppleNotesSyncInit() {
	handler := sync.NewHandler(sync.Options{
		UserHandler: userHandler,
		AuthAgent:   authAgent,
		Database:    sysdb,
		Logger:      systemWideLogger,
	})

	//Status endpoint: any authenticated user (shows connection instructions)
	router := prout.NewModuleRouter(prout.RouterOption{
		AdminOnly:   false,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})
	router.HandleFunc("/api/notes/apple/status", handler.HandleStatus)

	//Config endpoint: admin only (starts / stops the system-wide IMAP listener)
	adminRouter := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "System Setting",
		AdminOnly:   true,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})
	adminRouter.HandleFunc("/api/notes/apple/config", handler.HandleConfig)
}
