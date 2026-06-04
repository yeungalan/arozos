package main

import (
	"net/http"

	prout "imuslab.com/arozos/mod/prouter"
	"imuslab.com/arozos/mod/utils"
	caldav "imuslab.com/arozos/mod/caldav"
)

var CalDAVServer *caldav.Server

// CalDAVInit initialises the CalDAV server and registers its settings panel.
func CalDAVInit() {
	CalDAVServer = caldav.NewServer(
		"/caldav",
		*root_directory,
		authAgent,
		sysdb,
	)

	// Register the settings panel (admin only, under Network group)
	registerSetting(settingModule{
		Name:         "CalDAV Server",
		Desc:         "Sync Notes with iOS Reminders via CalDAV",
		IconPath:     "SystemAO/caldav/img/icon.png",
		Group:        "Network",
		StartDir:     "SystemAO/caldav/index.html",
		RequireAdmin: true,
	})

	// Admin-only API endpoints
	adminRouter := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "System Setting",
		AdminOnly:   true,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})

	adminRouter.HandleFunc("/system/caldav/status", handleCalDAVStatus)
	adminRouter.HandleFunc("/system/caldav/toggle", handleCalDAVToggle)
}

// handleCalDAVStatus returns current enabled state.
func handleCalDAVStatus(w http.ResponseWriter, r *http.Request) {
	if CalDAVServer == nil {
		utils.SendJSONResponse(w, `{"enabled":false}`)
		return
	}
	if CalDAVServer.Enabled {
		utils.SendJSONResponse(w, `{"enabled":true}`)
	} else {
		utils.SendJSONResponse(w, `{"enabled":false}`)
	}
}

// handleCalDAVToggle enables or disables the CalDAV server.
func handleCalDAVToggle(w http.ResponseWriter, r *http.Request) {
	if CalDAVServer == nil {
		utils.SendErrorResponse(w, "CalDAV server not initialised")
		return
	}
	enable, err := utils.PostPara(r, "enable")
	if err != nil {
		utils.SendErrorResponse(w, "Missing 'enable' parameter")
		return
	}
	CalDAVServer.SetEnabled(enable == "true")
	utils.SendOK(w)
}
