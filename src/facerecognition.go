package main

import (
	"net/http"

	"imuslab.com/arozos/mod/facerecognition"
	"imuslab.com/arozos/mod/info/logger"
	prout "imuslab.com/arozos/mod/prouter"
	"imuslab.com/arozos/mod/utils"
)

/*
	Face Recognition Service Manager

	Registers the optional, backend-only face recognition feature for the
	Photo app. The feature ships DISABLED and is switched on or off from the
	"Face Recognition" tab in System Settings > AI Integration. Detection
	and people grouping run entirely on this host (pure Go, no external
	models or services), and every endpoint below only exposes the data of
	the requesting user.

	Admin endpoints (System Settings > AI Integration > Face Recognition):
	  GET  /system/facerecognition/config     – current configuration
	  POST /system/facerecognition/config     – save configuration (on/off, engine, model)
	  POST /system/facerecognition/clearall   – wipe stored face data of all users
	  POST /system/facerecognition/modeltest  – validate the deep (ONNX) model

	User endpoints (used by the Photo web app, Photo module access required):
	  GET  /system/facerecognition/status         – feature switch + own statistics
	  POST /system/facerecognition/scan           – scan a batch of photos for faces
	  GET  /system/facerecognition/people         – list own people clusters
	  POST /system/facerecognition/people/rename  – rename one of my people
	  GET  /system/facerecognition/people/photos  – photos containing a person
	  GET  /system/facerecognition/photofaces     – faces stored for one photo
	  POST /system/facerecognition/clear          – wipe my own face data
*/

var faceRecognitionManager *facerecognition.Manager

func FaceRecognitionInit() {
	manager, err := facerecognition.NewManager(&facerecognition.Options{
		UserHandler: userHandler,
		Database:    sysdb,
	})
	if err != nil {
		logger.PrintAndLog("FaceRecognition", "Unable to start face recognition manager", err)
		return
	}
	faceRecognitionManager = manager

	//Register the settings tab in the "AI Integration" group
	registerSetting(settingModule{
		Name:         "Face Recognition",
		Desc:         "Optional on-device face detection and people grouping for the Photo app",
		IconPath:     "SystemAO/system_setting/img/ai.svg",
		Group:        "AInteg",
		StartDir:     "SystemAO/advance/facerecognition.html",
		RequireAdmin: true,
	})

	//Admin-only router: feature on/off switch and global data wipe
	adminRouter := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "System Settings",
		AdminOnly:   true,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})
	adminRouter.HandleFunc("/system/facerecognition/config", faceRecognitionManager.HandleConfig)
	adminRouter.HandleFunc("/system/facerecognition/clearall", faceRecognitionManager.HandleClearAll)
	adminRouter.HandleFunc("/system/facerecognition/modeltest", faceRecognitionManager.HandleModelTest)

	//User router: scanning and people browsing for the Photo app. Scoped to
	//the Photo module so only users that can use Photo can scan their files.
	photoRouter := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "Photo",
		AdminOnly:   false,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})
	photoRouter.HandleFunc("/system/facerecognition/status", faceRecognitionManager.HandleStatus)
	photoRouter.HandleFunc("/system/facerecognition/scan", faceRecognitionManager.HandleScan)
	photoRouter.HandleFunc("/system/facerecognition/people", faceRecognitionManager.HandlePeople)
	photoRouter.HandleFunc("/system/facerecognition/people/rename", faceRecognitionManager.HandleRenamePerson)
	photoRouter.HandleFunc("/system/facerecognition/people/photos", faceRecognitionManager.HandlePersonPhotos)
	photoRouter.HandleFunc("/system/facerecognition/photofaces", faceRecognitionManager.HandlePhotoFaces)
	photoRouter.HandleFunc("/system/facerecognition/clear", faceRecognitionManager.HandleClearUser)
}
