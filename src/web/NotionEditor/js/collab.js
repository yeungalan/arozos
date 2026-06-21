/*
    NotionEditor - Collaboration client

    Connects to the core collaboration hub (/api/collab/ws) and bridges it to the
    block editor: relays local operations, applies remote ones, syncs the room
    snapshot and reports presence. Reconnects automatically with backoff.

    The "primary" member (oldest in the room, decided by the server) is the single
    client responsible for pushing snapshots to the hub and persisting to disk, so
    every edit is captured exactly once regardless of how many people are editing.
*/
(function (global) {
    "use strict";

    function NECollab(editor, opts) {
        this.editor = editor;
        this.opts = opts || {};
        this.docId = opts.docId;
        this.aoRoot = opts.aoRoot || "";
        this.ws = null;
        this.selfId = null;
        this.color = null;
        this.primary = false;
        this.destroyed = false;
        this.backoff = 1000;
        this.members = [];
        this._sync = global.NEUtil.debounce(this._doSync.bind(this), 600);
        this._keepalive = null;
    }

    NECollab.prototype._wsURL = function () {
        var u = new URL(this.aoRoot + "api/collab/ws", window.location.href);
        u.protocol = (u.protocol === "https:") ? "wss:" : "ws:";
        u.search = "?doc=" + encodeURIComponent(this.docId);
        return u.toString();
    };

    NECollab.prototype.connect = function () {
        if (this.destroyed) return;
        this._setStatus("connecting");
        var self = this;
        var ws;
        try {
            ws = new WebSocket(this._wsURL());
        } catch (e) {
            this._scheduleReconnect();
            return;
        }
        this.ws = ws;

        ws.onopen = function () {
            self.backoff = 1000;
            self._setStatus("connected");
            self._startKeepalive();
        };
        ws.onmessage = function (ev) { self._onMessage(ev.data); };
        ws.onclose = function () {
            self._stopKeepalive();
            self._setStatus("offline");
            self._scheduleReconnect();
        };
        ws.onerror = function () { /* close handler will deal with it */ };
    };

    NECollab.prototype._scheduleReconnect = function () {
        if (this.destroyed) return;
        var self = this;
        setTimeout(function () { self.connect(); }, this.backoff);
        this.backoff = Math.min(this.backoff * 2, 15000);
    };

    NECollab.prototype._startKeepalive = function () {
        var self = this;
        this._stopKeepalive();
        // Periodic frame keeps the server read deadline from expiring.
        this._keepalive = setInterval(function () { self._send({ type: "ping" }); }, 45000);
    };
    NECollab.prototype._stopKeepalive = function () {
        if (this._keepalive) { clearInterval(this._keepalive); this._keepalive = null; }
    };

    NECollab.prototype._onMessage = function (raw) {
        var msg;
        try { msg = JSON.parse(raw); } catch (e) { return; }

        if (msg.type === "welcome") {
            this.selfId = msg.id;
            this.color = msg.color;
            if (msg.hasSnapshot && msg.snapshot) {
                this._loadSnapshot(msg.snapshot);
            } else {
                // Cold room: seed the hub with what we loaded from disk. This only
                // primes the in-memory snapshot for late joiners; it must NOT
                // trigger a disk write (that is the user's / edits' job).
                this._seedSnapshot();
            }
            this._setMembers(msg.members || []);
        } else if (msg.type === "members") {
            this._setMembers(msg.members || []);
        } else if (msg.type === "op") {
            if (msg.op) {
                this.editor.applyRemoteOp(msg.op);
                // Primary persists edits that originated elsewhere too.
                if (this.primary) this._sync();
            }
        } else if (msg.type === "snapshot") {
            if (msg.hasSnapshot && msg.snapshot) this._loadSnapshot(msg.snapshot);
        }
    };

    NECollab.prototype._loadSnapshot = function (data) {
        try {
            var blocks = JSON.parse(data);
            if (blocks && blocks.length) this.editor.setBlocks(blocks);
        } catch (e) { /* ignore malformed snapshot */ }
    };

    NECollab.prototype._setMembers = function (members) {
        this.members = members;
        // Recompute primary flag for this client.
        var wasPrimary = this.primary;
        this.primary = false;
        for (var i = 0; i < members.length; i++) {
            if (members[i].id === this.selfId) { this.primary = !!members[i].primary; }
        }
        if (this.primary && !wasPrimary) {
            // Just inherited persistence duty: push current state.
            this._sync();
        }
        this.editor.setRemotePresence(members, this.selfId);
        if (this.opts.onMembers) this.opts.onMembers(members, this.selfId);
    };

    // ---- outbound --------------------------------------------------------------
    NECollab.prototype._send = function (obj) {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            try { this.ws.send(JSON.stringify(obj)); return true; } catch (e) { }
        }
        return false;
    };

    // Called by the editor for every local operation.
    NECollab.prototype.sendOp = function (op) {
        this._send({ type: "op", op: op });
        this._sync();
    };

    NECollab.prototype.sendPresence = function (blockId) {
        this.lastCursor = blockId || "";
        this._send({ type: "presence", cursor: this.lastCursor });
    };

    // Seed the hub snapshot without asking the host to persist. Used once when we
    // are the first client in a cold room.
    NECollab.prototype._seedSnapshot = function () {
        this._send({ type: "snapshot", data: JSON.stringify(this.editor.getBlocks()), rev: Date.now() });
    };

    // Debounced edit sync: only the primary pushes the snapshot to the hub and
    // signals the host to persist. Non-primary connected clients do nothing here;
    // the primary captures their edits because it received and applied the ops.
    NECollab.prototype._doSync = function () {
        if (!this.primary) return;
        this._send({ type: "snapshot", data: JSON.stringify(this.editor.getBlocks()), rev: Date.now() });
        if (this.opts.onLocalChange) this.opts.onLocalChange(true);
    };

    NECollab.prototype.isPrimary = function () { return this.primary; };
    NECollab.prototype.isConnected = function () {
        return this.ws && this.ws.readyState === WebSocket.OPEN;
    };

    NECollab.prototype._setStatus = function (state) {
        if (this.opts.onStatus) this.opts.onStatus(state);
    };

    NECollab.prototype.destroy = function () {
        this.destroyed = true;
        this._stopKeepalive();
        if (this.ws) { try { this.ws.close(); } catch (e) { } }
    };

    global.NECollab = NECollab;
})(window);
