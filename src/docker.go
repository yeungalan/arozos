package main

/*
	Docker Management Module

	Provides admin-only API endpoints for managing Docker images and containers
	through the system settings UI.  All operations run via the Docker CLI using
	exec.Command with explicit argument slices – no shell interpolation is used,
	preventing command-injection attacks.

	All endpoints are guarded by the admin-only ProRouter so only administrator
	accounts can reach them.
*/

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	prout "imuslab.com/arozos/mod/prouter"
	"imuslab.com/arozos/mod/utils"
)

// validImageRef accepts Docker image references such as:
//
//	nginx, nginx:latest, registry.example.com:5000/ns/img:tag@sha256:…
//
// It intentionally rejects anything containing shell-special characters.
var validImageRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._\-/:@]*$`)

// validContainerRef accepts container short/long IDs and human-readable names.
var validContainerRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.\-]*$`)

// validPositiveInt accepts a bare decimal integer string used for the log-lines parameter.
var validPositiveInt = regexp.MustCompile(`^[1-9][0-9]*$`)

// dockerImageRow is what we return to the UI for each image.
type dockerImageRow struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Size       string `json:"size"`
	CreatedAt  string `json:"createdAt"`
}

// dockerContainerRow is what we return to the UI for each container.
type dockerContainerRow struct {
	ID         string `json:"id"`
	Names      string `json:"names"`
	Image      string `json:"image"`
	Status     string `json:"status"`
	State      string `json:"state"`
	Ports      string `json:"ports"`
	RunningFor string `json:"runningFor"`
}

// dockerInfoRow is what we return for the Docker daemon summary.
type dockerInfoRow struct {
	ServerVersion     string `json:"serverVersion"`
	StorageDriver     string `json:"storageDriver"`
	ContainersRunning int    `json:"containersRunning"`
	ContainersStopped int    `json:"containersStopped"`
	Images            int    `json:"images"`
	OSType            string `json:"osType"`
	Architecture      string `json:"architecture"`
}

// DockerInit registers all Docker-management API routes and the settings entry.
// It must be called after userHandler is initialised.
func DockerInit() {
	// Every route requires the caller to be a logged-in administrator.
	adminRouter := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "System Setting",
		AdminOnly:   true,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission Denied")
		},
	})

	adminRouter.HandleFunc("/system/docker/available", docker_checkAvailable)
	adminRouter.HandleFunc("/system/docker/info", docker_getInfo)
	adminRouter.HandleFunc("/system/docker/images/list", docker_listImages)
	adminRouter.HandleFunc("/system/docker/images/pull", docker_pullImage)
	adminRouter.HandleFunc("/system/docker/images/remove", docker_removeImage)
	adminRouter.HandleFunc("/system/docker/containers/list", docker_listContainers)
	adminRouter.HandleFunc("/system/docker/containers/start", docker_startContainer)
	adminRouter.HandleFunc("/system/docker/containers/stop", docker_stopContainer)
	adminRouter.HandleFunc("/system/docker/containers/restart", docker_restartContainer)
	adminRouter.HandleFunc("/system/docker/containers/remove", docker_removeContainer)
	adminRouter.HandleFunc("/system/docker/containers/logs", docker_containerLogs)

	registerSetting(settingModule{
		Name:         "Docker Manager",
		Desc:         "Manage Docker images and containers",
		IconPath:     "SystemAO/docker/img/icon.svg",
		Group:        "Advance",
		StartDir:     "SystemAO/docker/index.html",
		RequireAdmin: true,
	})
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// requireMethod returns false and writes an error if the method doesn't match.
func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		utils.SendErrorResponse(w, "Method not allowed")
		return false
	}
	return true
}

// runDockerCmd executes docker with the supplied arguments and returns
// combined stdout+stderr output.  A 30-second timeout protects quick
// operations; long-running ones (pull) set their own context.
func runDockerCmd(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

// ── API handlers ─────────────────────────────────────────────────────────────

func docker_checkAvailable(w http.ResponseWriter, r *http.Request) {
	_, err := exec.LookPath("docker")
	if err != nil {
		utils.SendJSONResponse(w, `{"available":false,"error":"docker binary not found in PATH"}`)
		return
	}
	out, err := runDockerCmd(10*time.Second, "info", "--format", "{{.ServerVersion}}")
	if err != nil {
		utils.SendJSONResponse(w, `{"available":false,"error":"Docker daemon is not running"}`)
		return
	}
	version := strings.TrimSpace(string(out))
	resp, _ := json.Marshal(map[string]interface{}{"available": true, "version": version})
	utils.SendJSONResponse(w, string(resp))
}

func docker_getInfo(w http.ResponseWriter, r *http.Request) {
	// docker info outputs a free-form text block; use --format to extract fields safely.
	fmtStr := `{"serverVersion":"{{.ServerVersion}}","storageDriver":"{{.Driver}}",` +
		`"containersRunning":{{.ContainersRunning}},"containersStopped":{{.ContainersStopped}},` +
		`"images":{{.Images}},"osType":"{{.OSType}}","architecture":"{{.Architecture}}"}`
	out, err := runDockerCmd(10*time.Second, "info", "--format", fmtStr)
	if err != nil {
		utils.SendErrorResponse(w, "Failed to get Docker info: "+err.Error())
		return
	}
	utils.SendJSONResponse(w, strings.TrimSpace(string(out)))
}

func docker_listImages(w http.ResponseWriter, r *http.Request) {
	// {{json .}} ensures all field values are properly JSON-escaped by Docker.
	out, err := runDockerCmd(15*time.Second, "images", "--format", "{{json .}}")
	if err != nil {
		utils.SendErrorResponse(w, "Failed to list images: "+err.Error())
		return
	}

	type rawImage struct {
		ID         string `json:"ID"`
		Repository string `json:"Repository"`
		Tag        string `json:"Tag"`
		Size       string `json:"Size"`
		CreatedAt  string `json:"CreatedAt"`
	}

	var rows []dockerImageRow
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw rawImage
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		rows = append(rows, dockerImageRow{
			ID:         raw.ID,
			Repository: raw.Repository,
			Tag:        raw.Tag,
			Size:       raw.Size,
			CreatedAt:  raw.CreatedAt,
		})
	}

	if rows == nil {
		rows = []dockerImageRow{}
	}
	resp, _ := json.Marshal(rows)
	utils.SendJSONResponse(w, string(resp))
}

func docker_pullImage(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, "POST") {
		return
	}
	imageName, err := utils.GetPara(r, "image")
	if err != nil || imageName == "" {
		utils.SendErrorResponse(w, "image parameter is required")
		return
	}
	if !validImageRef.MatchString(imageName) {
		utils.SendErrorResponse(w, "Invalid image name")
		return
	}

	// Allow up to 10 minutes for large images.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "pull", imageName).CombinedOutput()
	if err != nil {
		utils.SendErrorResponse(w, "Pull failed: "+strings.TrimSpace(string(out)))
		return
	}
	utils.SendOK(w)
}

func docker_removeImage(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, "POST") {
		return
	}
	imageID, err := utils.GetPara(r, "id")
	if err != nil || imageID == "" {
		utils.SendErrorResponse(w, "id parameter is required")
		return
	}
	if !validImageRef.MatchString(imageID) {
		utils.SendErrorResponse(w, "Invalid image identifier")
		return
	}

	out, err := runDockerCmd(30*time.Second, "rmi", imageID)
	if err != nil {
		utils.SendErrorResponse(w, "Remove failed: "+strings.TrimSpace(string(out)))
		return
	}
	utils.SendOK(w)
}

func docker_listContainers(w http.ResponseWriter, r *http.Request) {
	out, err := runDockerCmd(15*time.Second, "ps", "-a", "--format", "{{json .}}")
	if err != nil {
		utils.SendErrorResponse(w, "Failed to list containers: "+err.Error())
		return
	}

	type rawContainer struct {
		ID         string `json:"ID"`
		Names      string `json:"Names"`
		Image      string `json:"Image"`
		Status     string `json:"Status"`
		State      string `json:"State"`
		Ports      string `json:"Ports"`
		RunningFor string `json:"RunningFor"`
	}

	var rows []dockerContainerRow
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw rawContainer
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		rows = append(rows, dockerContainerRow{
			ID:         raw.ID,
			Names:      raw.Names,
			Image:      raw.Image,
			Status:     raw.Status,
			State:      raw.State,
			Ports:      raw.Ports,
			RunningFor: raw.RunningFor,
		})
	}

	if rows == nil {
		rows = []dockerContainerRow{}
	}
	resp, _ := json.Marshal(rows)
	utils.SendJSONResponse(w, string(resp))
}

func docker_startContainer(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, "POST") {
		return
	}
	id, err := getValidContainerID(r)
	if err != "" {
		utils.SendErrorResponse(w, err)
		return
	}
	out, cmdErr := runDockerCmd(30*time.Second, "start", id)
	if cmdErr != nil {
		utils.SendErrorResponse(w, "Start failed: "+strings.TrimSpace(string(out)))
		return
	}
	utils.SendOK(w)
}

func docker_stopContainer(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, "POST") {
		return
	}
	id, err := getValidContainerID(r)
	if err != "" {
		utils.SendErrorResponse(w, err)
		return
	}
	// Allow up to 30 s for a graceful stop before the daemon sends SIGKILL.
	out, cmdErr := runDockerCmd(40*time.Second, "stop", "--time", "30", id)
	if cmdErr != nil {
		utils.SendErrorResponse(w, "Stop failed: "+strings.TrimSpace(string(out)))
		return
	}
	utils.SendOK(w)
}

func docker_restartContainer(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, "POST") {
		return
	}
	id, err := getValidContainerID(r)
	if err != "" {
		utils.SendErrorResponse(w, err)
		return
	}
	out, cmdErr := runDockerCmd(50*time.Second, "restart", "--time", "30", id)
	if cmdErr != nil {
		utils.SendErrorResponse(w, "Restart failed: "+strings.TrimSpace(string(out)))
		return
	}
	utils.SendOK(w)
}

func docker_removeContainer(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, "POST") {
		return
	}
	id, err := getValidContainerID(r)
	if err != "" {
		utils.SendErrorResponse(w, err)
		return
	}

	// Only accept an explicit force=true query parameter; default is safe (no force).
	forceParam, _ := utils.GetPara(r, "force")
	args := []string{"rm"}
	if forceParam == "true" {
		args = append(args, "-f")
	}
	args = append(args, id)

	out, cmdErr := runDockerCmd(30*time.Second, args...)
	if cmdErr != nil {
		utils.SendErrorResponse(w, "Remove failed: "+strings.TrimSpace(string(out)))
		return
	}
	utils.SendOK(w)
}

func docker_containerLogs(w http.ResponseWriter, r *http.Request) {
	id, err := getValidContainerID(r)
	if err != "" {
		utils.SendErrorResponse(w, err)
		return
	}

	// Default to last 100 lines; accept a caller-supplied value in [1, 500].
	tailLines := "100"
	if lParam, pErr := utils.GetPara(r, "lines"); pErr == nil && validPositiveInt.MatchString(lParam) {
		if n, _ := strconv.Atoi(lParam); n >= 1 && n <= 500 {
			tailLines = lParam
		}
	}

	// docker logs exits 1 for a stopped container that has logs; use CombinedOutput.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "docker", "logs", "--tail", tailLines, "--timestamps", id).CombinedOutput()

	logLines := strings.Split(string(out), "\n")
	// Remove trailing empty line that Split produces.
	if len(logLines) > 0 && logLines[len(logLines)-1] == "" {
		logLines = logLines[:len(logLines)-1]
	}

	resp, _ := json.Marshal(map[string]interface{}{"logs": logLines})
	utils.SendJSONResponse(w, string(resp))
}

// getValidContainerID extracts and validates the "id" query parameter.
// Returns ("", "") on success, or ("", errMsg) on failure.
func getValidContainerID(r *http.Request) (string, string) {
	id, err := utils.GetPara(r, "id")
	if err != nil || id == "" {
		return "", "id parameter is required"
	}
	if !validContainerRef.MatchString(id) {
		return "", "Invalid container identifier"
	}
	return id, ""
}
