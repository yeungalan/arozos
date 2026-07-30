/*
    Inventory - remote scanner pairing (front end)

    One Inventory page acts as the HOST (usually a desktop): it shows a pairing
    code and long-polls for barcodes. Another acts as the REMOTE (the FZ-N1 or
    any phone): it joins with that code and forwards every trigger pull instead
    of applying it locally.

    A forwarded barcode is handed to the host's normal scan handler, so the
    desktop's selected mode decides what happens to it - the handheld is purely
    an input device, exactly like a wedge plugged into the desktop itself.

    The remote's pairing survives a page reload: the session and device ids are
    kept in localStorage and replayed through pairJoin, which reuses the same
    device queue rather than creating a second entry in the host's device list.
*/

var InvPairing = (function () {

    var STORAGE_KEY = "arozos.inventory.pairing";

    // How long the host asks the server to hold an empty poll. Long enough that
    // an idle host makes ~5 requests a minute, short enough that a proxy or a
    // sleeping laptop never leaves the connection in limbo.
    var POLL_HOLD_MS = 12000;

    // Floor between empty polls. The server normally holds the request for
    // POLL_HOLD_MS, but a reverse proxy that buffers or shortens the response
    // would otherwise turn this into a tight request loop.
    var POLL_MIN_GAP_MS = 1000;

    // Handheld heartbeat. Keeps the desktop's "connected" indicator honest and
    // refreshes the host's mode, so the operator can see what the next trigger
    // pull will do before pulling it rather than after.
    var PING_EVERY_MS = 6000;

    /*
        Reconnection.

        A handheld walks out of Wi-Fi range, a desktop gets reloaded, a switch
        reboots. None of that should cost the operator their pairing or, worse, a
        scan. So: retry with a widening gap rather than a fixed one, re-establish
        the session automatically instead of dropping to the pairing screen, and
        hold scans taken while offline until they can be delivered.

        A transport failure is retried indefinitely - the server being
        unreachable is temporary and the pairing is still valid. A session the
        server actively says is gone is retried only for GRACE_MS, which is long
        enough to cover a desktop reload but short enough that a deliberate
        "stop pairing" tells the handheld reasonably soon.
    */
    var RECONNECT_MIN_MS = 1000;
    var RECONNECT_MAX_MS = 15000;
    var GRACE_MS = 90000;
    var OUTBOX_MAX = 200;

    var state = {
        role: "none",        // "none" | "host" | "remote"
        sessionId: "",
        deviceId: "",
        deviceName: "",
        code: "",
        devices: [],
        hostMode: "",
        hostStep: 1,
        after: 0,            // newest scan timestamp already delivered
        polling: false,
        stopped: true,
        online: false,          // last exchange with the server succeeded
        attempt: 0,             // consecutive reconnection attempts
        offlineSince: 0,        // when contact was lost, 0 when connected
        lastError: ""
    };

    var seenScanIds = {};    // guards the inclusive-timestamp window
    var pingTimer = null;
    var reconnectTimer = null;
    var outbox = [];         // scans taken while offline, oldest first
    var handlers = {};       // onScan / onDevices / onStatus / onError
    var apiCall = null;      // injected: function(script, payload, done, fail)

    function emit(name, arg) {
        if (handlers[name]) handlers[name](arg);
    }

    function status() {
        return {
            role: state.role,
            sessionId: state.sessionId,
            deviceId: state.deviceId,
            deviceName: state.deviceName,
            code: state.code,
            devices: state.devices,
            hostMode: state.hostMode,
            hostStep: state.hostStep,
            online: state.online,
            reconnecting: !state.online && state.role !== "none" && !state.stopped,
            queued: outbox.length,
            lastError: state.lastError
        };
    }

    function announce() {
        emit("onStatus", status());
    }

    /* ── Persistence, so a reload on either end resumes the pairing ─────── */

    function remember() {
        try {
            if (state.role === "none") {
                window.localStorage.removeItem(STORAGE_KEY);
                return;
            }
            window.localStorage.setItem(STORAGE_KEY, JSON.stringify({
                role: state.role,
                sessionId: state.sessionId,
                deviceId: state.deviceId,
                deviceName: state.deviceName,
                code: state.code,
                // Held scans survive a reload of the handheld too, so closing
                // the app by accident mid-outage does not lose them
                outbox: outbox
            }));
        } catch (e) {
            // Private mode or a storage quota - pairing just will not resume
        }
    }

    function recall() {
        try {
            var raw = window.localStorage.getItem(STORAGE_KEY);
            if (!raw) return null;
            var saved = JSON.parse(raw);
            return (saved && saved.role) ? saved : null;
        } catch (e) {
            return null;
        }
    }

    function stopPing() {
        if (pingTimer) {
            clearInterval(pingTimer);
            pingTimer = null;
        }
    }

    function startPing() {
        stopPing();
        pingTimer = setInterval(ping, PING_EVERY_MS);
    }

    function ping() {
        if (state.role !== "remote" || state.stopped) return;
        apiCall("pairPing.agi", {
            sessionId: state.sessionId,
            deviceId: state.deviceId
        }, function (data) {
            var changed = (state.hostMode !== data.hostMode) || (state.hostStep !== data.hostStep);
            state.hostMode = data.hostMode || state.hostMode;
            state.hostStep = data.hostStep || state.hostStep;
            goOnline();
            if (changed) announce();
        }, function (data) {
            if (endedByHost(data)) {
                giveUp("The desktop ended the session");
                return;
            }
            // The heartbeat is the first thing to notice an outage
            goOffline(data && data.expired
                ? "Reconnecting to the desktop..."
                : "Reconnecting to the server...", !!(data && data.network));
        });
    }

    /* ── Reconnection ───────────────────────────────────────────────────── */

    function backoffDelay() {
        var steps = state.attempt > 4 ? 4 : state.attempt;
        var delay = RECONNECT_MIN_MS * Math.pow(2, steps);
        return delay > RECONNECT_MAX_MS ? RECONNECT_MAX_MS : delay;
    }

    /*
        The desktop saying it ended the session is final - there is nothing to
        reconnect to, and continuing to hold scans would let an operator fill a
        queue that can never be delivered.
    */
    function endedByHost(data) {
        return !!(data && data.ended);
    }

    function giveUp(message) {
        emit("onError", message);
        reset();
    }

    function goOffline(reason, isNetwork) {
        if (state.online || state.offlineSince === 0) {
            state.offlineSince = new Date().getTime();
        }
        state.online = false;
        state.polling = false;
        state.lastError = reason || "Reconnecting...";

        // A session the server says is gone is only worth chasing for a while;
        // an unreachable server is worth chasing indefinitely
        if (!isNetwork && (new Date().getTime() - state.offlineSince) > GRACE_MS) {
            emit("onError", "The desktop ended the session - pair again");
            reset();
            return;
        }

        announce();
        scheduleReconnect();
    }

    function goOnline() {
        var wasOffline = !state.online;
        state.online = true;
        state.attempt = 0;
        state.offlineSince = 0;
        state.lastError = "";

        if (reconnectTimer) {
            clearTimeout(reconnectTimer);
            reconnectTimer = null;
        }
        announce();

        if (wasOffline) {
            emit("onReconnect", status());
            flushOutbox();
        }
    }

    function scheduleReconnect() {
        if (reconnectTimer || state.stopped || state.role === "none") return;

        var delay = backoffDelay();
        state.attempt++;
        reconnectTimer = setTimeout(function () {
            reconnectTimer = null;
            if (state.stopped) return;
            if (state.role === "host") reconnectHost();
            else if (state.role === "remote") reconnectRemote();
        }, delay);
    }

    /*
        The host re-establishes by asking for its session again. pairHost reuses
        the one already on disk, so a reload or an outage keeps the same code and
        any paired handheld never notices.
    */
    function reconnectHost() {
        apiCall("pairHost.agi", { reset: "false" }, function (data) {
            if (state.role !== "host" || state.stopped) return;

            var sameSession = (data.sessionId === state.sessionId);
            state.sessionId = data.sessionId;
            state.code = data.code;
            state.devices = data.devices || [];
            if (!sameSession) {
                // A brand new session: nothing from the old one is pending
                state.after = data.serverTime || new Date().getTime();
                seenScanIds = {};
            }
            remember();
            goOnline();
            pollOnce();
        }, function (data) {
            if (state.role !== "host" || state.stopped) return;
            goOffline("Reconnecting to the server...", !!(data && data.network));
        });
    }

    /*
        The handheld re-joins with the code it still remembers, reusing its own
        device id so the desktop sees the same handheld coming back rather than a
        second one appearing.
    */
    function reconnectRemote() {
        apiCall("pairJoin.agi", {
            code: state.code,
            deviceName: state.deviceName,
            deviceId: state.deviceId
        }, function (data) {
            if (state.role !== "remote" || state.stopped) return;

            state.sessionId = data.sessionId;
            state.deviceId = data.deviceId;
            state.hostMode = data.hostMode || state.hostMode;
            state.hostStep = data.hostStep || state.hostStep;
            remember();
            goOnline();
            startPing();
        }, function (data) {
            if (state.role !== "remote" || state.stopped) return;
            if (endedByHost(data)) {
                giveUp("The desktop ended the session - pair again");
                return;
            }
            goOffline("Reconnecting to the desktop...", !!(data && data.network));
        });
    }

    /* Sends everything held during the outage, in the order it was scanned */
    function flushOutbox() {
        if (state.role !== "remote" || !outbox.length || !state.online) return;

        var next = outbox[0];
        apiCall("pairPush.agi", {
            sessionId: state.sessionId,
            deviceId: state.deviceId,
            barcode: next.code
        }, function (data) {
            outbox.shift();
            remember();
            state.hostMode = data.hostMode || state.hostMode;
            state.hostStep = data.hostStep || state.hostStep;
            announce();
            emit("onFlush", { code: next.code, remaining: outbox.length });
            flushOutbox();
        }, function (data) {
            if (endedByHost(data)) {
                giveUp("The desktop ended the session - the held scans were not sent");
                return;
            }
            // Back offline mid-flush: the rest stay held
            goOffline("Reconnecting to the desktop...", !!(data && data.network));
        });
    }

    function queueScan(code) {
        if (outbox.length >= OUTBOX_MAX) return false;
        outbox.push({ code: code, at: new Date().getTime() });
        remember();
        announce();
        return true;
    }

    function reset() {
        stopPing();
        if (reconnectTimer) {
            clearTimeout(reconnectTimer);
            reconnectTimer = null;
        }
        outbox = [];
        state.online = false;
        state.attempt = 0;
        state.offlineSince = 0;
        state.role = "none";
        state.sessionId = "";
        state.deviceId = "";
        state.code = "";
        state.devices = [];
        state.polling = false;
        state.stopped = true;
        state.after = 0;
        seenScanIds = {};
        remember();
        announce();
    }

    /* ── Host: show a code, then wait for barcodes ──────────────────────── */

    /*
        startHost(options)
            options.reset      true to invalidate the old code and issue a new one
            options.getContext function() -> { mode, step }, echoed to handhelds
    */
    function startHost(options) {
        options = options || {};
        if (options.getContext) handlers.getContext = options.getContext;

        apiCall("pairHost.agi", { reset: options.reset ? "true" : "false" }, function (data) {
            state.role = "host";
            state.sessionId = data.sessionId;
            state.code = data.code;
            state.devices = data.devices || [];
            state.deviceId = "";
            state.lastError = "";
            state.stopped = false;
            // Only barcodes scanned from now on are of interest; anything left
            // in a queue from an earlier shift is history, not a pending scan.
            state.after = data.serverTime || new Date().getTime();
            seenScanIds = {};

            remember();
            goOnline();
            pollOnce();
        }, function (data) {
            state.lastError = (data && data.error) || "Could not start pairing";
            emit("onError", state.lastError);
            // Starting is also worth retrying: the server may just be booting
            if (state.role === "host") goOffline(state.lastError, !!(data && data.network));
        });
    }

    function pollOnce() {
        if (state.role !== "host" || state.stopped) return;

        var context = handlers.getContext ? handlers.getContext() : {};
        var startedAt = new Date().getTime();
        state.polling = true;

        apiCall("pairPoll.agi", {
            sessionId: state.sessionId,
            after: state.after,
            waitMs: POLL_HOLD_MS,
            hostMode: context.mode || "",
            hostStep: context.step || 1
        }, function (data) {
            if (state.role !== "host" || state.stopped) return;

            state.devices = data.devices || [];
            goOnline();
            emit("onDevices", state.devices);

            var scans = data.scans || [];
            for (var i = 0; i < scans.length; i++) {
                var scan = scans[i];
                // The server window is inclusive, so the same scan can arrive
                // twice at a millisecond boundary - deliver each one once
                if (seenScanIds[scan.id]) continue;
                seenScanIds[scan.id] = true;
                if (scan.ts > state.after) state.after = scan.ts;
                emit("onScan", scan);
            }

            announce();

            // Straight back in when scans arrived; otherwise respect the floor
            var elapsed = new Date().getTime() - startedAt;
            var gap = (scans.length > 0 || elapsed >= POLL_MIN_GAP_MS)
                ? 0
                : (POLL_MIN_GAP_MS - elapsed);
            if (gap > 0) {
                setTimeout(pollOnce, gap);
            } else {
                pollOnce();
            }
        }, function (data) {
            if (state.role !== "host" || state.stopped) return;

            // Both an unreachable server and a session the server has forgotten
            // are recoverable: reconnectHost re-establishes it, keeping the same
            // code so any paired handheld never notices
            goOffline(data && data.expired
                ? "Restoring the pairing session..."
                : "Reconnecting to the server...", !!(data && data.network));
        });
    }

    function stopHost(onDone) {
        var sessionId = state.sessionId;
        state.stopped = true;
        if (!sessionId) {
            reset();
            if (onDone) onDone();
            return;
        }
        apiCall("pairEnd.agi", { sessionId: sessionId }, function () {
            reset();
            if (onDone) onDone();
        }, function () {
            reset();
            if (onDone) onDone();
        });
    }

    /* ── Remote: join a desktop and forward every scan ──────────────────── */

    function join(code, deviceName, onDone, onFail) {
        apiCall("pairJoin.agi", {
            code: code,
            deviceName: deviceName,
            deviceId: state.deviceId || ""
        }, function (data) {
            state.role = "remote";
            state.sessionId = data.sessionId;
            state.deviceId = data.deviceId;
            state.deviceName = data.deviceName;
            state.code = ("" + code).toUpperCase();
            state.hostMode = data.hostMode || "";
            state.hostStep = data.hostStep || 1;
            state.stopped = false;

            remember();
            goOnline();
            startPing();
            if (onDone) onDone(status());
        }, function (data) {
            state.lastError = (data && data.error) || "Could not pair";
            emit("onError", state.lastError);
            if (onFail) onFail(state.lastError);
        });
    }

    /*
        Forwards one barcode to the host. The callback reports the host's current
        mode so the handheld can show what the desktop did with it.
    */
    function push(barcode, onDone, onFail) {
        if (state.role !== "remote") {
            if (onFail) onFail("Not paired to a desktop");
            return;
        }

        /*
            Already offline: hold it rather than failing. The operator has
            scanned a real barcode and should be able to keep working through a
            dead spot - the queue goes out the moment contact is back.
        */
        if (!state.online) {
            if (queueScan(barcode)) {
                if (onDone) onDone({ queued: true, queueLength: outbox.length });
            } else if (onFail) {
                onFail("Too many scans waiting - reconnect before scanning more");
            }
            return;
        }

        apiCall("pairPush.agi", {
            sessionId: state.sessionId,
            deviceId: state.deviceId,
            barcode: barcode
        }, function (data) {
            state.hostMode = data.hostMode || state.hostMode;
            state.hostStep = data.hostStep || state.hostStep;
            goOnline();
            if (onDone) onDone(data);
        }, function (data) {
            if (endedByHost(data)) {
                giveUp("The desktop ended the session - pair again");
                if (onFail) onFail("The desktop ended the session");
                return;
            }

            var held = queueScan(barcode);
            goOffline(data && data.expired
                ? "Reconnecting to the desktop..."
                : "Reconnecting to the server...", !!(data && data.network));

            // Held, so as far as the operator is concerned the pull worked - it
            // just has not landed yet
            if (held) {
                if (onDone) onDone({ queued: true, queueLength: outbox.length });
            } else if (onFail) {
                onFail("Too many scans waiting - reconnect before scanning more");
            }
        });
    }

    function leave(onDone) {
        var sessionId = state.sessionId;
        var deviceId = state.deviceId;
        state.stopped = true;
        if (!sessionId || !deviceId) {
            reset();
            if (onDone) onDone();
            return;
        }
        apiCall("pairEnd.agi", { sessionId: sessionId, deviceId: deviceId }, function () {
            reset();
            if (onDone) onDone();
        }, function () {
            reset();
            if (onDone) onDone();
        });
    }

    /*
        init(options)
            options.api        function(script, payload, done, fail)
            options.onScan     function(scan)     - host received a barcode
            options.onDevices  function(devices)  - host device list changed
            options.onStatus   function(status)   - anything changed
            options.onReconnect function(status)  - contact restored after an outage
            options.onFlush    function({code, remaining}) - a held scan went out
            options.onError    function(message)
        Resumes a stored pairing when there is one.
    */
    function init(options) {
        apiCall = options.api;
        handlers.onScan = options.onScan;
        handlers.onDevices = options.onDevices;
        handlers.onStatus = options.onStatus;
        handlers.onError = options.onError;
        handlers.onReconnect = options.onReconnect;
        handlers.onFlush = options.onFlush;
        handlers.getContext = options.getContext;

        // Coming back to the app should show the desktop's current mode at once
        document.addEventListener("visibilitychange", function () {
            if (!document.hidden && state.role === "remote") ping();
        });

        var saved = recall();
        if (!saved) return;

        outbox = (saved.outbox && saved.outbox.length) ? saved.outbox : [];

        if (saved.role === "host") {
            startHost({ reset: false });
        } else if (saved.role === "remote" && saved.code) {
            state.role = "remote";
            state.stopped = false;
            state.sessionId = saved.sessionId || "";
            state.deviceId = saved.deviceId || "";
            state.deviceName = saved.deviceName || "Handheld";
            state.code = saved.code;
            announce();

            // Straight into the reconnection path, so a handheld that was closed
            // mid-outage comes back holding its queue instead of losing it
            reconnectRemote();
        }
    }

    return {
        init: init,
        startHost: startHost,
        stopHost: stopHost,
        join: join,
        push: push,
        ping: ping,
        queued: function () { return outbox.length; },
        leave: leave,
        status: status,
        reset: reset,
        isHost: function () { return state.role === "host"; },
        isRemote: function () { return state.role === "remote"; }
    };
})();
