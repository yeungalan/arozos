package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
)

/*
	aroz.go

	Minimal, self-contained implementation of the ArozOS subservice startup
	contract (the "aroz" helper used by the official subservice example,
	https://github.com/aroz-online/ArozOS-Subservice-Example).

	A subservice is launched by ArozOS as:

	    ./imagerecognition_linux_amd64 -port :12810 -rpt http://localhost:8080/api/ajgi/interface

	and is probed for its module metadata with:

	    ./imagerecognition_linux_amd64 -info

	HandleFlagParse implements both behaviours: with -info it prints the
	ServiceInfo JSON and exits; otherwise it returns the listening port and
	the parent gateway endpoint.
*/

// ServiceInfo mirrors ArozOS modules.ModuleInfo. It is emitted verbatim as
// JSON when the host probes the binary with -info.
type ServiceInfo struct {
	Name         string
	Desc         string
	Group        string
	IconPath     string
	Version      string
	StartDir     string
	SupportFW    bool
	LaunchFWDir  string
	SupportEmb   bool
	LaunchEmb    string
	InitFWSize   []int
	InitEmbSize  []int
	SupportedExt []string
}

// ArozHandler holds the runtime parameters handed to the subservice by the
// host and provides helpers to talk back to the ArozOS gateway.
type ArozHandler struct {
	Port            string //Listening endpoint, e.g. ":12810"
	restfulEndpoint string //Parent AGI gateway interface URL
}

// HandleFlagParse parses the standard subservice flags. On -info it prints the
// supplied ServiceInfo as JSON and terminates the process.
func HandleFlagParse(info ServiceInfo) *ArozHandler {
	infoRequestMode := flag.Bool("info", false, "Show information about this subservice")
	port := flag.String("port", ":80", "The listening endpoint for this subservice")
	restful := flag.String("rpt", "http://localhost:8080/api/ajgi/interface", "The RESTful endpoint of the parent ArozOS host")
	flag.Parse()

	if *infoRequestMode {
		jsonString, _ := json.Marshal(info)
		fmt.Println(string(jsonString))
		os.Exit(0)
	}

	return &ArozHandler{
		Port:            *port,
		restfulEndpoint: *restful,
	}
}

// GetUserInfoFromRequest extracts the ArozOS user and auth token that the host
// injects into every proxied request.
func (a *ArozHandler) GetUserInfoFromRequest(w http.ResponseWriter, r *http.Request) (string, string) {
	username := r.Header.Get("aouser")
	token := r.Header.Get("aotoken")
	return username, token
}

// RequestGatewayInterface runs an AGI script on the parent host on behalf of
// the user identified by token.
func (a *ArozHandler) RequestGatewayInterface(token string, script string) (*http.Response, error) {
	return http.PostForm(a.restfulEndpoint, url.Values{"token": {token}, "script": {script}})
}
