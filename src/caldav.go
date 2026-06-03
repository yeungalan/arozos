package main

/*
	CalDAV Integration for ArozOS

	Mounts the CalDAV server at /caldav/ and /.well-known/caldav.
	Admin can enable/disable via System Setting → Network → CalDAV Notes Sync.
*/

import (
	"net/http"

	prout "imuslab.com/arozos/mod/prouter"
	"imuslab.com/arozos/mod/utils"

	"imuslab.com/arozos/mod/caldav"
)

var CalDAVManager *caldav.Manager

func CalDAVInit() {
	CalDAVManager = caldav.NewManager(userHandler, sysdb)

	// Admin-only router for toggle/status API.
	adminRouter := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "System Setting",
		AdminOnly:   true,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})

	adminRouter.HandleFunc("/system/caldav/toggle", CalDAVManager.HandleToggle)
	adminRouter.HandleFunc("/system/caldav/status", CalDAVManager.HandleStatus)

	// Register in System Setting → Network.
	registerSetting(settingModule{
		Name:         "CalDAV Notes Sync",
		Desc:         "Sync Notes with CalDAV clients (e.g. iPhone Reminders)",
		IconPath:     "img/system/network.svg",
		Group:        "Network",
		StartDir:     "SystemAO/caldav/index.html",
		RequireAdmin: true,
	})
}
