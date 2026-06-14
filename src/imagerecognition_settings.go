package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	prout "imuslab.com/arozos/mod/prouter"
	"imuslab.com/arozos/mod/utils"
)

/*
	Photo Recognition Settings Manager

	Registers the "Photo Recognition" tab in System Settings > AI Integration
	and wires the admin endpoints that configure the connection to the
	"imagerecognition" subservice.

	The capability is powered by an external subservice (see
	subservices/imagerecognition). When that subservice is installed and
	running, this manager auto-detects it and enables AGI access through the
	"imagerecognition" library — no manual configuration required. An admin may
	still point the system at a recognizer running elsewhere.

	  GET  /system/imagerecognition/status   – installed / reachable / config
	  GET  /system/imagerecognition/config   – stored connection config
	  POST /system/imagerecognition/config   – manual override / re-enable auto
	  POST /system/imagerecognition/test     – probe the configured endpoint

	All endpoints require administrator privileges.
*/

// imageRecognitionSubserviceEndpoint is the reverse-proxy endpoint (derived
// from the subservice's StartDir) used to recognise it among running services.
const imageRecognitionSubserviceEndpoint = "imagerecognition"

func ImageRecognitionSettingInit() {
	//Register the settings tab in the "AI Integration" group.
	registerSetting(settingModule{
		Name:         "Photo Recognition",
		Desc:         "AI image tagging and face recognition, powered by the imagerecognition subservice",
		IconPath:     "SystemAO/system_setting/img/ai.svg",
		Group:        "AInteg",
		StartDir:     "SystemAO/advance/photorecognition.html",
		RequireAdmin: true,
	})

	//Detect an installed subservice at startup and enable AGI access for it.
	endpoint, installed := detectImageRecognitionSubservice()
	if AGIGateway != nil {
		AGIGateway.ApplyDetectedImageRecognitionEndpoint(endpoint, installed)
	}

	adminRouter := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "System Settings",
		AdminOnly:   true,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})

	adminRouter.HandleFunc("/system/imagerecognition/status", HandleImageRecognitionStatus)
	adminRouter.HandleFunc("/system/imagerecognition/config", AGIGateway.HandleImageRecognitionConfig)
	adminRouter.HandleFunc("/system/imagerecognition/test", AGIGateway.HandleImageRecognitionTest)
}

// detectImageRecognitionSubservice scans the running subservices for the image
// recognition service and returns its direct base URL plus whether it is
// installed/running.
func detectImageRecognitionSubservice() (string, bool) {
	if ssRouter == nil {
		return "", false
	}
	for _, ss := range ssRouter.RunningSubService {
		if ss.RpEndpoint == imageRecognitionSubserviceEndpoint || ss.Info.Name == "Image Recognition" {
			return fmt.Sprintf("http://localhost:%d", ss.Port), true
		}
	}
	return "", false
}

// HandleImageRecognitionStatus reports whether the subservice is installed and
// reachable, refreshing the auto-detected endpoint on the way.
func HandleImageRecognitionStatus(w http.ResponseWriter, r *http.Request) {
	endpoint, installed := detectImageRecognitionSubservice()
	if AGIGateway != nil {
		AGIGateway.ApplyDetectedImageRecognitionEndpoint(endpoint, installed)
	}

	cfg := AGIGateway.GetImageRecognitionConfig()

	//Live reachability probe so the UI can show a green/red status.
	reachable := false
	var serviceInfo map[string]interface{}
	if strings.TrimSpace(cfg.Endpoint) != "" {
		client := &http.Client{Timeout: 3 * time.Second}
		if resp, err := client.Get(strings.TrimRight(cfg.Endpoint, "/") + "/api/info"); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				reachable = true
				_ = json.NewDecoder(resp.Body).Decode(&serviceInfo)
			}
		}
	}

	js, _ := json.Marshal(map[string]interface{}{
		"installed":   installed,
		"endpoint":    cfg.Endpoint,
		"enabled":     cfg.Enabled,
		"manual":      cfg.Manual,
		"reachable":   reachable,
		"serviceInfo": serviceInfo,
	})
	utils.SendJSONResponse(w, string(js))
}
