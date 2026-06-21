package main

/*
	Collaborative Document Endpoints (NotionEditor backend)
	author: tobychui

	Wires the transport-agnostic room engine in mod/collab onto authenticated
	WebSocket + HTTP endpoints so the NotionEditor webapp can offer real-time
	multi-user editing and live presence.

	All endpoints require login and access to the "NotionEditor" module.

	  GET  /api/collab/ws?doc=<docID>   --> WebSocket upgrade (edit relay + presence)
	  GET  /api/collab/peers?doc=<docID> --> {"count":n,"members":[...]} (REST fallback)

	Wire protocol (JSON text frames, {"type": ...})
	───────────────────────────────────────────────
	  client -> server
	    {type:"op", op:{...}}              relayed verbatim to every other member
	    {type:"snapshot", data, rev}       stored as the room's latest snapshot
	    {type:"presence", cursor}          updates this member's caret block id
	    {type:"requestSnapshot"}           asks for the room's stored snapshot
	  server -> client
	    {type:"welcome", id, color, members, snapshot, snapshotRev, hasSnapshot}
	    {type:"members", members}          on every join / leave / presence change
	    {type:"op", op}                    a relayed edit from another member
	    {type:"snapshot", snapshot, snapshotRev, hasSnapshot}
*/

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"imuslab.com/arozos/mod/collab"
	prout "imuslab.com/arozos/mod/prouter"
	"imuslab.com/arozos/mod/utils"
)

const (
	collabModuleName  = "NotionEditor"
	collabMaxDocIDLen = 256              // reject absurdly long room ids
	collabIdleTimeout = 10 * time.Minute // drop empty rooms after this long
	collabSweepEvery  = 30 * time.Second // how often the cleanup goroutine runs
	collabSendBuffer  = 128              // per-client outbound frame buffer
	collabIdleConnTTL = 5 * time.Minute  // close a socket after this much read idle
)

// collabManager owns every live document room for the lifetime of the process.
var collabManager = collab.NewManager()

var collabUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// collabInbound is the subset of fields parsed from every client frame. The op
// payload is kept raw so it can be relayed without re-serialising.
type collabInbound struct {
	Type   string          `json:"type"`
	Cursor string          `json:"cursor"`
	Data   string          `json:"data"`
	Rev    int64           `json:"rev"`
	Op     json.RawMessage `json:"op"`
}

func CollabInit() {
	// Background sweep: reclaim rooms that are empty and idle.
	go func() {
		ticker := time.NewTicker(collabSweepEvery)
		defer ticker.Stop()
		for range ticker.C {
			collabManager.Sweep(collabIdleTimeout)
		}
	}()

	router := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  collabModuleName,
		AdminOnly:   false,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			errorHandlePermissionDenied(w, r)
		},
	})

	// REST fallback: report who is currently in a room.
	router.HandleFunc("/api/collab/peers", func(w http.ResponseWriter, r *http.Request) {
		docID, err := utils.GetPara(r, "doc")
		if err != nil || docID == "" || len(docID) > collabMaxDocIDLen {
			utils.SendErrorResponse(w, "Invalid doc id")
			return
		}
		members := []collab.Member{}
		if room, ok := collabManager.Lookup(docID); ok {
			members = room.Members()
		}
		js, err := json.Marshal(map[string]interface{}{
			"count":   len(members),
			"members": members,
		})
		if err != nil {
			utils.SendErrorResponse(w, "Failed to encode members")
			return
		}
		utils.SendJSONResponse(w, string(js))
	})

	// WebSocket relay + presence.
	router.HandleFunc("/api/collab/ws", collabHandleWS)

	systemWideLogger.PrintAndLog("NotionEditor", "Collaborative document service started", nil)
}

// collabHandleWS upgrades the connection and runs one participant's read loop.
func collabHandleWS(w http.ResponseWriter, r *http.Request) {
	docID, err := utils.GetPara(r, "doc")
	if err != nil || docID == "" || len(docID) > collabMaxDocIDLen {
		http.Error(w, "Invalid doc id", http.StatusBadRequest)
		return
	}

	// Identity comes from the authenticated session, never from the client, so a
	// participant cannot spoof another user's presence name.
	userinfo, err := userHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		http.Error(w, "Not logged in", http.StatusUnauthorized)
		return
	}

	conn, err := collabUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	room := collabManager.Room(docID)
	client := collab.NewClient(collab.RandomID(), userinfo.Username, collabManager.NextColor(), collabSendBuffer)
	room.Join(client)

	// Writer goroutine: drains the client's outbound queue to the socket.
	go func() {
		defer conn.Close()
		for msg := range client.Outbound() {
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}()

	// Greet the new participant with their identity, the current roster and the
	// latest stored snapshot (if any) so they can sync immediately.
	snapshot, rev := room.Snapshot()
	welcome, _ := json.Marshal(map[string]interface{}{
		"type":        "welcome",
		"id":          client.ID,
		"color":       client.Color,
		"members":     room.Members(),
		"snapshot":    snapshot,
		"snapshotRev": rev,
		"hasSnapshot": rev > 0,
	})
	client.Send(welcome)

	// Tell everyone else the roster changed (the joiner already has the roster
	// from its welcome frame).
	collabBroadcastMembers(room, client)

	// Reader loop.
	defer func() {
		room.Leave(client)
		client.Close()
		collabBroadcastMembers(room, client)
	}()

	for {
		conn.SetReadDeadline(time.Now().Add(collabIdleConnTTL))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var in collabInbound
		if json.Unmarshal(msg, &in) != nil {
			continue
		}

		switch in.Type {
		case "op":
			// Relay the raw frame untouched so peers apply the edit as-is.
			room.Broadcast(msg, client)
		case "snapshot":
			room.SetSnapshot(in.Data, in.Rev)
		case "presence":
			room.SetCursor(client, in.Cursor)
			collabBroadcastMembers(room, nil)
		case "requestSnapshot":
			data, srev := room.Snapshot()
			resp, _ := json.Marshal(map[string]interface{}{
				"type":        "snapshot",
				"snapshot":    data,
				"snapshotRev": srev,
				"hasSnapshot": srev > 0,
			})
			client.Send(resp)
		}
	}
}

// collabBroadcastMembers sends the current roster to every member except exclude.
func collabBroadcastMembers(room *collab.Room, exclude *collab.Client) {
	msg, err := json.Marshal(map[string]interface{}{
		"type":    "members",
		"members": room.Members(),
	})
	if err != nil {
		return
	}
	room.Broadcast(msg, exclude)
}
