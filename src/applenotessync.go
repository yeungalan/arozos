package main

import (
	"net/http"

	prout "imuslab.com/arozos/mod/prouter"
	sync "imuslab.com/arozos/mod/applenotessync"
	"imuslab.com/arozos/mod/utils"
)

func AppleNotesSyncInit() {
	handler := sync.NewHandler(sync.Options{
		UserHandler: userHandler,
		Database:    sysdb,
		Logger:      systemWideLogger,
	})

	router := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "Notes",
		AdminOnly:   false,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})

	router.HandleFunc("/api/notes/apple/config", handler.HandleConfig)
	router.HandleFunc("/api/notes/apple/sync", handler.HandleSync)
	router.HandleFunc("/api/notes/apple/status", handler.HandleStatus)
}
