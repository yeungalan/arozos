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
    var POLL_RETRY_MS = 3000;

    // Floor between empty polls. The server normally holds the request for
    // POLL_HOLD_MS, but a reverse proxy that buffers or shortens the response
    // would otherwise turn this into a tight request loop.
    var POLL_MIN_GAP_MS = 1000;

    // Handheld heartbeat. Keeps the desktop's "connected" indicator honest and
    // refreshes the host's mode, so the operator can see what the next trigger
    // pull will do before pulling it rather than after.
    var PING_EVERY_MS = 6000;

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
        lastError: ""
    };

    var seenScanIds = {};    // guards the inclusive-timestamp window
    var pingTimer = null;
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
            online: state.polling,
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
                code: state.code
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
            state.lastError = "";
            if (changed) announce();
        }, function (data) {
            if (data && data.expired) {
                state.lastError = data.error || "The desktop ended the session";
                emit("onError", state.lastError);
                reset();
            }
        });
    }

    function reset() {
        stopPing();
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
            announce();
            pollOnce();
        }, function (data) {
            state.lastError = (data && data.error) || "Could not start pairing";
            emit("onError", state.lastError);
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
            state.lastError = "";
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
            state.polling = false;

            if (data && data.expired) {
                state.lastError = data.error || "Pairing session ended";
                emit("onError", state.lastError);
                reset();
                return;
            }

            // A dropped connection is normal on a handheld network; back off
            // briefly and pick the poll back up rather than ending the pairing
            state.lastError = (data && data.error) || "Connection lost - retrying";
            announce();
            setTimeout(pollOnce, POLL_RETRY_MS);
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
            state.lastError = "";
            state.stopped = false;

            remember();
            announce();
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
        apiCall("pairPush.agi", {
            sessionId: state.sessionId,
            deviceId: state.deviceId,
            barcode: barcode
        }, function (data) {
            state.hostMode = data.hostMode || state.hostMode;
            state.hostStep = data.hostStep || state.hostStep;
            state.lastError = "";
            announce();
            if (onDone) onDone(data);
        }, function (data) {
            state.lastError = (data && data.error) || "Could not reach the desktop";
            if (data && data.expired) {
                emit("onError", state.lastError);
                reset();
            } else {
                announce();
            }
            if (onFail) onFail(state.lastError);
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
            options.onError    function(message)
        Resumes a stored pairing when there is one.
    */
    function init(options) {
        apiCall = options.api;
        handlers.onScan = options.onScan;
        handlers.onDevices = options.onDevices;
        handlers.onStatus = options.onStatus;
        handlers.onError = options.onError;
        handlers.getContext = options.getContext;

        // Coming back to the app should show the desktop's current mode at once
        document.addEventListener("visibilitychange", function () {
            if (!document.hidden && state.role === "remote") ping();
        });

        var saved = recall();
        if (!saved) return;

        if (saved.role === "host") {
            startHost({ reset: false });
        } else if (saved.role === "remote" && saved.code) {
            state.deviceId = saved.deviceId || "";
            join(saved.code, saved.deviceName || "Handheld", null, function () {
                // The desktop is gone or the code was rotated - start clean
                reset();
            });
        }
    }

    return {
        init: init,
        startHost: startHost,
        stopHost: stopHost,
        join: join,
        push: push,
        ping: ping,
        leave: leave,
        status: status,
        reset: reset,
        isHost: function () { return state.role === "host"; },
        isRemote: function () { return state.role === "remote"; }
    };
})();
