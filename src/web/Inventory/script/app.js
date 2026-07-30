/*
    Inventory - application logic

    The whole item list is loaded once at start-up and kept in memory, so
    searching and looking a barcode up cost nothing on the network. Only writes
    (stock in/out, count, move, edits) go back to the AGI backend, and each one
    returns the updated item so the local copy stays authoritative.
*/

var App = (function () {

    var BACKEND = "Inventory/backend/";

    var state = {
        items: [],
        locations: [],
        settings: {},
        movements: [],
        loaded: false,

        view: "scan",
        mode: "lookup",         // lookup | in | out | move | set
        step: 1,                // units per in/out scan
        moveTarget: "",         // destination for move mode
        query: "",
        filter: "all",
        session: [],            // this shift's operations, newest first
        searchResults: [],      // last rendered result set, for keyboard nav
        searchIndex: -1,        // highlighted row on the Find view, -1 = none
        scanTyping: false,      // kept for the settings toggle; per-field state
                                // now lives on the element as data-typing
        runs: [],               // installed cable runs
        catalogueSize: 0,
        counting: null,         // the open blind count, null when none
        countMethod: "keypad",  // how Count mode works: "keypad" | "blind"
        countBatch: {},         // itemId -> batch chosen for it in this count
        runFilter: "all",
        cableView: "list",      // "list" | "diagram"
        diagramFocus: "",       // location the diagram is centred on, "" = all
        pairing: null,          // InvPairing status, null until it reports in
        forwarding: false,      // a scan is in flight to the paired desktop
        lastResult: null,
        busy: false
    };

    var scanner = null;
    var searchTimer = null;

    // Same wedge-vs-typing threshold the scanner module uses
    var SEARCH_BURST_GAP_MS = 40;

    /* ── Small helpers ──────────────────────────────────────────────────── */

    function $(id) {
        return document.getElementById(id);
    }

    function esc(text) {
        return InvSearch.escapeHtml(text);
    }

    /* Whole days from today to an ISO date; null when the date is absent */
    function daysUntil(isoDate) {
        if (!isoDate) return null;
        var parts = ("" + isoDate).split("-");
        if (parts.length !== 3) return null;
        var target = new Date(parseInt(parts[0], 10), parseInt(parts[1], 10) - 1, parseInt(parts[2], 10));
        if (isNaN(target.getTime())) return null;
        var now = new Date();
        var today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
        return Math.round((target - today) / 86400000);
    }

    function relativeDays(days) {
        if (days === null) return "";
        if (days === 0) return "today";
        if (days === 1) return "tomorrow";
        if (days === -1) return "yesterday";
        if (days < 0) return (-days) + "d ago";
        return "in " + days + "d";
    }

    function money(value) {
        var amount = parseFloat(value);
        if (isNaN(amount)) amount = 0;
        return (state.settings.currency || "$") + amount.toFixed(2);
    }

    function qtyText(value) {
        var amount = parseFloat(value);
        if (isNaN(amount)) amount = 0;
        // Whole numbers are the norm; keep decimals only when they carry info
        return (Math.round(amount) === amount) ? ("" + amount) : amount.toFixed(2);
    }

    function timeText(ms) {
        var d = new Date(ms);
        var pad = function (n) { return (n < 10 ? "0" : "") + n; };
        return pad(d.getHours()) + ":" + pad(d.getMinutes()) + ":" + pad(d.getSeconds());
    }

    function findById(id) {
        for (var i = 0; i < state.items.length; i++) {
            if (state.items[i].id === id) return state.items[i];
        }
        return null;
    }

    function findByBarcode(barcode) {
        var needle = ("" + barcode).trim().toLowerCase();
        if (needle === "") return null;
        for (var i = 0; i < state.items.length; i++) {
            if (("" + state.items[i].barcode).trim().toLowerCase() === needle) return state.items[i];
        }
        return null;
    }

    /* Replaces (or inserts) an item returned by the backend */
    function upsertItem(item) {
        for (var i = 0; i < state.items.length; i++) {
            if (state.items[i].id === item.id) {
                state.items[i] = item;
                return;
            }
        }
        state.items.push(item);
    }

    /* ── Backend calls ──────────────────────────────────────────────────── */

    function api(script, payload, onDone, onFail) {
        ao_module_agirun(BACKEND + script, payload || {}, function (data) {
            if (typeof data === "string") {
                try {
                    data = JSON.parse(data);
                } catch (e) {
                    toast("Unexpected reply from the server", "err");
                    if (onFail) onFail({ error: "Bad response" });
                    return;
                }
            }
            if (data && data.error && !data.unknownBarcode) {
                if (onFail) {
                    onFail(data);
                } else {
                    toast(data.error, "err");
                }
                return;
            }
            if (onDone) onDone(data);
        }, function () {
            toast("Cannot reach the server", "err");
            if (onFail) onFail({ error: "Network error" , network: true });
        });
    }

    /* ── Toasts ─────────────────────────────────────────────────────────── */

    function toast(message, kind) {
        var host = $("toast");
        var node = document.createElement("div");
        node.className = "toast " + (kind || "");
        var icon = kind === "err" ? "exclamation triangle"
            : (kind === "warn" ? "warning circle" : (kind === "ok" ? "check circle" : "info circle"));
        node.innerHTML = '<i class="' + icon + ' icon"></i><span>' + esc(message) + "</span>";
        host.appendChild(node);
        setTimeout(function () {
            if (node.parentNode) node.parentNode.removeChild(node);
        }, kind === "err" ? 4200 : 2600);
    }

    /* ── Alert classification ───────────────────────────────────────────── */

    /* Returns every warning that applies to an item, worst first */
    function itemAlerts(item) {
        var alerts = [];
        var expiryWarn = state.settings.expiryWarnDays;
        var warrantyWarn = state.settings.warrantyWarnDays;

        var expiryDays = daysUntil(item.expiryDate);
        if (expiryDays !== null) {
            if (expiryDays < 0) {
                alerts.push({ kind: "expired", level: "danger", icon: "hourglass end", text: "Expired " + relativeDays(expiryDays) });
            } else if (expiryDays <= expiryWarn) {
                alerts.push({ kind: "expiring", level: "warn", icon: "hourglass half", text: "Expires " + relativeDays(expiryDays) });
            }
        }

        var warrantyDays = daysUntil(item.warrantyEnd);
        if (warrantyDays !== null) {
            if (warrantyDays < 0) {
                alerts.push({ kind: "warrantyOver", level: "danger", icon: "shield alternate", text: "Warranty ended " + relativeDays(warrantyDays) });
            } else if (warrantyDays <= warrantyWarn) {
                alerts.push({ kind: "warrantyEnding", level: "warn", icon: "shield alternate", text: "Warranty ends " + relativeDays(warrantyDays) });
            }
        }

        if (item.qty <= 0) {
            alerts.push({ kind: "out", level: "danger", icon: "ban", text: "Out of stock" });
        } else if (item.minQty > 0 && item.qty <= item.minQty) {
            alerts.push({ kind: "low", level: "warn", icon: "battery low", text: "Low stock (min " + qtyText(item.minQty) + ")" });
        }

        alerts.sort(function (a, b) {
            var rank = function (x) { return x.level === "danger" ? 0 : 1; };
            return rank(a) - rank(b);
        });
        return alerts;
    }

    function alertCount() {
        var total = 0;
        for (var i = 0; i < state.items.length; i++) {
            if (itemAlerts(state.items[i]).length > 0) total++;
        }
        return total;
    }

    function badgesHtml(alerts) {
        if (!alerts.length) return "";
        var html = '<div class="badges">';
        for (var i = 0; i < alerts.length; i++) {
            html += '<span class="badge ' + alerts[i].level + '"><i class="' + alerts[i].icon +
                ' icon"></i>' + esc(alerts[i].text) + "</span>";
        }
        return html + "</div>";
    }

    /* ── Scan handling ──────────────────────────────────────────────────── */

    function onScan(code, source) {
        if (!state.loaded) {
            // The operator has already pulled the trigger - the barcode is real
            // and must not be thrown away just because the inventory is still
            // on its way. Hold it and apply it the moment the data lands.
            queueUntilLoaded(code, source);
            return;
        }
        if (state.busy) return;

        // Paired as a remote scanner: this device is an input for the desktop,
        // so the barcode is forwarded and nothing is changed here. A scan that
        // arrived *from* a handheld is exempt, or a host that is also paired
        // would bounce it straight back.
        if (InvPairing.isRemote() && source !== "remote") {
            forwardScan(code);
            return;
        }

        // Scanning on the Find view means "find this", not "apply the current
        // mode to it" - that view exists to look things up. Every other view
        // hands the scan to the active mode on the Scan view.
        if (state.view === "search") {
            findByScan(code);
            return;
        }

        if (state.view !== "scan") switchView("scan");

        if (state.mode === "lookup") {
            var found = findByBarcode(code);
            if (found) {
                InvScanner.feedbackOk();
                showResult({ type: "lookup", item: found });
                return;
            }

            // A cable label is a barcode too, so a scan that is not stock may
            // still be an installed run
            var run = findRunByCode(code);
            if (run) {
                InvScanner.feedbackOk();
                showResult({ type: "run", run: run });
                return;
            }

            reportUnknown(code);
            return;
        }

        if (state.mode === "set") {
            if (state.countMethod === "blind") {
                tallyScan(code);
                return;
            }
            var target = findByBarcode(code);
            if (!target) {
                reportUnknown(code);
                return;
            }
            InvScanner.feedbackOk();
            openKeypad(target);
            return;
        }

        if (state.mode === "move" && state.moveTarget === "") {
            InvScanner.feedbackError();
            toast("Pick a destination location first", "err");
            return;
        }

        runStockOp({
            barcode: code,
            op: state.mode,
            amount: state.step,
            toLocation: state.moveTarget
        });
    }

    /* Sends one stock operation and folds the result back into local state */
    function runStockOp(payload, onSuccess) {
        state.busy = true;
        setScanStatus("Working...", false);

        api("stockOp.agi", payload, function (data) {
            state.busy = false;

            if (data.error && data.unknownBarcode) {
                setScanStatus(null, true);
                reportUnknown(data.unknownBarcode);
                return;
            }

            var before = findById(data.item.id);
            var previousQty = before ? before.qty : data.item.qty;
            var previousLocation = before ? before.location : data.item.location;

            upsertItem(data.item);
            if (data.locations) state.locations = data.locations;

            if (data.warning) {
                InvScanner.feedbackWarn();
                toast(data.warning, "warn");
            } else {
                InvScanner.feedbackOk();
            }

            pushSession({
                op: payload.op,
                itemId: data.item.id,
                name: data.item.name,
                barcode: data.item.barcode,
                delta: data.movement ? data.movement.delta : 0,
                qtyAfter: data.item.qty,
                prevQty: previousQty,
                prevLocation: previousLocation,
                newLocation: data.item.location,
                ts: data.movement ? data.movement.ts : new Date().getTime()
            });

            showResult({
                type: payload.op,
                item: data.item,
                delta: data.movement ? data.movement.delta : 0,
                warning: data.warning || "",
                fromLocation: previousLocation
            });

            setScanStatus(null, true);
            renderAlertBadge();
            if (state.view === "search") renderSearch();
            if (onSuccess) onSuccess(data);
        }, function (data) {
            state.busy = false;
            setScanStatus(null, true);

            if (data && data.unknownBarcode !== undefined && data.unknownBarcode !== "") {
                reportUnknown(data.unknownBarcode);
                return;
            }
            if (data && data.needBatch && data.item) {
                InvScanner.feedbackWarn();
                openBatchPicker(data.item, data.batches, false, function (batchId) {
                    var retry = {};
                    for (var key in payload) retry[key] = payload[key];
                    retry.batchId = batchId;
                    runStockOp(retry, onSuccess);
                });
                return;
            }

            InvScanner.feedbackError();
            showResult({ type: "error", message: (data && data.error) || "Operation failed" });
        });
    }

    function pushSession(entry) {
        entry.undone = false;
        entry.key = "s" + new Date().getTime() + "_" + Math.random().toString(36).slice(2, 6);
        state.session.unshift(entry);
        if (state.session.length > 60) state.session.pop();
        renderSession();
    }

    /*
        Undo restores the pre-operation value rather than applying an inverse
        delta, so a stale count can never be made worse by tapping undo twice.
    */
    function undoSession(key) {
        var entry = null;
        for (var i = 0; i < state.session.length; i++) {
            if (state.session[i].key === key) { entry = state.session[i]; break; }
        }
        if (!entry || entry.undone) return;

        var payload;
        if (entry.op === "move") {
            payload = { itemId: entry.itemId, op: "move", toLocation: entry.prevLocation, note: "Undo" };
            if (!entry.prevLocation) {
                toast("Cannot undo - the item had no previous location", "warn");
                return;
            }
        } else {
            payload = { itemId: entry.itemId, op: "set", amount: entry.prevQty, note: "Undo" };
        }

        runStockOp(payload, function () {
            entry.undone = true;
            renderSession();
        });
    }

    /*
        Handles a scan that arrived while the Find view was open: show the code
        as the query so the list matches what was scanned, then open the item
        outright when the barcode is an exact hit.
    */
    function findByScan(code) {
        var input = $("searchInput");
        input.value = code;
        state.query = code;
        $("searchClear").className = "search-clear visible";
        renderSearch();

        var direct = findByBarcode(code);
        if (direct) {
            InvScanner.feedbackOk();
            openItemDetail(direct.id);
        } else if (state.searchResults.length) {
            InvScanner.feedbackWarn();
            toast("No exact barcode match - showing the closest items", "warn");
        } else {
            InvScanner.feedbackError();
            toast("Nothing matches " + code, "err");
        }
    }

    // Scans taken before the inventory finished loading
    var pendingScans = [];
    var PENDING_MAX = 8;
    var PENDING_STALE_MS = 20000;

    function queueUntilLoaded(code, source) {
        if (pendingScans.length < PENDING_MAX) {
            pendingScans.push({ code: code, source: source, at: new Date().getTime() });
        }
        InvScanner.feedbackWarn();
        setScanStatus("Loading inventory - " + pendingScans.length +
            (pendingScans.length === 1 ? " scan held" : " scans held"), false);
    }

    /*
        Replays held scans one at a time. Each one may start a server round trip
        that sets state.busy, so the next waits for it rather than being dropped.
    */
    function flushPendingScans() {
        if (!pendingScans.length) return;
        if (state.busy) {
            setTimeout(flushPendingScans, 150);
            return;
        }

        var next = pendingScans.shift();
        // A scan from minutes ago is not what the operator is holding now
        if (new Date().getTime() - next.at <= PENDING_STALE_MS) {
            onScan(next.code, next.source);
        }
        if (pendingScans.length) setTimeout(flushPendingScans, 150);
    }

    /* ── Remote scanner pairing ─────────────────────────────────────────── */

    var MODE_LABELS = {
        lookup: "Look up", in: "Stock in", out: "Stock out",
        move: "Move", set: "Count"
    };

    /* Handheld side: send the barcode to the desktop and report what it did */
    function forwardScan(code) {
        state.forwarding = true;
        showResult({ type: "sending", barcode: code });
        setScanStatus("Sending to the desktop...", false);

        InvPairing.push(code, function (data) {
            state.forwarding = false;

            if (data.queued) {
                // Held, not lost: the operator can keep working through a dead spot
                InvScanner.feedbackWarn();
                showResult({
                    type: "sent",
                    barcode: code,
                    detail: "Held - " + data.queueLength +
                        (data.queueLength === 1 ? " scan waiting" : " scans waiting") +
                        " for the connection"
                });
                setScanStatus(null, true);
                armInput();
                return;
            }

            InvScanner.feedbackOk();
            var did = MODE_LABELS[data.hostMode] || "handled";
            showResult({
                type: "sent",
                barcode: code,
                detail: "Desktop is set to " + did +
                    (data.hostMode === "in" || data.hostMode === "out"
                        ? " " + qtyText(data.hostStep) : "")
            });
            setScanStatus(null, true);
            armInput();
        }, function (message) {
            state.forwarding = false;
            InvScanner.feedbackError();
            showResult({ type: "error", message: message });
            setScanStatus(null, true);
            armInput();
        });
    }

    /*
        Desktop side: a paired handheld scanned something. Put it in the scan box
        so the operator can see the value land, then run it through exactly the
        same path a locally scanned barcode takes.
    */
    /*
        Desktop side: a paired handheld scanned something.

        It behaves exactly as a wedge plugged into this machine would - the value
        lands in whatever field is focused. So if the operator is on the Cables
        tab with a run's label box focused, the scan fills that box; if they are
        on the Scan view with the scan box armed, it runs the active mode. That
        is the difference between a remote scanner and a remote button.
    */
    /*
        Remote scans arrive in batches - three at once when a handheld's queue
        drains after an outage - and each one may start a server round trip that
        sets state.busy. onScan ignores a scan while busy, so they are queued and
        applied one at a time; otherwise a flush of five would apply one and
        silently discard four.
    */
    var remoteQueue = [];

    function receiveRemoteScan(scan) {
        remoteQueue.push(scan);
        drainRemoteQueue();
    }

    function drainRemoteQueue() {
        if (!remoteQueue.length) return;
        if (state.busy || !state.loaded) {
            setTimeout(drainRemoteQueue, 120);
            return;
        }

        applyRemoteScan(remoteQueue.shift());
        if (remoteQueue.length) setTimeout(drainRemoteQueue, 120);
    }

    function applyRemoteScan(scan) {
        toast(scan.deviceName + ": " + scan.code, "");

        var active = document.activeElement;
        if (isRemoteTypeTarget(active)) {
            deliverToField(active, scan.code);
            InvScanner.feedbackOk();
            return;
        }

        var input = $("scanInput");
        if (input) {
            input.value = scan.code;
            // Cleared once the operation has been shown, so the box is armed
            // and empty again for the next pull
            setTimeout(function () {
                if (input.value === scan.code) input.value = "";
            }, 600);
        }
        onScan(scan.code, "remote");
    }

    /*
        Which focused elements should simply receive the text. The scan box is
        excluded because it is the workflow's own entry point, not a field being
        filled in, and a textarea is excluded because a barcode is never prose.
    */
    function isRemoteTypeTarget(element) {
        if (!element || element === $("scanInput")) return false;
        if ((element.tagName || "").toLowerCase() !== "input") return false;

        var type = (element.getAttribute("type") || "text").toLowerCase();
        return type === "text" || type === "search" || type === "number" || type === "tel";
    }

    /*
        Puts the value in and tells the page about it. The synthetic events matter:
        the search box filters on input, the item editor looks a barcode up on
        change, and Enter is what a wedge would have sent - so a field that acts
        on Enter acts here too.
    */
    function deliverToField(input, value) {
        input.value = value;

        var fire = function (name, Ctor, init) {
            try {
                input.dispatchEvent(new Ctor(name, init));
            } catch (e) {
                // Older engines without the constructors - the value is still set
            }
        };

        fire("input", Event, { bubbles: true });
        fire("change", Event, { bubbles: true });
        fire("keydown", KeyboardEvent, { bubbles: true, key: "Enter", cancelable: true });

        flashField(input);
    }

    /* Brief highlight so it is obvious which box just received a remote scan */
    function flashField(input) {
        input.className = (input.className ? input.className + " " : "") + "remote-filled";
        setTimeout(function () {
            input.className = input.className.replace(/\s*remote-filled/, "");
        }, 700);
    }

    function pairingStatusHtml() {
        var pairing = state.pairing;
        if (!pairing || pairing.role === "none") return "";

        if (pairing.role === "remote") {
            var offline = pairing.reconnecting;
            return '<div class="pair-bar remote' + (offline ? " offline" : "") + '">' +
                '<i class="' + (offline ? "sync" : "mobile alternate") + ' icon"></i>' +
                '<div class="body"><div class="t">' +
                (offline ? "Reconnecting to the desktop" : "Sending scans to the desktop") + "</div>" +
                '<div class="s">' +
                (pairing.queued
                    ? pairing.queued + (pairing.queued === 1 ? " scan held" : " scans held") +
                      " - they go out as soon as it is back"
                    : "Paired as " + esc(pairing.deviceName) +
                      (pairing.hostMode
                        ? " &middot; desktop is on " + esc(MODE_LABELS[pairing.hostMode] || pairing.hostMode)
                        : "")) +
                "</div></div>" +
                '<button class="btn small" id="pairLeave">Stop</button></div>';
        }

        var online = 0;
        for (var i = 0; i < pairing.devices.length; i++) {
            if (pairing.devices[i].online) online++;
        }
        var lost = pairing.reconnecting;
        return '<div class="pair-bar host' + (lost ? " offline" : (online ? " live" : "")) + '">' +
            '<i class="' + (lost ? "sync" : "wifi") + ' icon"></i>' +
            '<div class="body"><div class="t">' +
            (lost
                ? "Reconnecting to the server"
                : (online ? online + (online === 1 ? " handheld connected" : " handhelds connected")
                          : "Waiting for a handheld")) + "</div>" +
            '<div class="s">' +
            (lost ? "The pairing is kept - scans resume automatically"
                  : "Pairing code " + esc(pairing.code)) + "</div></div>" +
            '<button class="btn small" id="pairPanel">Manage</button></div>';
    }

    var lastPairBarHtml = null;

    function renderPairingBar() {
        var host = $("pairBar");
        if (!host) return;

        // The host polls continuously; rebuilding this bar on every response
        // would replace its buttons mid-tap and flicker the status text
        applyRemoteScannerUi();

        // The scan box names the desktop's mode, so it has to follow the
        // heartbeat too - but never step on an in-flight "Sending..."
        if (InvPairing.isRemote() && !state.forwarding) {
            setScanStatus(null, true);
        }

        var html = pairingStatusHtml();
        if (html === lastPairBarHtml) return;
        lastPairBarHtml = html;
        host.innerHTML = html;

        if ($("pairLeave")) {
            $("pairLeave").onclick = function () {
                InvPairing.leave(function () {
                    toast("Stopped forwarding scans", "");
                });
            };
        }
        if ($("pairPanel")) {
            $("pairPanel").onclick = openPairingSheet;
        }
    }

    /*
        While this device is forwarding, its own mode selector and context row
        do nothing - the desktop's mode is what applies - so they are hidden
        rather than left there to be tapped hopefully.
    */
    function applyRemoteScannerUi() {
        var remote = InvPairing.isRemote();
        var grid = $("modeGrid");
        var context = $("contextCard");
        if (grid) grid.style.display = remote ? "none" : "";
        if (context) context.style.display = remote ? "none" : "";
    }

    /* The pairing screen: pick a role, or manage the active pairing */
    function openPairingSheet() {
        var pairing = state.pairing || { role: "none", devices: [] };
        var html = "";
        var foot = "";

        if (pairing.role === "host") {
            html += '<div class="card pair-code-card">' +
                '<div class="context-label">Pairing code</div>' +
                '<div class="pair-code">' + esc(pairing.code) + "</div>" +
                '<div class="hint">On the handheld, open Inventory, go to <b>More</b> and choose ' +
                "<b>Send my scans to a desktop</b>, then key in this code. " +
                "Both devices must be signed in as the same ArozOS user.</div></div>";

            html += '<div class="section-title">Handhelds</div><div class="card flush" id="pairDevices">' +
                pairDeviceListHtml(pairing.devices) + "</div>";

            html += '<div class="card muted" style="font-size:13px;line-height:1.55;">' +
                "A forwarded barcode is treated exactly like one scanned here: whatever mode " +
                "this page is in - Look up, Stock in, Stock out, Move or Count - is what happens " +
                "to it. Change the mode here and the handheld follows.</div>";

            foot = '<button class="btn" id="pairNewCode"><i class="sync icon"></i>New code</button>' +
                '<button class="btn danger" id="pairStop"><i class="close icon"></i>Stop pairing</button>';

        } else if (pairing.role === "remote") {
            html += '<div class="card">' +
                '<div class="result-name">Sending scans to the desktop</div>' +
                '<div class="result-sub">Code ' + esc(pairing.code) + " &middot; this device is " +
                esc(pairing.deviceName) + "</div>" +
                (pairing.hostMode
                    ? '<div class="badges"><span class="badge info"><i class="desktop icon"></i>Desktop is on ' +
                      esc(MODE_LABELS[pairing.hostMode] || pairing.hostMode) + "</span></div>"
                    : "") +
                "</div>" +
                '<div class="card muted" style="font-size:13px;line-height:1.55;">' +
                "Every trigger pull is sent to the desktop instead of changing stock here. " +
                "The desktop decides what happens to it.</div>";

            foot = '<button class="btn danger block" id="pairStop"><i class="close icon"></i>Stop sending</button>';

        } else {
            html += '<div class="card muted" style="font-size:13px;line-height:1.55;">' +
                "Use one device as the scanner for another. The handheld reads the barcode, " +
                "the desktop receives it and applies whatever mode is selected there. " +
                "Both must be signed in as the same ArozOS user.</div>";

            html += '<button class="btn primary block" id="pairBeHost" style="margin-bottom:10px;">' +
                '<i class="desktop icon"></i>Receive scans on this device</button>' +
                '<button class="btn block" id="pairBeRemote" style="margin-bottom:10px;">' +
                '<i class="mobile alternate icon"></i>Send my scans to a desktop</button>' +
                '<button class="btn block ghost" id="pairOpenRemotePage">' +
                '<i class="expand icon"></i>Open the dedicated scanner page</button>' +
                '<div class="hint">A stripped-down screen with nothing but the scan box - ' +
                "what you want open on the handheld itself.</div>";
        }

        openSheet("Remote scanner", html, foot);

        if ($("pairNewCode")) {
            $("pairNewCode").onclick = function () {
                InvPairing.startHost({ reset: true, getContext: pairingContext });
                setTimeout(openPairingSheet, 400);
            };
        }
        if ($("pairStop")) {
            $("pairStop").onclick = function () {
                var done = function () {
                    closeSheet();
                    toast("Remote scanner stopped", "");
                };
                if (pairing.role === "host") InvPairing.stopHost(done); else InvPairing.leave(done);
            };
        }
        if ($("pairBeHost")) {
            $("pairBeHost").onclick = function () {
                InvPairing.startHost({ reset: false, getContext: pairingContext });
                setTimeout(openPairingSheet, 400);
            };
        }
        if ($("pairBeRemote")) {
            $("pairBeRemote").onclick = openJoinSheet;
        }
        if ($("pairOpenRemotePage")) {
            $("pairOpenRemotePage").onclick = function () {
                window.location.href = "remote.html";
            };
        }
    }

    function pairDeviceListHtml(devices) {
        if (!devices || !devices.length) {
            return '<div class="empty"><i class="mobile alternate icon"></i>' +
                "No handheld has joined yet.</div>";
        }
        var html = "";
        for (var i = 0; i < devices.length; i++) {
            var device = devices[i];
            html += '<div class="log-row">' +
                '<div class="log-icon ' + (device.online ? "in" : "lookup") + '">' +
                '<i class="mobile alternate icon"></i></div>' +
                '<div class="body"><div class="t">' + esc(device.deviceName) + "</div>" +
                '<div class="s">' + (device.online ? "connected" : "offline") +
                " &middot; " + device.scanCount + " scans sent</div></div></div>";
        }
        return html;
    }

    /*
        Shown under a scan field on a device with an on-screen keyboard, because
        the two-tap behaviour is not something anyone would guess.
    */
    function scanFieldHint() {
        if (!InvScanner.deviceHasSoftKeyboard()) return "";
        return "<b>Armed for scanning</b> - tap again to type by hand. ";
    }

    /* Handheld side: key in the code shown on the desktop */
    function openJoinSheet() {
        var defaultName = InvScanner.deviceHasSoftKeyboard() ? "Handheld" : "This computer";

        openSheet("Pair with a desktop",
            '<div class="field"><label>Pairing code</label>' +
            '<input type="text" id="joinCode" class="pair-input" maxlength="9" ' +
            'autocomplete="off" autocorrect="off" autocapitalize="characters" spellcheck="false" ' +
            'placeholder="ABC123">' +
            '<div class="hint">Shown on the desktop under More, Remote scanner.</div></div>' +
            '<div class="field"><label>Name this device</label>' +
            '<input type="text" id="joinName" value="' + esc(defaultName) + '" maxlength="40">' +
            '<div class="hint">Appears in the desktop\'s handheld list.</div></div>',
            '<button class="btn" id="joinCancel">Cancel</button>' +
            '<button class="btn primary" id="joinGo"><i class="linkify icon"></i>Pair</button>');

        var codeInput = $("joinCode");
        var commit = function () {
            var code = codeInput.value.trim();
            if (code === "") {
                toast("Enter the code shown on the desktop", "err");
                return;
            }
            InvPairing.join(code, $("joinName").value, function () {
                closeSheet();
                InvScanner.feedbackOk();
                toast("Paired - scans now go to the desktop", "ok");
            }, function (message) {
                InvScanner.feedbackError();
                toast(message, "err");
            });
        };

        $("joinCancel").onclick = closeSheet;
        $("joinGo").onclick = commit;
        codeInput.addEventListener("keydown", function (event) {
            if (event.key === "Enter") { event.preventDefault(); commit(); }
        });
        setTimeout(function () { codeInput.focus(); }, 60);
    }

    /* What the host advertises to its handhelds */
    function pairingContext() {
        return { mode: state.mode, step: state.step };
    }

    /* ── Unknown barcodes and the local catalogue ───────────────────────── */

    /*
        An unknown scan is worth more than "not found": the local catalogue may
        already know the product's name from a previous site or an imported
        UPC/JAN list, and the code itself says which country issued it and
        whether its check digit even adds up. All of that is resolved on this
        server - nothing about the scan leaves the machine.
    */
    function reportUnknown(code) {
        InvScanner.feedbackError();
        showResult({ type: "unknown", barcode: code });

        api("catalogueLookup.agi", { barcode: code }, function (data) {
            // Only decorate the card if it is still the one on screen
            if (!state.lastResult || state.lastResult.type !== "unknown") return;
            if (state.lastResult.barcode !== code) return;
            showResult({ type: "unknown", barcode: code, catalogue: data });
        });
    }

    /* ── Batches ────────────────────────────────────────────────────────── */

    function batchLabelOf(item, batchId) {
        if (!item || !batchId) return "";
        for (var i = 0; i < item.batches.length; i++) {
            if (item.batches[i].id === batchId) return item.batches[i].batch;
        }
        return "";
    }

    function batchRowHtml(batch, hideQty) {
        var days = daysUntil(batch.expiryDate);
        var tone = days === null ? "" :
            (days < 0 ? " danger" : (days <= state.settings.expiryWarnDays ? " warn" : ""));

        return '<div class="log-row batch-row" data-batch="' + esc(batch.id) + '">' +
            '<div class="log-icon lookup"><i class="tags icon"></i></div>' +
            '<div class="body"><div class="t">' + esc(batch.batch || "(no lot number)") + "</div>" +
            '<div class="s' + tone + '">' +
            (batch.expiryDate
                ? "expires " + esc(batch.expiryDate) + " (" + relativeDays(days) + ")"
                : "no expiry date") +
            "</div></div>" +
            (hideQty
                ? ""
                : '<div class="qty">' + qtyText(batch.qty) + "</div>") +
            "</div>";
    }

    /*
        Asks which lot an operation applies to.

        hideQty is set during a blind count: showing how many the system thinks
        are in the lot is exactly the number a blind count must not reveal.
    */
    function openBatchPicker(item, batches, hideQty, onPick) {
        var html = '<div class="card"><div class="result-name">' + esc(item.name) + "</div>" +
            '<div class="result-sub">' + esc(item.barcode) + " &middot; " +
            batches.length + " batches</div></div>" +
            '<div class="section-title">Which batch' + (hideQty ? " are you counting" : "") + "?</div>" +
            '<div class="card flush" id="batchChoices">';

        for (var i = 0; i < batches.length; i++) {
            html += batchRowHtml(batches[i], hideQty);
        }
        html += "</div>" +
            '<div class="hint">Listed earliest expiry first.</div>';

        openSheet("Pick a batch", html,
            '<button class="btn block" id="batchCancel">Cancel</button>');

        var rows = $("batchChoices").querySelectorAll(".batch-row");
        for (var r = 0; r < rows.length; r++) {
            rows[r].onclick = function () {
                var chosen = this.getAttribute("data-batch");
                closeSheet();
                onPick(chosen);
            };
        }
        $("batchCancel").onclick = closeSheet;
    }

    /* ── Blind cycle count ──────────────────────────────────────────────── */

    function startBlindCount(onReady) {
        var name = "Count " + new Date().toISOString().slice(0, 10);
        api("countStart.agi", { name: name }, function (data) {
            state.counting = {
                id: data.session.id,
                name: data.session.name,
                startedAt: data.session.startedAt,
                lines: data.session.lines.length
            };
            state.countBatch = {};
            renderContext();
            setScanStatus(null, true);
            armInput();
            if (onReady) onReady();
        });
    }

    /*
        One scan, one unit. The reply deliberately carries no book quantity, and
        neither does anything shown here - the operator sees only their own
        running tally until they open the review.
    */
    function tallyScan(code) {
        if (!state.counting) {
            startBlindCount(function () { tallyScan(code); });
            return;
        }

        var item = findByBarcode(code);
        var payload = { barcode: code, step: state.step };

        // The lot is asked for once per item per count, then remembered - asking
        // again on every one of fifty pulls would make a blind count unusable
        if (item && state.countBatch[item.id]) {
            payload.batchId = state.countBatch[item.id];
        }

        state.busy = true;
        api("countTally.agi", payload, function (data) {
            state.busy = false;
            InvScanner.feedbackOk();
            state.counting.lines = data.totalLines;
            renderContext();
            showResult({ type: "tally", line: data.line });
            setScanStatus(null, true);
            armInput();
        }, function (data) {
            state.busy = false;

            if (data && data.needBatch && data.item) {
                InvScanner.feedbackWarn();
                openBatchPicker(data.item, data.batches, true, function (batchId) {
                    state.countBatch[data.item.id] = batchId;
                    tallyScan(code);
                });
                return;
            }
            if (data && data.unknownBarcode !== undefined && data.unknownBarcode !== "") {
                reportUnknown(data.unknownBarcode);
                return;
            }
            if (data && data.noSession) {
                state.counting = null;
                renderContext();
            }
            InvScanner.feedbackError();
            showResult({ type: "error", message: (data && data.error) || "Could not count that" });
        });
    }

    function openCountReview() {
        openSheet("Count review",
            '<div class="card flush" id="reviewBody"><div class="empty">Loading...</div></div>', "");

        api("countReview.agi", {}, function (data) {
            if (!$("reviewBody")) return;

            if (!data.lines.length) {
                $("reviewBody").innerHTML =
                    '<div class="empty"><i class="clipboard list icon"></i>Nothing counted yet.</div>';
                return;
            }

            var summary = data.summary;
            var html = '<div class="stat-grid" style="padding:12px;">' +
                '<div class="stat"><div class="v">' + summary.lines + '</div><div class="k">Lines</div></div>' +
                '<div class="stat"><div class="v">' + summary.matched + '</div><div class="k">Match</div></div>' +
                '<div class="stat"><div class="v">' + summary.over + '</div><div class="k">Over</div></div>' +
                '<div class="stat"><div class="v">' + summary.under + '</div><div class="k">Short</div></div>' +
                "</div>";

            for (var i = 0; i < data.lines.length; i++) {
                var line = data.lines[i];
                var tone = line.variance === 0 ? "ok" : (line.variance > 0 ? "info" : "danger");
                html += '<div class="log-row">' +
                    '<div class="log-icon ' + (line.variance === 0 ? "in" : "out") + '">' +
                    '<i class="' + (line.variance === 0 ? "check" : "exclamation") + ' icon"></i></div>' +
                    '<div class="body"><div class="t">' + esc(line.name) +
                    (line.batchLabel ? " &middot; " + esc(line.batchLabel) : "") + "</div>" +
                    '<div class="s">counted ' + qtyText(line.counted) +
                    " &middot; system " + qtyText(line.expected) +
                    (line.location ? " &middot; " + esc(line.location) : "") +
                    (line.gone ? " &middot; item deleted mid-count" : "") + "</div></div>" +
                    '<span class="badge ' + tone + '">' +
                    (line.variance > 0 ? "+" : "") + qtyText(line.variance) + "</span></div>";
            }

            $("reviewBody").innerHTML = html;
            $("sheetFoot").style.display = "flex";
            $("sheetFoot").innerHTML =
                '<button class="btn" id="reviewKeep">Keep counting</button>' +
                '<button class="btn primary" id="reviewPost"><i class="check icon"></i>Post ' +
                summary.lines + " lines</button>";

            $("reviewKeep").onclick = closeSheet;
            $("reviewPost").onclick = function () {
                api("countCommit.agi", { close: "true" }, function (posted) {
                    state.counting = null;
                    state.countBatch = {};
                    closeSheet();
                    toast(posted.posted + " lines posted to stock", "ok");
                    load(false);
                });
            };
        });
    }

    function cancelBlindCount() {
        api("countCancel.agi", {}, function () {
            state.counting = null;
            state.countBatch = {};
            renderContext();
            setScanStatus(null, true);
            toast("Count discarded - no stock was changed", "");
            armInput();
        });
    }

    /* ── Cable runs ─────────────────────────────────────────────────────── */

    var RUN_STATES = [
        { id: "planned", label: "Planned", icon: "clipboard outline" },
        { id: "installed", label: "Installed", icon: "plug" },
        { id: "tested", label: "Tested", icon: "check circle" },
        { id: "faulty", label: "Faulty", icon: "exclamation triangle" },
        { id: "retired", label: "Retired", icon: "archive" }
    ];

    function runStateInfo(id) {
        for (var i = 0; i < RUN_STATES.length; i++) {
            if (RUN_STATES[i].id === id) return RUN_STATES[i];
        }
        return RUN_STATES[0];
    }

    function findRunByCode(code) {
        var needle = ("" + code).trim().toLowerCase();
        if (needle === "") return null;
        for (var i = 0; i < state.runs.length; i++) {
            var run = state.runs[i];
            if (("" + run.barcode).trim().toLowerCase() === needle) return run;
            if (("" + run.label).trim().toLowerCase() === needle) return run;
        }
        return null;
    }

    /* ── Scan view rendering ────────────────────────────────────────────── */

    var MODES = [
        { id: "lookup", label: "Look up", icon: "search" },
        { id: "in", label: "Stock in", icon: "plus" },
        { id: "out", label: "Stock out", icon: "minus" },
        { id: "move", label: "Move", icon: "exchange" },
        { id: "set", label: "Count", icon: "clipboard list" }
    ];

    function setMode(mode) {
        state.mode = mode;
        var buttons = document.querySelectorAll(".mode-btn");
        for (var i = 0; i < buttons.length; i++) {
            var isActive = buttons[i].getAttribute("data-mode") === mode;
            buttons[i].className = "mode-btn" + (isActive ? " active" : "");
        }
        renderContext();
        setScanStatus(null, true);
        armInput();
    }

    var STEP_CHOICES = [1, 2, 5, 10, 25];

    function renderContext() {
        var host = $("contextCard");
        var html = "";

        if (state.mode === "in" || state.mode === "out") {
            html += '<div class="context-label">Units per scan</div><div class="chip-row">';
            for (var i = 0; i < STEP_CHOICES.length; i++) {
                var value = STEP_CHOICES[i];
                html += '<button class="chip step-chip' + (state.step === value ? " active" : "") +
                    '" data-step="' + value + '">' + value + "</button>";
            }
            var isCustom = STEP_CHOICES.indexOf(state.step) === -1;
            html += '<button class="chip step-custom' + (isCustom ? " active" : "") + '">' +
                '<i class="pencil icon"></i>' + (isCustom ? qtyText(state.step) : "Other") + "</button>";
            html += "</div>";
        } else if (state.mode === "move") {
            html += '<div class="context-label">Move scanned items to</div>';
            html += '<select id="moveTarget" class="select-lg">';
            html += '<option value="">- pick a destination -</option>';
            for (var j = 0; j < state.locations.length; j++) {
                var name = state.locations[j];
                html += '<option value="' + esc(name) + '"' +
                    (name === state.moveTarget ? " selected" : "") + ">" + esc(name) + "</option>";
            }
            html += '<option value="__new__">+ New location...</option>';
            html += "</select>";
        } else if (state.mode === "set") {
            html += '<div class="context-label">Stock take</div><div class="chip-row">' +
                '<button class="chip count-method' + (state.countMethod === "keypad" ? " active" : "") +
                '" data-method="keypad"><i class="calculator icon"></i>Keypad</button>' +
                '<button class="chip count-method' + (state.countMethod === "blind" ? " active" : "") +
                '" data-method="blind"><i class="barcode icon"></i>Blind tally</button></div>';

            if (state.countMethod === "keypad") {
                html += '<div class="muted" style="font-size:13px;margin-top:8px;">' +
                    "Scan an item, then key in the quantity you counted on the shelf.</div>";
            } else if (state.counting) {
                html += '<div class="count-live"><div class="t">' + esc(state.counting.name) + "</div>" +
                    '<div class="s">' + state.counting.lines +
                    (state.counting.lines === 1 ? " line counted" : " lines counted") +
                    " &middot; one scan is one " + esc(state.step === 1 ? "unit" : qtyText(state.step) + " units") +
                    "</div></div>" +
                    '<div class="btn-row" style="margin-top:8px;">' +
                    '<button class="btn primary small" id="countReviewBtn"><i class="clipboard check icon"></i>Review</button>' +
                    '<button class="btn small" id="countCancelBtn"><i class="close icon"></i>Discard</button>' +
                    "</div>";
            } else {
                html += '<div class="muted" style="font-size:13px;margin-top:8px;">' +
                    "Keep scanning and each pull adds one. You will not see the system quantity " +
                    "until you review, which is what keeps the count honest.</div>" +
                    '<button class="btn primary block small" id="countStartBtn" style="margin-top:8px;">' +
                    '<i class="play icon"></i>Start a blind count</button>';
            }
        } else {
            html += '<div class="context-label">Look up</div>' +
                '<div class="muted" style="font-size:13px;">Scan an item to see its price, location, expiry and warranty. Nothing is changed.</div>';
        }

        host.innerHTML = html;

        var select = $("moveTarget");
        if (select) {
            select.addEventListener("change", function () {
                if (select.value === "__new__") {
                    promptNewLocation(function (name) {
                        state.moveTarget = name;
                        renderContext();
                    });
                    select.value = state.moveTarget;
                } else {
                    state.moveTarget = select.value;
                }
                armInput();
            });
        }
    }

    function setScanStatus(message, listening) {
        var box = $("scanBox");
        var text = $("scanStatusText");
        if ((message === null || message === undefined) && !state.loaded) {
            text.textContent = "Loading inventory...";
            box.className = "scan-box";
            return;
        }

        if ((message === null || message === undefined) && InvPairing.isRemote()) {
            var pairing = state.pairing;
            var hostMode = pairing && pairing.hostMode ? MODE_LABELS[pairing.hostMode] : "";
            text.textContent = hostMode
                ? "Ready - scan goes to the desktop (" + hostMode + ")"
                : "Ready - scan goes to the desktop";
            box.className = "scan-box" + (listening ? " listening" : "");
            return;
        }

        if (message === null || message === undefined) {
            var labels = {
                lookup: "Ready - scan to look up",
                in: "Ready - scan to add " + qtyText(state.step),
                out: "Ready - scan to remove " + qtyText(state.step),
                move: state.moveTarget ? ("Ready - scan to move to " + state.moveTarget) : "Pick a destination first",
                set: state.countMethod === "blind"
                    ? (state.counting
                        ? "Counting - each scan adds " + qtyText(state.step)
                        : "Start a blind count to begin")
                    : "Ready - scan, then key in the counted quantity"
            };
            text.textContent = labels[state.mode] || "Ready";
        } else {
            text.textContent = message;
        }
        box.className = "scan-box" + (listening ? " listening" : "");
    }

    function flashScanBox(kind) {
        var box = $("scanBox");
        box.className = "scan-box flash-" + kind;
        setTimeout(function () { setScanStatus(null, true); }, 700);
    }

    /* ── Layout, on-screen keyboard and focus ───────────────────────────── */

    // Mirrors the breakpoint in css/app.css: below it the handheld stack, at
    // or above it the desktop sidebar + wide list layout.
    var DESKTOP_MIN_WIDTH = 900;

    function isDesktopLayout() {
        if (window.matchMedia) {
            return window.matchMedia("(min-width: " + DESKTOP_MIN_WIDTH + "px)").matches;
        }
        return window.innerWidth >= DESKTOP_MIN_WIDTH;
    }

    function isSheetOpen() {
        return $("sheet").className.indexOf("open") !== -1;
    }

    /*
        Resolves the on-screen-keyboard policy for this device. "auto" - the
        default - suppresses the IME wherever one exists, which is what keeps a
        handheld usable: the app re-arms a field after every scan, and without
        suppression that would throw the Android keyboard over half of a 4.7"
        screen on every single trigger pull.
    */
    function suppressSoftKeyboard() {
        var mode = state.settings.softKeyboard || "auto";
        if (mode === "always") return false;
        if (mode === "never") return true;
        return InvScanner.deviceHasSoftKeyboard();
    }

    /*
        inputmode="none" tells the browser the field is filled by hardware, so
        no on-screen keyboard is raised. Removing it restores normal typing.
    */
    function setFieldKeyboard(input, allowTyping) {
        if (!input) return;

        var current = input.getAttribute("inputmode");
        var wanted = allowTyping ? null : "none";
        // Rewriting the attribute makes the browser re-evaluate the input method
        // for a field that may be focused and armed, which on Android can hide
        // the caret or drop focus outright - so only write it when it differs.
        if (current === wanted) return;

        if (wanted === null) {
            input.removeAttribute("inputmode");
        } else {
            input.setAttribute("inputmode", wanted);
        }
    }

    /*
        Suppression applies to the scan box and nothing else.

        The scan box is the only field fed by hardware, so it is the only one
        that gains anything from keeping the on-screen keyboard down. Applying
        the same policy to the search box - or to any form field - just leaves
        the operator tapping a field that refuses to type, which is worse than
        an on-screen keyboard appearing. Tapping the scan box itself is read as
        "I want to type", and lifts the suppression for that field too.
    */
    /*
        A "scan field" is any box a barcode can legitimately land in: the scan
        box, the search box, an item's barcode, a batch lot number, a cable
        label. They all follow one rule, which resolves the awkward pair of
        requirements a handheld has:

          - armed without being tapped, so a trigger pull lands somewhere
          - no on-screen keyboard, because arming one would otherwise cover the
            screen every time the app re-armed it

        so the field is focused with the keyboard suppressed, and a tap on an
        already-focused field is read as "I want to type this one by hand" and
        lifts the suppression. Every other field - name, price, notes - is left
        completely alone and types normally on the first tap.
    */
    function scanFieldsIn(root) {
        return (root || document).querySelectorAll("input[data-scan]");
    }

    function firstScanField(root) {
        var fields = scanFieldsIn(root);
        return fields.length ? fields[0] : null;
    }

    function applyScanFieldKeyboard(input) {
        var typing = input.getAttribute("data-typing") === "1";
        setFieldKeyboard(input, !suppressSoftKeyboard() || typing);
    }

    function prepareScanField(input) {
        if (!input || input.getAttribute("data-scan-ready") === "1") return;
        input.setAttribute("data-scan-ready", "1");
        applyScanFieldKeyboard(input);

        // pointerdown, not focus: the attribute has to change before the focus
        // lands or the browser has already decided not to raise the keyboard
        input.addEventListener("pointerdown", function () {
            // Nothing to lift where there is no on-screen keyboard
            if (!InvScanner.deviceHasSoftKeyboard()) return;
            if (document.activeElement !== input) return;   // first tap = arm
            input.setAttribute("data-typing", "1");
            applyScanFieldKeyboard(input);
        });

        // Leaving puts it back to hardware-fed, so the next automatic arming
        // does not bring the keyboard up with it
        input.addEventListener("blur", function () {
            if (input.getAttribute("data-typing") !== "1") return;
            input.removeAttribute("data-typing");
            applyScanFieldKeyboard(input);
        });
    }

    /* Arms every scan field in a freshly rendered region and focuses the first */
    function prepareScanFields(root) {
        var fields = scanFieldsIn(root);
        for (var i = 0; i < fields.length; i++) prepareScanField(fields[i]);
        return fields.length ? fields[0] : null;
    }

    function applyKeyboardPolicy() {
        var fields = scanFieldsIn(document);
        for (var i = 0; i < fields.length; i++) applyScanFieldKeyboard(fields[i]);

        // Nothing to toggle on a device without an on-screen keyboard
        var scanBtn = $("kbToggle");
        if (scanBtn) {
            scanBtn.style.display = InvScanner.deviceHasSoftKeyboard() ? "" : "none";
            scanBtn.className = "btn square" +
                (suppressSoftKeyboard() ? "" : " primary");
        }
    }

    /*
        Re-arms the field that should receive the next trigger pull for the
        current view. Called after every operation, view change, sheet close,
        window focus and tap, plus a watchdog: a handheld that has quietly lost
        focus drops the next scan, which is the worst failure mode this app has.
    */
    function armInput() {
        var target = null;

        if (isSheetOpen()) {
            // An open sheet owns the wedge: arm its own first scan field, so a
            // barcode can be scanned into an item's editor without tapping it
            target = firstScanField($("sheetBody"));
            if (!target) return;
        } else if (state.view === "scan") {
            target = $("scanInput");
        } else if (state.view === "search") {
            target = $("searchInput");
        }
        if (!target) return;

        claimWindowFocus();

        if (document.activeElement === target) return;
        try { target.focus(); } catch (e) {}
    }

    /*
        In an ArozOS float window the app runs inside an iframe. Until that frame
        holds the window focus, key events go to the parent document no matter
        what has DOM focus inside here - which is exactly why the first scan
        after opening the app can go nowhere. Only claimed when this document
        does not already have focus, so a desktop with several windows open
        never has focus yanked out from under it.
    */
    function claimWindowFocus() {
        try {
            if (document.hasFocus && !document.hasFocus() && window.focus) window.focus();
        } catch (e) {}
    }

    /*
        Focus can be lost outright with nothing to notice it - an Android IME
        closing, a system dialog dismissed, a tap on dead space - so poll for
        the case where nothing at all holds focus and hand it back.
    */
    function startFocusWatchdog() {
        setInterval(function () {
            if (isSheetOpen()) return;
            // Re-arming collapses a selection, and copying a barcode out of the
            // list is normal desktop work - leave the operator alone until done
            if (hasTextSelection()) return;

            var active = document.activeElement;
            // Never yank focus off a control the operator is actually using
            if (active && active !== document.body && active !== document.documentElement) return;
            armInput();
        }, 700);
    }

    function hasTextSelection() {
        try {
            var selection = window.getSelection();
            return !!selection && !selection.isCollapsed && ("" + selection).length > 0;
        } catch (e) {
            return false;
        }
    }

    function showResult(result) {
        state.lastResult = result;
        var host = $("scanResult");

        if (result.type === "unknown") {
            flashScanBox("err");

            var known = result.catalogue && result.catalogue.found;
            var hints = "";
            if (result.catalogue) {
                if (result.catalogue.origin) {
                    hints += '<span class="badge info"><i class="globe icon"></i>' +
                        esc(result.catalogue.origin) + "</span>";
                }
                if (result.catalogue.checkDigitOk === false) {
                    // A failed check digit almost always means a misread label,
                    // not a new product - worth saying before an item is created
                    hints += '<span class="badge danger"><i class="exclamation triangle icon"></i>' +
                        "Check digit fails - possible misread</span>";
                }
            }

            host.innerHTML =
                '<div class="card result-card ' + (known ? "op-set" : "op-error") + '">' +
                '<div class="result-name">' +
                (known ? esc(result.catalogue.name) : "Unknown barcode") + "</div>" +
                '<div class="result-sub">' + esc(result.barcode) + "</div>" +
                (known
                    ? '<div class="badges"><span class="badge ok"><i class="book icon"></i>' +
                      "Name from your catalogue - not in stock yet</span>" + hints + "</div>"
                    : (hints ? '<div class="badges">' + hints + "</div>" : "")) +
                '<div class="btn-row" style="margin-top:12px;">' +
                '<button class="btn primary" id="createFromScan"><i class="plus icon"></i>' +
                (known ? "Add with this name" : "Add this item") + "</button>" +
                '<button class="btn" id="searchFromScan"><i class="search icon"></i>Search</button>' +
                "</div></div>";
            $("createFromScan").onclick = function () {
                openItemEditor(null, result.barcode, known ? result.catalogue : null);
            };
            $("searchFromScan").onclick = function () {
                switchView("search");
                $("searchInput").value = result.barcode;
                state.query = result.barcode;
                renderSearch();
            };
            return;
        }

        if (result.type === "tally") {
            flashScanBox("ok");
            var line = result.line;
            host.innerHTML =
                '<div class="card result-card op-set">' +
                '<div class="result-head"><div class="body">' +
                '<div class="result-name">' + esc(line.name) + "</div>" +
                '<div class="result-sub">' + esc(line.barcode) +
                (line.batchLabel ? " &middot; batch " + esc(line.batchLabel) : "") + "</div>" +
                '<div class="badges"><span class="badge info"><i class="eye slash icon"></i>' +
                "Blind - system quantity hidden until review</span></div>" +
                '</div><div class="result-qty"><div class="value">' + qtyText(line.counted) + "</div>" +
                '<div class="unit">counted</div></div></div></div>';
            return;
        }

        if (result.type === "run") {
            flashScanBox("ok");
            var run = result.run;
            var info = runStateInfo(run.state);
            host.innerHTML =
                '<div class="card result-card op-move">' +
                '<div class="result-name">' + esc(run.label) + "</div>" +
                '<div class="result-sub">' + esc(run.cableType || "cable") +
                (run.length ? " &middot; " + qtyText(run.length) + " m" : "") + "</div>" +
                '<div class="badges"><span class="badge info"><i class="' + info.icon + ' icon"></i>' +
                esc(info.label) + "</span></div>" +
                '<div class="kv-grid">' +
                kv("From", (run.fromLocation || "-") + (run.fromPort ? " / " + run.fromPort : "")) +
                kv("To", (run.toLocation || "-") + (run.toPort ? " / " + run.toPort : "")) +
                "</div>" +
                '<button class="btn block" id="runOpen" style="margin-top:12px;">' +
                '<i class="pencil icon"></i>Open this run</button></div>';
            $("runOpen").onclick = function () { openRunEditor(run); };
            return;
        }

        if (result.type === "sending") {
            host.innerHTML =
                '<div class="card result-card">' +
                '<div class="result-name">Sending...</div>' +
                '<div class="result-sub">' + esc(result.barcode) + "</div></div>";
            return;
        }

        if (result.type === "sent") {
            flashScanBox("ok");
            host.innerHTML =
                '<div class="card result-card op-in">' +
                '<div class="result-head"><div class="body">' +
                '<div class="result-name">Sent to the desktop</div>' +
                '<div class="result-sub">' + esc(result.barcode) + "</div>" +
                '<div class="badges"><span class="badge info">' +
                '<i class="desktop icon"></i>' + esc(result.detail) + "</span></div>" +
                "</div></div></div>";
            return;
        }

        if (result.type === "error") {
            flashScanBox("err");
            host.innerHTML =
                '<div class="card result-card op-error">' +
                '<div class="result-name">Could not complete</div>' +
                '<div class="result-sub">' + esc(result.message) + "</div></div>";
            return;
        }

        flashScanBox(result.warning ? "warn" : "ok");

        var item = result.item;
        var alerts = itemAlerts(item);
        var deltaHtml = "";
        if (result.delta) {
            var up = result.delta > 0;
            deltaHtml = '<div class="delta ' + (up ? "up" : "down") + '">' +
                (up ? "+" : "") + qtyText(result.delta) + "</div>";
        }

        var expiryDays = daysUntil(item.expiryDate);
        var warrantyDays = daysUntil(item.warrantyEnd);
        var expiryClass = expiryDays === null ? "" : (expiryDays < 0 ? " danger" : (expiryDays <= state.settings.expiryWarnDays ? " warn" : ""));
        var warrantyClass = warrantyDays === null ? "" : (warrantyDays < 0 ? " danger" : (warrantyDays <= state.settings.warrantyWarnDays ? " warn" : ""));

        var html =
            '<div class="card result-card op-' + esc(result.type) + '">' +
            '<div class="result-head"><div class="body">' +
            '<div class="result-name">' + esc(item.name) + "</div>" +
            '<div class="result-sub">' + esc(item.barcode || "no barcode") +
            (item.sku ? " &middot; " + esc(item.sku) : "") + "</div>" +
            badgesHtml(alerts) +
            '</div><div class="result-qty"><div class="value">' + qtyText(item.qty) + "</div>" +
            '<div class="unit">' + esc(item.unit) + "</div>" + deltaHtml + "</div></div>";

        if (result.type === "move" && result.fromLocation) {
            html += '<div class="badges" style="margin-top:10px;"><span class="badge info">' +
                '<i class="exchange icon"></i>' + esc(result.fromLocation || "-") + " to " +
                esc(item.location) + "</span></div>";
        }

        html += '<div class="kv-grid">' +
            '<div class="kv"><div class="k">Location</div><div class="v">' + esc(item.location || "-") + "</div></div>" +
            '<div class="kv"><div class="k">Unit price</div><div class="v">' + money(item.price) + "</div></div>" +
            kv("Expiry", item.expiryDate || "-", expiryClass.replace(" ", ""),
                item.expiryDate ? relativeDays(expiryDays) : "") +
            kv("Warranty end", item.warrantyEnd || "-", warrantyClass.replace(" ", ""),
                item.warrantyEnd ? relativeDays(warrantyDays) : "") +
            '<div class="kv"><div class="k">Stock value</div><div class="v">' + money(item.price * item.qty) + "</div></div>" +
            '<div class="kv"><div class="k">Category</div><div class="v">' + esc(item.category || "-") + "</div></div>" +
            "</div>";

        html += '<div class="btn-row" style="margin-top:12px;">' +
            '<button class="btn" id="resultOpen"><i class="folder open icon"></i>Details</button>' +
            '<button class="btn" id="resultEdit"><i class="pencil icon"></i>Edit</button>' +
            "</div></div>";

        host.innerHTML = html;
        $("resultOpen").onclick = function () { openItemDetail(item.id); };
        $("resultEdit").onclick = function () { openItemEditor(findById(item.id), ""); };
    }

    function renderSession() {
        var host = $("sessionLog");
        if (!state.session.length) {
            host.innerHTML = "";
            return;
        }

        var html = '<div class="section-title">This session (' + state.session.length + ')</div>' +
            '<div class="card flush">';

        for (var i = 0; i < state.session.length && i < 25; i++) {
            var entry = state.session[i];
            var icons = { in: "plus", out: "minus", move: "exchange", set: "clipboard list" };
            var summary;
            if (entry.op === "move") {
                summary = (entry.prevLocation || "-") + " to " + (entry.newLocation || "-");
            } else {
                summary = qtyText(entry.prevQty) + " to " + qtyText(entry.qtyAfter) +
                    (entry.delta ? " (" + (entry.delta > 0 ? "+" : "") + qtyText(entry.delta) + ")" : "");
            }

            html += '<div class="log-row">' +
                '<div class="log-icon ' + esc(entry.op) + '"><i class="' + (icons[entry.op] || "dot circle") + ' icon"></i></div>' +
                '<div class="body"><div class="t">' + esc(entry.name) + "</div>" +
                '<div class="s">' + timeText(entry.ts) + " &middot; " + esc(summary) + "</div></div>";

            if (entry.undone) {
                html += '<span class="badge">undone</span>';
            } else {
                html += '<button class="btn small ghost undo" data-key="' + esc(entry.key) + '">' +
                    '<i class="undo icon"></i>Undo</button>';
            }
            html += "</div>";
        }

        html += "</div>" +
            '<button class="btn block ghost small" id="clearSession" style="margin-bottom:12px;">' +
            '<i class="eraser icon"></i>Clear session list</button>';

        host.innerHTML = html;

        var undoButtons = host.querySelectorAll("button.undo");
        for (var u = 0; u < undoButtons.length; u++) {
            undoButtons[u].onclick = function () {
                undoSession(this.getAttribute("data-key"));
            };
        }
        $("clearSession").onclick = function () {
            state.session = [];
            renderSession();
            armInput();
        };
    }

    /* ── Search view ────────────────────────────────────────────────────── */

    var FILTERS = [
        { id: "all", label: "All", test: null },
        { id: "alerts", label: "Needs attention", test: function (it) { return itemAlerts(it).length > 0; } },
        { id: "expiring", label: "Expiring", test: function (it) {
            var d = daysUntil(it.expiryDate);
            return d !== null && d <= state.settings.expiryWarnDays;
        } },
        { id: "warranty", label: "Warranty", test: function (it) {
            var d = daysUntil(it.warrantyEnd);
            return d !== null && d <= state.settings.warrantyWarnDays;
        } },
        { id: "low", label: "Low / out", test: function (it) {
            return it.qty <= 0 || (it.minQty > 0 && it.qty <= it.minQty);
        } }
    ];

    function activeFilter() {
        for (var i = 0; i < FILTERS.length; i++) {
            if (FILTERS[i].id === state.filter) return FILTERS[i];
        }
        return FILTERS[0];
    }

    function renderFilterChips() {
        var html = "";
        for (var i = 0; i < FILTERS.length; i++) {
            var f = FILTERS[i];
            var count = 0;
            if (f.test) {
                for (var j = 0; j < state.items.length; j++) {
                    if (f.test(state.items[j])) count++;
                }
            } else {
                count = state.items.length;
            }
            html += '<button class="chip filter-chip' + (state.filter === f.id ? " active" : "") +
                '" data-filter="' + f.id + '">' + esc(f.label) +
                '<span class="count">' + count + "</span></button>";
        }
        $("filterChips").innerHTML = html;

        var chips = document.querySelectorAll(".filter-chip");
        for (var c = 0; c < chips.length; c++) {
            chips[c].onclick = function () {
                state.filter = this.getAttribute("data-filter");
                renderSearch();
            };
        }
    }

    /*
        One row markup for both layouts. The .cell columns are hidden on the
        handheld, where the same facts are folded into the .meta line instead,
        and become real table columns once the desktop breakpoint applies.
    */
    function itemRowHtml(item, query) {
        var alerts = itemAlerts(item);
        var qtyClass = item.qty <= 0 ? " zero" : ((item.minQty > 0 && item.qty <= item.minQty) ? " low" : "");

        var meta = [];
        if (item.location) meta.push(InvSearch.highlight(item.location, query));
        if (item.barcode) meta.push(InvSearch.highlight(item.barcode, query));
        if (item.price) meta.push(esc(money(item.price)));

        var expiryDays = daysUntil(item.expiryDate);
        var warrantyDays = daysUntil(item.warrantyEnd);
        var dateCell = function (iso, days, warnDays, column) {
            if (!iso) return '<div class="cell ' + column + ' muted">-</div>';
            var tone = days < 0 ? " danger" : (days <= warnDays ? " warn" : "");
            return '<div class="cell ' + column + tone + '">' + esc(iso) +
                "<small>" + esc(relativeDays(days)) + "</small></div>";
        };

        return '<div class="item-row" data-id="' + esc(item.id) + '">' +
            '<div class="body"><div class="name">' + InvSearch.highlight(item.name, query) + "</div>" +
            '<div class="meta">' + (meta.length ? meta.join(" &middot; ") : "&nbsp;") + "</div>" +
            badgesHtml(alerts) + "</div>" +
            '<div class="cell col-barcode">' + InvSearch.highlight(item.barcode || "-", query) + "</div>" +
            '<div class="cell col-location">' + InvSearch.highlight(item.location || "-", query) + "</div>" +
            '<div class="cell col-price">' + esc(money(item.price)) + "</div>" +
            dateCell(item.expiryDate, expiryDays, state.settings.expiryWarnDays, "col-expiry") +
            dateCell(item.warrantyEnd, warrantyDays, state.settings.warrantyWarnDays, "col-warranty") +
            '<div class="qty' + qtyClass + '">' + qtyText(item.qty) + "<small>" + esc(item.unit) + "</small></div>" +
            "</div>";
    }

    /* Column captions, shown only in the desktop table layout */
    function listHeadHtml() {
        return '<div class="list-head">' +
            '<div class="body">Item</div>' +
            '<div class="cell col-barcode">Barcode</div>' +
            '<div class="cell col-location">Location</div>' +
            '<div class="cell col-price">Price</div>' +
            '<div class="cell col-expiry">Expiry</div>' +
            '<div class="cell col-warranty">Warranty</div>' +
            '<div class="qty">Qty</div>' +
            "</div>";
    }

    function bindItemRows(host) {
        var rows = host.querySelectorAll(".item-row");
        for (var i = 0; i < rows.length; i++) {
            rows[i].onclick = function () {
                openItemDetail(this.getAttribute("data-id"));
            };
        }
    }

    function renderSearch() {
        renderFilterChips();

        var filter = activeFilter();
        var results = InvSearch.search(state.items, state.query, {
            limit: 300,
            filter: filter.test
        });

        var meta = $("searchMeta");
        if (state.query) {
            meta.textContent = results.length + (results.length === 1 ? " match" : " matches") + ' for "' + state.query + '"';
        } else {
            meta.textContent = results.length + (results.length === 1 ? " item" : " items");
        }

        // Kept so Enter and the arrow keys can act on exactly what is shown
        state.searchResults = results;
        state.searchIndex = -1;

        var host = $("searchResults");
        if (!results.length) {
            host.innerHTML = '<div class="empty"><i class="box icon"></i>' +
                (state.query ? "Nothing matches that search." : "No items yet - scan a barcode to add your first one.") +
                "</div>";
            return;
        }

        var html = listHeadHtml();
        for (var i = 0; i < results.length; i++) {
            html += itemRowHtml(results[i].item, state.query);
        }
        host.innerHTML = html;
        bindItemRows(host);
    }

    /*
        Moves the highlight through the result list. Desktop keyboard driving:
        type, arrow down to the right row, Enter to open it - no mouse needed.
    */
    function moveSearchSelection(delta) {
        var rows = $("searchResults").querySelectorAll(".item-row");
        if (!rows.length) return;

        var next = state.searchIndex + delta;
        if (next < 0) next = 0;
        if (next > rows.length - 1) next = rows.length - 1;
        state.searchIndex = next;

        for (var i = 0; i < rows.length; i++) {
            rows[i].className = "item-row" + (i === next ? " selected" : "");
        }
        if (rows[next].scrollIntoView) {
            rows[next].scrollIntoView({ block: "nearest" });
        }
    }

    /* The item Enter should act on: the highlighted row, else the top hit */
    function currentSearchItem() {
        if (!state.searchResults.length) return null;
        var index = state.searchIndex >= 0 ? state.searchIndex : 0;
        var hit = state.searchResults[index];
        return hit ? hit.item : null;
    }

    /* ── Cables view: installed runs ────────────────────────────────────── */

    function renderCables() {
        var counts = { all: state.runs.length };
        var i;
        for (i = 0; i < RUN_STATES.length; i++) counts[RUN_STATES[i].id] = 0;
        for (i = 0; i < state.runs.length; i++) {
            if (counts[state.runs[i].state] !== undefined) counts[state.runs[i].state]++;
        }

        var chips = '<button class="chip run-chip' + (state.runFilter === "all" ? " active" : "") +
            '" data-run-filter="all">All<span class="count">' + counts.all + "</span></button>";
        for (i = 0; i < RUN_STATES.length; i++) {
            if (!counts[RUN_STATES[i].id]) continue;
            chips += '<button class="chip run-chip' +
                (state.runFilter === RUN_STATES[i].id ? " active" : "") +
                '" data-run-filter="' + RUN_STATES[i].id + '">' + esc(RUN_STATES[i].label) +
                '<span class="count">' + counts[RUN_STATES[i].id] + "</span></button>";
        }

        var visible = [];
        for (i = 0; i < state.runs.length; i++) {
            if (state.runFilter === "all" || state.runs[i].state === state.runFilter) {
                visible.push(state.runs[i]);
            }
        }
        visible.sort(function (a, b) {
            return ("" + a.label).toLowerCase() < ("" + b.label).toLowerCase() ? -1 : 1;
        });

        var html =
            '<button class="btn primary block" id="runAdd" style="margin-bottom:12px;">' +
            '<i class="plus icon"></i>New cable run</button>' +
            '<div class="chip-row" style="margin-bottom:10px;">' +
            '<button class="chip cable-view' + (state.cableView === "list" ? " active" : "") +
            '" data-cable-view="list"><i class="list icon"></i>List</button>' +
            '<button class="chip cable-view' + (state.cableView === "diagram" ? " active" : "") +
            '" data-cable-view="diagram"><i class="sitemap icon"></i>Diagram</button>' +
            "</div>" +
            '<div class="chip-row">' + chips + "</div>";

        if (state.cableView === "diagram") {
            html += diagramHtml(visible);
            $("cablesBody").innerHTML = html;
            bindCablesChrome();
            bindDiagram(visible);
            return;
        }

        html += '<div class="section-title">' + visible.length +
            (visible.length === 1 ? " run" : " runs") + "</div>";

        if (!visible.length) {
            html += '<div class="card"><div class="empty"><i class="plug icon"></i>' +
                (state.runs.length
                    ? "No runs in that state."
                    : "No cable runs recorded yet.<br>A run is one installed cable and what each end plugs into.") +
                "</div></div>";
        } else {
            html += '<div class="card flush" id="runList">' + runListHeadHtml();
            for (i = 0; i < visible.length; i++) html += runRowHtml(visible[i]);
            html += "</div>";
        }

        $("cablesBody").innerHTML = html;
        bindCablesChrome();

        if ($("runList")) {
            var rows = $("runList").querySelectorAll(".item-row");
            for (i = 0; i < rows.length; i++) {
                rows[i].onclick = function () {
                    var id = this.getAttribute("data-id");
                    for (var k = 0; k < state.runs.length; k++) {
                        if (state.runs[k].id === id) { openRunEditor(state.runs[k]); return; }
                    }
                };
            }
        }
    }

    function bindCablesChrome() {
        $("runAdd").onclick = function () { openRunEditor(null); };

        var chipNodes = document.querySelectorAll(".run-chip");
        for (var i = 0; i < chipNodes.length; i++) {
            chipNodes[i].onclick = function () {
                state.runFilter = this.getAttribute("data-run-filter");
                renderCables();
            };
        }
        var viewNodes = document.querySelectorAll(".cable-view");
        for (var v = 0; v < viewNodes.length; v++) {
            viewNodes[v].onclick = function () {
                state.cableView = this.getAttribute("data-cable-view");
                state.diagramFocus = "";
                renderCables();
            };
        }
    }

    /*
        The wiring picture. Locations are nodes, runs are the links between them,
        and a colour legend explains the states - which is the one thing a list of
        runs cannot show you.
    */
    function diagramHtml(runs) {
        var drawn = InvCableDiagram.render(runs, { highlight: state.diagramFocus });
        lastDiagram = drawn;

        if (!drawn.nodeCount) {
            return '<div class="card"><div class="empty"><i class="sitemap icon"></i>' +
                (runs.length
                    ? "These runs have no locations recorded, so there is nothing to draw."
                    : "No cable runs yet.<br>Record one and it appears here as a link between two locations.") +
                "</div></div>";
        }

        var legend = "";
        var states = [
            { id: "tested", label: "Tested" },
            { id: "installed", label: "Installed" },
            { id: "planned", label: "Planned" },
            { id: "faulty", label: "Faulty" },
            { id: "retired", label: "Retired" }
        ];
        for (var i = 0; i < states.length; i++) {
            legend += '<span class="cd-key"><span class="cd-swatch" style="background:' +
                InvCableDiagram.STATE_COLOUR[states[i].id] + '"></span>' +
                esc(states[i].label) + "</span>";
        }

        return '<div class="card diagram-card">' + drawn.svg + "</div>" +
            '<div class="cd-legend">' + legend + "</div>" +
            '<div class="hint">' + drawn.nodeCount +
            (drawn.nodeCount === 1 ? " location" : " locations") + ", " +
            drawn.linkCount + (drawn.linkCount === 1 ? " link" : " links") +
            ". Tap a location to follow just its cables, or a link to list the runs on it." +
            (drawn.dangling
                ? " " + drawn.dangling + " run" + (drawn.dangling === 1 ? "" : "s") +
                  " missing an end are not drawn."
                : "") +
            (drawn.truncated
                ? " " + drawn.truncated + " quieter locations were left out to keep it readable."
                : "") +
            "</div>" +
            (state.diagramFocus
                ? '<button class="btn block small" id="diagramAll" style="margin-top:10px;">' +
                  '<i class="close icon"></i>Show every location</button>'
                : "");
    }

    // The most recent render, so a tapped link can be resolved to its runs
    var lastDiagram = null;

    function bindDiagram(runs) {
        var i;
        var nodes = document.querySelectorAll(".cd-node");
        for (i = 0; i < nodes.length; i++) {
            nodes[i].onclick = function () {
                var name = this.getAttribute("data-location");
                // Tapping the focused location clears the focus again
                state.diagramFocus =
                    InvCableDiagram.locationKey(state.diagramFocus) === InvCableDiagram.locationKey(name)
                        ? "" : name;
                renderCables();
            };
        }

        var links = document.querySelectorAll(".cd-link, .cd-label");
        for (i = 0; i < links.length; i++) {
            links[i].onclick = function () {
                openLinkRuns(parseInt(this.getAttribute("data-pair"), 10));
            };
        }

        if ($("diagramAll")) {
            $("diagramAll").onclick = function () {
                state.diagramFocus = "";
                renderCables();
            };
        }
    }

    /* The runs sitting on one link of the diagram */
    function openLinkRuns(index) {
        if (!lastDiagram || isNaN(index)) return;
        var bundle = lastDiagram.bundles[index];
        if (!bundle || !bundle.runs.length) return;

        var html = '<div class="card flush" id="linkRuns">' + runListHeadHtml();
        for (var j = 0; j < bundle.runs.length; j++) html += runRowHtml(bundle.runs[j]);
        html += "</div>";

        openSheet(bundle.from + " to " + bundle.to, html, "");

        var rows = $("linkRuns").querySelectorAll(".item-row");
        for (var r = 0; r < rows.length; r++) {
            rows[r].onclick = function () {
                var id = this.getAttribute("data-id");
                for (var k = 0; k < state.runs.length; k++) {
                    if (state.runs[k].id === id) { openRunEditor(state.runs[k]); return; }
                }
            };
        }
    }

    function runListHeadHtml() {
        return '<div class="list-head">' +
            '<div class="body">Label</div>' +
            '<div class="cell col-barcode">Type</div>' +
            '<div class="cell col-location">From</div>' +
            '<div class="cell col-price">To</div>' +
            '<div class="cell col-expiry">State</div>' +
            '<div class="qty">Length</div>' +
            "</div>";
    }

    function runRowHtml(run) {
        var info = runStateInfo(run.state);
        var from = (run.fromLocation || "-") + (run.fromPort ? " / " + run.fromPort : "");
        var to = (run.toLocation || "-") + (run.toPort ? " / " + run.toPort : "");
        var tone = run.state === "faulty" ? " danger" : (run.state === "tested" ? "" : "");

        return '<div class="item-row" data-id="' + esc(run.id) + '">' +
            '<div class="body"><div class="name">' + esc(run.label) + "</div>" +
            '<div class="meta">' + esc(run.cableType || "cable") + " &middot; " +
            esc(from) + " to " + esc(to) + "</div>" +
            '<div class="badges"><span class="badge' +
            (run.state === "faulty" ? " danger" : (run.state === "tested" ? " ok" : "")) +
            '"><i class="' + info.icon + ' icon"></i>' + esc(info.label) + "</span></div></div>" +
            '<div class="cell col-barcode">' + esc(run.cableType || "-") + "</div>" +
            '<div class="cell col-location">' + esc(from) + "</div>" +
            '<div class="cell col-price" style="text-align:left;">' + esc(to) + "</div>" +
            '<div class="cell col-expiry' + tone + '">' + esc(info.label) +
            (run.testedOn ? "<small>" + esc(run.testedOn) + "</small>" : "") + "</div>" +
            '<div class="qty">' + (run.length ? qtyText(run.length) : "-") +
            "<small>m</small></div></div>";
    }

    function openRunEditor(run) {
        var isNew = !run;
        var r = run || {
            id: "", label: "", barcode: "", cableType: "", length: 0,
            fromLocation: "", fromPort: "", toLocation: "", toPort: "",
            state: "planned", itemId: "", testedOn: "", notes: ""
        };

        var stateOptions = "";
        for (var i = 0; i < RUN_STATES.length; i++) {
            stateOptions += '<option value="' + RUN_STATES[i].id + '"' +
                (RUN_STATES[i].id === r.state ? " selected" : "") + ">" +
                esc(RUN_STATES[i].label) + "</option>";
        }

        // Cable stock items can be named as the source this run was drawn from
        var sourceOptions = '<option value="">- not linked -</option>';
        for (var j = 0; j < state.items.length; j++) {
            if (state.items[j].kind !== "cable") continue;
            sourceOptions += '<option value="' + esc(state.items[j].id) + '"' +
                (state.items[j].id === r.itemId ? " selected" : "") + ">" +
                esc(state.items[j].name) + "</option>";
        }

        var html =
            '<div class="field"><label>Label on the cable</label>' +
            '<input type="text" id="rLabel" data-scan="1" value="' + esc(r.label) + '" placeholder="LAN-0142">' +
            '<div class="hint">' + scanFieldHint() +
            "What is printed or written on it. Must be unique.</div></div>" +

            '<div class="field"><label>Barcode</label>' +
            '<input type="text" id="rBarcode" data-scan="1" autocomplete="off" value="' + esc(r.barcode) + '">' +
            '<div class="hint">Optional. Scan it in Look up to jump straight to this run.</div></div>' +

            '<div class="field-row">' +
            '<div class="field"><label>Cable type</label>' +
            '<input type="text" id="rType" value="' + esc(r.cableType) + '" placeholder="Cat6a"></div>' +
            '<div class="field"><label>Length (m)</label>' +
            '<input type="number" inputmode="decimal" step="any" min="0" id="rLength" value="' + esc(r.length) + '"></div>' +
            "</div>" +

            '<div class="section-title">A end</div>' +
            '<div class="field-row">' +
            '<div class="field"><label>Location</label>' +
            '<input type="text" id="rFromLoc" value="' + esc(r.fromLocation) + '" placeholder="Rack A"></div>' +
            '<div class="field"><label>Port</label>' +
            '<input type="text" id="rFromPort" value="' + esc(r.fromPort) + '" placeholder="SW1 port 24"></div>' +
            "</div>" +

            '<div class="section-title">B end</div>' +
            '<div class="field-row">' +
            '<div class="field"><label>Location</label>' +
            '<input type="text" id="rToLoc" value="' + esc(r.toLocation) + '" placeholder="Room 3"></div>' +
            '<div class="field"><label>Port</label>' +
            '<input type="text" id="rToPort" value="' + esc(r.toPort) + '" placeholder="Wall plate B"></div>' +
            "</div>" +

            '<div class="field-row">' +
            '<div class="field"><label>State</label><select id="rState">' + stateOptions + "</select></div>" +
            '<div class="field"><label>Tested on</label>' +
            '<input type="date" id="rTested" value="' + esc(r.testedOn) + '"></div>' +
            "</div>" +

            '<div class="field"><label>Drawn from stock item</label>' +
            '<select id="rItem">' + sourceOptions + "</select>" +
            '<div class="hint">Only items marked as cables appear here.</div></div>' +

            '<div class="field"><label>Notes</label>' +
            '<textarea id="rNotes">' + esc(r.notes) + "</textarea></div>";

        if (!isNew) {
            html += '<button class="btn danger block" id="rDelete"><i class="trash icon"></i>Delete this run</button>';
        }

        openSheet(isNew ? "New cable run" : r.label, html,
            '<button class="btn" id="rCancel">Cancel</button>' +
            '<button class="btn primary" id="rSave"><i class="save icon"></i>Save</button>');

        $("rCancel").onclick = closeSheet;
        $("rSave").onclick = function () {
            var payload = {
                id: r.id,
                label: $("rLabel").value,
                barcode: $("rBarcode").value,
                cableType: $("rType").value,
                length: $("rLength").value,
                fromLocation: $("rFromLoc").value,
                fromPort: $("rFromPort").value,
                toLocation: $("rToLoc").value,
                toPort: $("rToPort").value,
                state: $("rState").value,
                testedOn: $("rTested").value,
                itemId: $("rItem").value,
                notes: $("rNotes").value,
                createdAt: r.createdAt
            };
            if (!payload.label.trim() && !payload.barcode.trim()) {
                toast("Give the run a label", "err");
                return;
            }
            api("saveRun.agi", { runData: JSON.stringify(payload) }, function (data) {
                state.runs = data.runs;
                closeSheet();
                toast(isNew ? "Cable run added" : "Cable run saved", "ok");
                updateSubtitle();
                if (state.view === "cables") renderCables();
            });
        };

        if (!isNew) {
            $("rDelete").onclick = function () {
                confirmSheet("Delete " + r.label + "?",
                    "The run is removed from the register. Stock is not affected.",
                    function () {
                        api("deleteRun.agi", { runId: r.id }, function (data) {
                            state.runs = data.runs;
                            closeSheet();
                            toast("Cable run deleted", "ok");
                            updateSubtitle();
                            if (state.view === "cables") renderCables();
                        });
                    },
                    function () { openRunEditor(r); });
            };
        }
    }

    /* ── Alerts view ────────────────────────────────────────────────────── */

    function renderAlerts() {
        var groups = [
            { key: "expired", title: "Expired", icon: "hourglass end" },
            { key: "expiring", title: "Expiring soon", icon: "hourglass half" },
            { key: "out", title: "Out of stock", icon: "ban" },
            { key: "low", title: "Low stock", icon: "battery low" },
            { key: "warrantyOver", title: "Warranty ended", icon: "shield alternate" },
            { key: "warrantyEnding", title: "Warranty ending soon", icon: "shield alternate" }
        ];

        var buckets = {};
        var i;
        for (i = 0; i < groups.length; i++) buckets[groups[i].key] = [];

        for (i = 0; i < state.items.length; i++) {
            var alerts = itemAlerts(state.items[i]);
            for (var a = 0; a < alerts.length; a++) {
                if (buckets[alerts[a].kind]) buckets[alerts[a].kind].push(state.items[i]);
            }
        }

        var html = "";
        var any = false;

        for (i = 0; i < groups.length; i++) {
            var group = groups[i];
            var list = buckets[group.key];
            if (!list.length) continue;
            any = true;

            // Soonest deadline first inside each group - that is the work queue
            if (group.key === "expired" || group.key === "expiring") {
                list.sort(function (x, y) { return (daysUntil(x.expiryDate) || 0) - (daysUntil(y.expiryDate) || 0); });
            } else if (group.key === "warrantyOver" || group.key === "warrantyEnding") {
                list.sort(function (x, y) { return (daysUntil(x.warrantyEnd) || 0) - (daysUntil(y.warrantyEnd) || 0); });
            } else {
                list.sort(function (x, y) { return x.qty - y.qty; });
            }

            html += '<div class="section-title"><i class="' + group.icon + ' icon"></i> ' +
                esc(group.title) + " (" + list.length + ")</div><div class=\"card flush\">" +
                listHeadHtml();
            for (var j = 0; j < list.length; j++) {
                html += itemRowHtml(list[j], "");
            }
            html += "</div>";
        }

        if (!any) {
            html = '<div class="empty"><i class="check circle outline icon"></i>' +
                "Nothing needs attention.<br>No expiries, warranties or stock levels are due.</div>";
        }

        var host = $("alertsBody");
        host.innerHTML = html;
        bindItemRows(host);
    }

    function renderAlertBadge() {
        var count = alertCount();
        var badge = $("alertBadge");
        badge.textContent = count > 99 ? "99+" : count;
        badge.className = "nav-badge" + (count === 0 ? " hidden" : "");
    }

    /* ── More view: stats, locations, settings, export ──────────────────── */

    function renderMore() {
        var totalUnits = 0;
        var totalValue = 0;
        for (var i = 0; i < state.items.length; i++) {
            totalUnits += state.items[i].qty;
            totalValue += state.items[i].qty * state.items[i].price;
        }

        var html =
            '<div class="section-title">Overview</div>' +
            '<div class="card"><div class="stat-grid">' +
            '<div class="stat"><div class="v">' + state.items.length + '</div><div class="k">Item types</div></div>' +
            '<div class="stat"><div class="v">' + qtyText(totalUnits) + '</div><div class="k">Units on hand</div></div>' +
            '<div class="stat"><div class="v">' + esc(money(totalValue)) + '</div><div class="k">Stock value</div></div>' +
            '<div class="stat"><div class="v">' + state.locations.length + '</div><div class="k">Locations</div></div>' +
            "</div></div>";

        html +=
            '<div class="section-title">Actions</div>' +
            '<div class="card"><div class="btn-row" style="margin-bottom:8px;">' +
            '<button class="btn primary" id="moreAddItem"><i class="plus icon"></i>New item</button>' +
            '<button class="btn" id="moreLocations"><i class="map marker alternate icon"></i>Locations</button>' +
            "</div>" +
            '<div class="btn-row" style="margin-bottom:8px;">' +
            '<button class="btn" id="moreHistory"><i class="history icon"></i>Full history</button>' +
            '<button class="btn" id="moreReload"><i class="sync icon"></i>Reload</button>' +
            "</div>" +
            '<div class="btn-row" style="margin-bottom:8px;">' +
            '<button class="btn" id="exportItems"><i class="file excel outline icon"></i>Export items</button>' +
            '<button class="btn" id="exportMoves"><i class="file alternate outline icon"></i>Export log</button>' +
            "</div>" +
            '<div class="btn-row">' +
            '<button class="btn" id="exportBatches"><i class="tags icon"></i>Export batches</button>' +
            '<button class="btn" id="exportRuns"><i class="plug icon"></i>Export cable runs</button>' +
            "</div></div>";

        html +=
            '<div class="section-title">Product catalogue</div><div class="card">' +
            '<div class="context-label">' + state.catalogueSize +
            (state.catalogueSize === 1 ? " barcode known" : " barcodes known") +
            ". Naming an item teaches this list, so the next time the same UPC or JAN " +
            "is scanned the name comes up on its own. Everything stays on this server - " +
            "no barcode is ever sent anywhere.</div>" +
            '<button class="btn block" id="catImport"><i class="upload icon"></i>' +
            "Import a UPC / JAN list</button></div>";

        var pairing = state.pairing || { role: "none", devices: [] };
        var pairSummary;
        if (pairing.role === "host") {
            var online = 0;
            for (var d = 0; d < pairing.devices.length; d++) {
                if (pairing.devices[d].online) online++;
            }
            pairSummary = "Receiving scans, code " + pairing.code + " - " +
                (online ? online + " handheld(s) connected" : "waiting for a handheld");
        } else if (pairing.role === "remote") {
            pairSummary = "Sending every scan to the desktop on code " + pairing.code;
        } else {
            pairSummary = "Scan on one device and have the barcode arrive on another - " +
                "read with the handheld, act on the desktop.";
        }

        html +=
            '<div class="section-title">Remote scanner</div><div class="card">' +
            '<div class="context-label">' + esc(pairSummary) + "</div>" +
            '<button class="btn primary block" id="morePairing">' +
            '<i class="exchange icon"></i>' +
            (pairing.role === "none" ? "Set up remote scanning" : "Manage remote scanning") +
            "</button></div>";

        var s = state.settings;
        html +=
            '<div class="section-title">Scanner</div><div class="card">' +
            switchHtml("scanAnywhere", "Capture scans anywhere", "Read the wedge even when the scan box has lost focus", s.scanAnywhere) +
            switchHtml("beep", "Beep on scan", "Audible confirm / warn / error tones", s.beep) +
            switchHtml("vibrate", "Vibrate on scan", "Haptic feedback through the handheld", s.vibrate) +
            "</div>";

        var kbModes = [
            { id: "auto", label: "Automatic" },
            { id: "always", label: "Always show" },
            { id: "never", label: "Never show" }
        ];
        var kbHtml = '<div class="chip-row">';
        for (var k = 0; k < kbModes.length; k++) {
            kbHtml += '<button class="chip kb-mode' + (s.softKeyboard === kbModes[k].id ? " active" : "") +
                '" data-kbmode="' + kbModes[k].id + '">' + esc(kbModes[k].label) + "</button>";
        }
        kbHtml += "</div>";

        html +=
            '<div class="section-title">On-screen keyboard</div><div class="card">' +
            '<div class="context-label">' +
            (InvScanner.deviceHasSoftKeyboard()
                ? "Applies to the scan box only. Automatic keeps the keyboard down while the box waits for the hardware scanner, so it never covers the screen - tap the box and it comes up so you can type a code by hand. Search and every other field always type normally."
                : "No on-screen keyboard on this device, so every field types normally.") +
            "</div>" + kbHtml +
            '<div class="hint" style="margin-top:8px;">Currently: ' +
            (suppressSoftKeyboard() ? "suppressed" : "allowed") + "</div></div>";

        html +=
            '<div class="section-title">Thresholds</div><div class="card">' +
            '<div class="field-row">' +
            '<div class="field"><label>Expiry warning (days)</label>' +
            '<input type="number" inputmode="numeric" min="0" id="setExpiryWarn" value="' + esc(s.expiryWarnDays) + '"></div>' +
            '<div class="field"><label>Warranty warning (days)</label>' +
            '<input type="number" inputmode="numeric" min="0" id="setWarrantyWarn" value="' + esc(s.warrantyWarnDays) + '"></div>' +
            "</div>" +
            '<div class="field-row">' +
            '<div class="field"><label>Currency symbol</label>' +
            '<input type="text" id="setCurrency" maxlength="4" value="' + esc(s.currency) + '"></div>' +
            '<div class="field"><label>Default step</label>' +
            '<input type="number" inputmode="numeric" min="1" id="setStep" value="' + esc(s.defaultStep) + '"></div>' +
            "</div>" +
            '<button class="btn primary block" id="saveThresholds"><i class="save icon"></i>Save</button>' +
            "</div>";

        html +=
            '<div class="section-title">How to use it</div><div class="card muted" style="font-size:13px;line-height:1.55;">' +
            "<p style=\"margin-top:0;\"><b>On a handheld</b> (FZ-N1 and similar): set the scanner service to " +
            "<b>keyboard wedge</b> output with an <b>Enter</b> suffix, then open this app full screen. The field " +
            "you are scanning into re-arms itself after every operation, and the on-screen keyboard is kept down " +
            "automatically so it never covers the screen - just pull the trigger.</p>" +
            "<p>Pick the operation first (Look up, Stock in, Stock out, Move, Count), then keep scanning: " +
            "every trigger pull applies that operation, so a whole pallet is one mode change and N pulls. " +
            "Scanning while the Find tab is open looks the item up instead of changing it.</p>" +
            "<p><b>On a desktop</b> with a USB or Bluetooth scanner the same flow works, plus shortcuts: " +
            "<b>F1-F5</b> pick the mode, <b>Ctrl+F</b> jumps to Find, <b>Ctrl+N</b> adds an item, arrow keys " +
            "walk the results, <b>Enter</b> opens the highlighted one and <b>Esc</b> closes.</p>" +
            "<p style=\"margin-bottom:0;\">Data lives in <code>user:/Document/Inventory/</code> and exports land in " +
            "<code>user:/Document/Inventory/exports/</code>.</p></div>";

        $("moreBody").innerHTML = html;

        $("moreAddItem").onclick = function () { openItemEditor(null, ""); };
        $("morePairing").onclick = openPairingSheet;
        $("moreLocations").onclick = openLocationManager;
        $("moreHistory").onclick = openHistory;
        $("moreReload").onclick = function () { load(true); };
        $("exportItems").onclick = function () { runExport("items"); };
        $("exportMoves").onclick = function () { runExport("movements"); };
        $("exportBatches").onclick = function () { runExport("batches"); };
        $("exportRuns").onclick = function () { runExport("runs"); };
        $("catImport").onclick = openCatalogueImport;

        bindSwitches();

        var kbButtons = document.querySelectorAll(".kb-mode");
        for (var m = 0; m < kbButtons.length; m++) {
            kbButtons[m].onclick = function () {
                saveSettings({ softKeyboard: this.getAttribute("data-kbmode") });
                renderMore();
            };
        }

        $("saveThresholds").onclick = function () {
            var next = {
                expiryWarnDays: parseInt($("setExpiryWarn").value, 10),
                warrantyWarnDays: parseInt($("setWarrantyWarn").value, 10),
                currency: $("setCurrency").value,
                defaultStep: parseInt($("setStep").value, 10)
            };
            if (isNaN(next.expiryWarnDays) || isNaN(next.warrantyWarnDays) || isNaN(next.defaultStep)) {
                toast("Those numbers do not look right", "err");
                return;
            }
            saveSettings(next);
        };
    }

    function switchHtml(key, label, hint, value) {
        return '<div class="switch-row"><div class="label">' + esc(label) +
            "<small>" + esc(hint) + "</small></div>" +
            '<div class="switch' + (value ? " on" : "") + '" data-setting="' + key + '"></div></div>';
    }

    function bindSwitches() {
        var switches = document.querySelectorAll(".switch[data-setting]");
        for (var i = 0; i < switches.length; i++) {
            switches[i].onclick = function () {
                var key = this.getAttribute("data-setting");
                var next = {};
                next[key] = !state.settings[key];
                this.className = "switch" + (next[key] ? " on" : "");
                saveSettings(next);
            };
        }
    }

    function saveSettings(changes) {
        for (var key in changes) state.settings[key] = changes[key];
        applySettings();
        api("saveSettings.agi", { settingsJson: JSON.stringify(state.settings) }, function (data) {
            if (data.settings) state.settings = data.settings;
            applySettings();
        });
    }

    function applySettings() {
        InvScanner.applySettings(state.settings);
        applyKeyboardPolicy();
        renderAlertBadge();
    }

    function runExport(kind) {
        toast("Exporting...", "");
        api("exportCsv.agi", { kind: kind }, function (data) {
            toast(data.rows + " rows written", "ok");
            if (typeof ao_module_openPath === "function") {
                ao_module_openPath(data.path.substring(0, data.path.lastIndexOf("/")));
            }
        });
    }

    /*
        Import a barcode list. Deliberately offers a paste box and a path into
        the user's own ArozOS storage rather than an upload to anywhere else.
    */
    function openCatalogueImport() {
        openSheet("Import a barcode list",
            '<div class="card muted" style="font-size:13px;line-height:1.55;">' +
            "<p style=\"margin-top:0;\">One row per product. CSV or JSON, with or without a header:</p>" +
            "<p style=\"margin:0;\"><code>4901777018888,Green Tea 500ml,Asahi</code><br>" +
            "<code>barcode,name,brand,category,unit</code></p>" +
            "<p style=\"margin-bottom:0;\">A 12-digit UPC-A and its 13-digit EAN form are stored as " +
            "the same product, so either scan finds it.</p></div>" +

            '<div class="field"><label>Paste the list</label>' +
            '<textarea id="catText" style="min-height:140px;font-family:monospace;font-size:13px;" ' +
            'placeholder="4901777018888,Green Tea 500ml"></textarea></div>' +

            '<div class="field"><label>...or read it from a file</label>' +
            '<input type="text" id="catPath" placeholder="user:/Desktop/upc.csv">' +
            '<div class="hint">Any path in your own storage.</div></div>' +

            '<div class="switch-row"><div class="label">Replace the catalogue' +
            "<small>Off adds to what is already known</small></div>" +
            '<div class="switch" id="catReplace"></div></div>',

            '<button class="btn" id="catCancel">Cancel</button>' +
            '<button class="btn primary" id="catGo"><i class="upload icon"></i>Import</button>');

        var replace = false;
        $("catReplace").onclick = function () {
            replace = !replace;
            this.className = "switch" + (replace ? " on" : "");
        };
        $("catCancel").onclick = closeSheet;
        $("catGo").onclick = function () {
            var text = $("catText").value;
            var vpath = $("catPath").value.trim();
            if (text.trim() === "" && vpath === "") {
                toast("Paste a list or give a file path", "err");
                return;
            }
            api("catalogueImport.agi", {
                text: text,
                vpath: vpath,
                replace: replace ? "true" : "false"
            }, function (data) {
                state.catalogueSize = data.catalogueSize;
                closeSheet();
                toast(data.imported + " barcodes imported" +
                    (data.skipped ? ", " + data.skipped + " skipped" : ""), "ok");
                if (state.view === "more") renderMore();
            });
        };
    }

    /* ── Sheets: item detail, editor, keypad, locations, history ────────── */

    function openSheet(title, bodyHtml, footHtml) {
        // The sheet's own fields receive the wedge directly, so the document
        // level capture stands down while one is open
        InvScanner.setEnabled(false);
        $("sheetTitle").textContent = title;
        $("sheetBody").innerHTML = bodyHtml;
        $("sheetFoot").innerHTML = footHtml || "";
        $("sheetFoot").style.display = footHtml ? "flex" : "none";
        $("sheet").className = "sheet open";
        $("sheetBackdrop").className = "sheet-backdrop open";
        $("sheetBody").scrollTop = 0;

        // Arm the sheet's barcode field so a trigger pull lands in it with no
        // tap and no keyboard
        var first = prepareScanFields($("sheetBody"));
        if (first) {
            setTimeout(function () {
                try { first.focus(); } catch (e) {}
            }, 60);
        }
    }

    function closeSheet() {
        $("sheet").className = "sheet";
        $("sheetBackdrop").className = "sheet-backdrop";
        InvScanner.setEnabled(true);
        armInput();
    }

    function openItemDetail(itemId) {
        var item = findById(itemId);
        if (!item) return;

        var alerts = itemAlerts(item);
        var expiryDays = daysUntil(item.expiryDate);
        var warrantyDays = daysUntil(item.warrantyEnd);

        var html =
            '<div class="card"><div class="result-head"><div class="body">' +
            '<div class="result-name">' + esc(item.name) + "</div>" +
            '<div class="result-sub">' + esc(item.barcode || "no barcode") + "</div>" +
            badgesHtml(alerts) + "</div>" +
            '<div class="result-qty"><div class="value">' + qtyText(item.qty) + "</div>" +
            '<div class="unit">' + esc(item.unit) + "</div></div></div>" +
            '<div class="btn-row" style="margin-top:12px;">' +
            '<button class="btn" id="detailMinus"><i class="minus icon"></i>1</button>' +
            '<button class="btn" id="detailPlus"><i class="plus icon"></i>1</button>' +
            '<button class="btn" id="detailCount"><i class="clipboard list icon"></i>Count</button>' +
            "</div></div>";

        html += '<div class="card"><div class="kv-grid">' +
            kv("Location", item.location || "-") +
            kv("Category", item.category || "-") +
            kv("Unit price", money(item.price)) +
            kv("Stock value", money(item.price * item.qty)) +
            kv("Expiry", item.expiryDate || "-",
                expiryDays === null ? "" : (expiryDays < 0 ? "danger" : (expiryDays <= state.settings.expiryWarnDays ? "warn" : "")),
                item.expiryDate ? relativeDays(expiryDays) : "") +
            kv("Warranty end", item.warrantyEnd || "-",
                warrantyDays === null ? "" : (warrantyDays < 0 ? "danger" : (warrantyDays <= state.settings.warrantyWarnDays ? "warn" : "")),
                item.warrantyEnd ? relativeDays(warrantyDays) : "") +
            kv("SKU", item.sku || "-") +
            kv("Serial", item.serial || "-") +
            kv("Supplier", item.supplier || "-") +
            kv("Min quantity", qtyText(item.minQty)) +
            "</div>" +
            (item.notes ? '<div class="kv" style="margin-top:8px;"><div class="k">Notes</div><div class="v" style="white-space:normal;">' + esc(item.notes) + "</div></div>" : "") +
            "</div>";

        if (item.kind === "cable") {
            html += '<div class="section-title">Cable</div><div class="card"><div class="kv-grid">' +
                kv("Type", item.cableType || "-") +
                kv("Length", item.cableLength ? qtyText(item.cableLength) + " m" : "-") +
                kv("Connector A", item.connectorA || "-") +
                kv("Connector B", item.connectorB || "-") +
                "</div></div>";
        }

        if (item.batches.length) {
            html += '<div class="section-title">Batches (' + item.batches.length + ")</div>" +
                '<div class="card flush">';
            var ordered = item.batches.slice();
            ordered.sort(function (x, y) {
                if (x.expiryDate === y.expiryDate) return 0;
                if (x.expiryDate === "") return 1;
                if (y.expiryDate === "") return -1;
                return x.expiryDate < y.expiryDate ? -1 : 1;
            });
            for (var b = 0; b < ordered.length; b++) {
                html += batchRowHtml(ordered[b], false);
            }
            html += "</div>";
        }

        html += '<div class="section-title">Recent movements</div><div class="card flush" id="itemHistory">' +
            '<div class="empty">Loading...</div></div>';

        openSheet(item.name, html,
            '<button class="btn" id="detailMove"><i class="exchange icon"></i>Move</button>' +
            '<button class="btn primary" id="detailEdit"><i class="pencil icon"></i>Edit</button>');

        $("detailPlus").onclick = function () {
            runStockOp({ itemId: item.id, op: "in", amount: 1 });
            closeSheet();
        };
        $("detailMinus").onclick = function () {
            runStockOp({ itemId: item.id, op: "out", amount: 1 });
            closeSheet();
        };
        $("detailCount").onclick = function () { openKeypad(item); };
        $("detailEdit").onclick = function () { openItemEditor(item, ""); };
        $("detailMove").onclick = function () { openMoveDialog(item); };

        api("history.agi", { itemId: item.id, limit: 25 }, function (data) {
            var target = $("itemHistory");
            if (!target) return;
            target.innerHTML = movementListHtml(data.movements);
        });
    }

    function kv(label, value, tone, sub) {
        return '<div class="kv' + (tone ? " " + tone : "") + '"><div class="k">' + esc(label) +
            '</div><div class="v">' + esc(value) + "</div>" +
            (sub ? '<div class="sub">' + esc(sub) + "</div>" : "") + "</div>";
    }

    function movementListHtml(movements) {
        if (!movements || !movements.length) {
            return '<div class="empty">No movements recorded yet.</div>';
        }
        var icons = {
            in: "plus", out: "minus", move: "exchange", set: "clipboard list",
            create: "star", edit: "pencil", "delete": "trash"
        };
        var html = "";
        for (var i = 0; i < movements.length; i++) {
            var mv = movements[i];
            var when = new Date(mv.ts);
            var pad = function (n) { return (n < 10 ? "0" : "") + n; };
            var stamp = when.getFullYear() + "-" + pad(when.getMonth() + 1) + "-" + pad(when.getDate()) +
                " " + pad(when.getHours()) + ":" + pad(when.getMinutes());

            var summary;
            if (mv.type === "move") {
                summary = (mv.fromLocation || "-") + " to " + (mv.toLocation || "-");
            } else {
                summary = (mv.delta > 0 ? "+" : "") + qtyText(mv.delta) + " to " + qtyText(mv.qtyAfter);
            }
            if (mv.note) summary += " &middot; " + esc(mv.note);

            html += '<div class="log-row">' +
                '<div class="log-icon ' + esc(mv.type) + '"><i class="' + (icons[mv.type] || "dot circle") + ' icon"></i></div>' +
                '<div class="body"><div class="t">' + esc(mv.name || mv.barcode || "item") + "</div>" +
                '<div class="s">' + esc(stamp) + " &middot; " + summary + "</div></div></div>";
        }
        return html;
    }

    function openHistory() {
        openSheet("Movement history", '<div class="card flush" id="historyBody"><div class="empty">Loading...</div></div>', "");
        api("history.agi", { limit: 250 }, function (data) {
            var host = $("historyBody");
            if (host) host.innerHTML = movementListHtml(data.movements);
        });
    }

    /* Item editor - the one place every tracked field can be set */
    function openItemEditor(item, prefillBarcode, catalogueHit) {
        var isNew = !item;
        var it = item || {
            id: "", kind: "item", barcode: prefillBarcode || "",
            name: (catalogueHit && catalogueHit.name) || "",
            sku: "", category: (catalogueHit && catalogueHit.category) || "",
            qty: 0, unit: (catalogueHit && catalogueHit.unit) || "pcs",
            minQty: 0, location: state.moveTarget || "", price: 0,
            expiryDate: "", warrantyEnd: "", supplier: "", serial: "", notes: "",
            batches: [], cableType: "", cableLength: 0, connectorA: "", connectorB: ""
        };

        // Edited in place while the sheet is open, saved with the item
        editorBatches = (it.batches || []).slice();

        var locationOptions = '<option value="">- none -</option>';
        var hasLocation = false;
        for (var i = 0; i < state.locations.length; i++) {
            if (state.locations[i] === it.location) hasLocation = true;
            locationOptions += '<option value="' + esc(state.locations[i]) + '"' +
                (state.locations[i] === it.location ? " selected" : "") + ">" + esc(state.locations[i]) + "</option>";
        }
        if (it.location && !hasLocation) {
            locationOptions += '<option value="' + esc(it.location) + '" selected>' + esc(it.location) + "</option>";
        }
        locationOptions += '<option value="__new__">+ New location...</option>';

        var html =
            '<div class="field"><label>Barcode</label>' +
            '<input type="text" id="fBarcode" data-scan="1" autocomplete="off" value="' +
            esc(it.barcode) + '" placeholder="Scan or type">' +
            '<div class="hint">' + scanFieldHint() +
            "The scan key. Must be unique across the inventory.</div></div>" +

            '<div class="field"><label>Name</label>' +
            '<input type="text" id="fName" value="' + esc(it.name) + '" placeholder="What is it"></div>' +

            '<div class="field"><label>What is it</label>' +
            '<select id="fKind">' +
            '<option value="item"' + (it.kind !== "cable" ? " selected" : "") + ">Stock item</option>" +
            '<option value="cable"' + (it.kind === "cable" ? " selected" : "") + ">Cable</option>" +
            "</select></div>" +

            '<div class="field-row">' +
            '<div class="field"><label>Quantity</label>' +
            '<input type="number" inputmode="decimal" step="any" id="fQty" value="' + esc(it.qty) + '"></div>' +
            '<div class="field"><label>Unit</label>' +
            '<input type="text" id="fUnit" value="' + esc(it.unit) + '" placeholder="pcs"></div>' +
            "</div>" +

            '<div class="field-row">' +
            '<div class="field"><label>Unit price</label>' +
            '<input type="number" inputmode="decimal" step="any" min="0" id="fPrice" value="' + esc(it.price) + '"></div>' +
            '<div class="field"><label>Low stock at</label>' +
            '<input type="number" inputmode="decimal" step="any" min="0" id="fMinQty" value="' + esc(it.minQty) + '"></div>' +
            "</div>" +

            '<div class="field"><label>Location</label>' +
            '<select id="fLocation">' + locationOptions + "</select></div>" +

            '<div class="field-row">' +
            '<div class="field"><label>Expiry date</label>' +
            '<input type="date" id="fExpiry" value="' + esc(it.expiryDate) + '"></div>' +
            '<div class="field"><label>Warranty end</label>' +
            '<input type="date" id="fWarranty" value="' + esc(it.warrantyEnd) + '"></div>' +
            "</div>" +

            '<div class="field-row">' +
            '<div class="field"><label>SKU</label>' +
            '<input type="text" id="fSku" value="' + esc(it.sku) + '"></div>' +
            '<div class="field"><label>Category</label>' +
            '<input type="text" id="fCategory" value="' + esc(it.category) + '"></div>' +
            "</div>" +

            '<div class="field-row">' +
            '<div class="field"><label>Supplier</label>' +
            '<input type="text" id="fSupplier" value="' + esc(it.supplier) + '"></div>' +
            '<div class="field"><label>Serial</label>' +
            '<input type="text" id="fSerial" value="' + esc(it.serial) + '"></div>' +
            "</div>" +

            '<div class="cable-fields" id="cableFields">' +
            '<div class="section-title">Cable</div>' +
            '<div class="field-row">' +
            '<div class="field"><label>Cable type</label>' +
            '<input type="text" id="fCableType" value="' + esc(it.cableType) + '" placeholder="Cat6a"></div>' +
            '<div class="field"><label>Length (m)</label>' +
            '<input type="number" inputmode="decimal" step="any" min="0" id="fCableLength" value="' + esc(it.cableLength) + '"></div>' +
            "</div>" +
            '<div class="field-row">' +
            '<div class="field"><label>Connector A</label>' +
            '<input type="text" id="fConnA" value="' + esc(it.connectorA) + '" placeholder="RJ45"></div>' +
            '<div class="field"><label>Connector B</label>' +
            '<input type="text" id="fConnB" value="' + esc(it.connectorB) + '" placeholder="RJ45"></div>' +
            "</div></div>" +

            '<div class="section-title">Batches</div>' +
            '<div class="card flush" id="batchEditor"></div>' +
            '<button class="btn block small" id="batchAdd" style="margin-bottom:12px;">' +
            '<i class="plus icon"></i>Add a batch</button>' +

            '<div class="field"><label>Notes</label>' +
            '<textarea id="fNotes" placeholder="Anything worth remembering">' + esc(it.notes) + "</textarea></div>";

        if (!isNew) {
            html += '<button class="btn danger block" id="fDelete"><i class="trash icon"></i>Delete this item</button>';
        }

        openSheet(isNew ? "New item" : "Edit item", html,
            '<button class="btn" id="fCancel">Cancel</button>' +
            '<button class="btn primary" id="fSave"><i class="save icon"></i>Save</button>');


        var locationSelect = $("fLocation");
        locationSelect.addEventListener("change", function () {
            if (locationSelect.value === "__new__") {
                promptNewLocation(function (name) {
                    var option = document.createElement("option");
                    option.value = name;
                    option.text = name;
                    locationSelect.add(option, locationSelect.options[locationSelect.options.length - 1]);
                    locationSelect.value = name;
                });
                locationSelect.value = it.location || "";
            }
        });

        /*
            The barcode box is armed when this sheet opens, so a trigger pull
            lands here. Once a code arrives, ask the local catalogue what it is
            and fill the name in when the operator has not typed one - which is
            the whole point of keeping a catalogue.
        */
        var barcodeInput = $("fBarcode");
        barcodeInput.addEventListener("keydown", function (event) {
            if (event.key !== "Enter") return;
            event.preventDefault();
            lookupIntoEditor();
        });
        barcodeInput.addEventListener("change", lookupIntoEditor);

        function lookupIntoEditor() {
            var code = barcodeInput.value.trim();
            if (code === "") return;

            api("catalogueLookup.agi", { barcode: code }, function (data) {
                if (!$("fName")) return;   // sheet closed while we were asking

                if (data.checkDigitOk === false) {
                    toast("That barcode's check digit does not add up", "warn");
                }
                if (data.found && $("fName").value.trim() === "") {
                    $("fName").value = data.name;
                    if (data.category && $("fCategory").value.trim() === "") {
                        $("fCategory").value = data.category;
                    }
                    if (data.unit && $("fUnit").value.trim() === "") {
                        $("fUnit").value = data.unit;
                    }
                    InvScanner.feedbackOk();
                    toast("Name filled in from your catalogue", "ok");
                }
            });
        }

        var kindSelect = $("fKind");
        var syncKind = function () {
            // The cable block is only noise on a box of gloves. "block" rather
            // than "" because clearing the inline style hands the element back
            // to the stylesheet, which hides .cable-fields by default.
            $("cableFields").style.display = kindSelect.value === "cable" ? "block" : "none";
        };
        kindSelect.addEventListener("change", syncKind);
        syncKind();

        renderBatchEditor();
        $("batchAdd").onclick = function () {
            editorBatches.push({ id: "", batch: "", expiryDate: "", qty: 0, note: "" });
            renderBatchEditor();
        };

        $("fCancel").onclick = closeSheet;

        $("fSave").onclick = function () {
            collectBatchEditor();
            var payload = {
                id: it.id,
                kind: kindSelect.value,
                barcode: $("fBarcode").value,
                name: $("fName").value,
                sku: $("fSku").value,
                category: $("fCategory").value,
                qty: $("fQty").value,
                unit: $("fUnit").value,
                minQty: $("fMinQty").value,
                location: locationSelect.value === "__new__" ? "" : locationSelect.value,
                price: $("fPrice").value,
                expiryDate: $("fExpiry").value,
                warrantyEnd: $("fWarranty").value,
                supplier: $("fSupplier").value,
                serial: $("fSerial").value,
                notes: $("fNotes").value,
                batches: editorBatches,
                cableType: $("fCableType").value,
                cableLength: $("fCableLength").value,
                connectorA: $("fConnA").value,
                connectorB: $("fConnB").value,
                createdAt: it.createdAt
            };

            if (!payload.name.trim() && !payload.barcode.trim()) {
                toast("Give the item a name or a barcode", "err");
                return;
            }

            api("saveItem.agi", { itemData: JSON.stringify(payload) }, function (data) {
                upsertItem(data.item);
                if (data.locations) state.locations = data.locations;
                closeSheet();
                if (data.barcodeSuspect) {
                    toast("Saved, but that barcode's check digit does not add up", "warn");
                } else {
                    toast(isNew ? "Item added" : "Item saved", "ok");
                }
                InvScanner.feedbackOk();
                refreshAllViews();
                if (isNew) showResult({ type: "lookup", item: data.item });
            });
        };

        if (!isNew) {
            $("fDelete").onclick = function () {
                confirmSheet("Delete " + it.name + "?",
                    "The item is removed from stock. Its movement history is kept for auditing.",
                    function () {
                        api("deleteItem.agi", { itemId: it.id }, function () {
                            for (var d = 0; d < state.items.length; d++) {
                                if (state.items[d].id === it.id) { state.items.splice(d, 1); break; }
                            }
                            closeSheet();
                            toast("Item deleted", "ok");
                            refreshAllViews();
                        });
                    },
                    function () { openItemEditor(findById(it.id), ""); });
            };
        }

        // When the barcode is already known (created from a scan) the operator's
        // next job is the name, so send them there instead of the armed barcode
        if (isNew && it.barcode) {
            setTimeout(function () {
                var name = $("fName");
                if (!name) return;
                name.setAttribute("data-typing", "1");
                try { name.focus(); } catch (e) {}
            }, 90);
        }
    }

    // Batch rows being edited in the open item sheet
    var editorBatches = [];

    /*
        Batch rows in the item editor. While any exist the item's own quantity
        and expiry are derived from them, so those two inputs are locked to stop
        an operator editing a number that is about to be overwritten.
    */
    function renderBatchEditor() {
        var host = $("batchEditor");
        if (!host) return;

        var qtyInput = $("fQty");
        var expiryInput = $("fExpiry");
        var derived = editorBatches.length > 0;

        if (qtyInput) {
            qtyInput.readOnly = derived;
            qtyInput.className = derived ? "derived" : "";
            if (derived) {
                var total = 0;
                for (var t = 0; t < editorBatches.length; t++) {
                    total += parseFloat(editorBatches[t].qty) || 0;
                }
                qtyInput.value = total;
            }
        }
        if (expiryInput) {
            expiryInput.readOnly = derived;
            expiryInput.className = derived ? "derived" : "";
        }

        if (!editorBatches.length) {
            host.innerHTML = '<div class="empty" style="padding:16px;">' +
                "Not batch tracked. Add a batch when the same product arrives with " +
                "its own lot number and expiry date.</div>";
            return;
        }

        var html = "";
        for (var i = 0; i < editorBatches.length; i++) {
            var batch = editorBatches[i];
            html += '<div class="batch-edit" data-index="' + i + '">' +
                '<div class="field-row">' +
                '<div class="field"><label>Batch / lot</label>' +
                '<input type="text" class="b-label" data-scan="1" value="' + esc(batch.batch) + '" placeholder="L-2409"></div>' +
                '<div class="field"><label>Quantity</label>' +
                '<input type="number" inputmode="decimal" step="any" class="b-qty" value="' + esc(batch.qty) + '"></div>' +
                "</div>" +
                '<div class="field-row">' +
                '<div class="field"><label>Expiry date</label>' +
                '<input type="date" class="b-expiry" value="' + esc(batch.expiryDate) + '"></div>' +
                '<div class="field" style="flex:0 0 auto;"><label>&nbsp;</label>' +
                '<button class="btn small danger b-remove"><i class="trash icon"></i></button></div>' +
                "</div></div>";
        }
        host.innerHTML = html;
        prepareScanFields(host);

        var removes = host.querySelectorAll(".b-remove");
        for (var r = 0; r < removes.length; r++) {
            removes[r].onclick = function () {
                collectBatchEditor();
                var index = parseInt(closestClass(this, "batch-edit").getAttribute("data-index"), 10);
                editorBatches.splice(index, 1);
                renderBatchEditor();
            };
        }

        // Keep the derived total honest as the operator types
        var qtyFields = host.querySelectorAll(".b-qty");
        for (var q = 0; q < qtyFields.length; q++) {
            qtyFields[q].addEventListener("input", function () {
                collectBatchEditor();
                renderBatchEditor();
            });
        }
    }

    /* Reads the batch rows back out of the DOM into editorBatches */
    function collectBatchEditor() {
        var host = $("batchEditor");
        if (!host) return;
        var rows = host.querySelectorAll(".batch-edit");
        var collected = [];
        for (var i = 0; i < rows.length; i++) {
            var index = parseInt(rows[i].getAttribute("data-index"), 10);
            var previous = editorBatches[index] || {};
            collected.push({
                id: previous.id || "",
                batch: rows[i].querySelector(".b-label").value,
                qty: rows[i].querySelector(".b-qty").value,
                expiryDate: rows[i].querySelector(".b-expiry").value,
                note: previous.note || "",
                receivedAt: previous.receivedAt
            });
        }
        editorBatches = collected;
    }

    /* Numeric keypad for stock takes - usable with gloves, no soft keyboard */
    function openKeypad(item) {
        var entry = "";

        var html =
            '<div class="card"><div class="result-name">' + esc(item.name) + "</div>" +
            '<div class="result-sub">' + esc(item.barcode || "no barcode") + " &middot; system says " +
            qtyText(item.qty) + " " + esc(item.unit) + "</div></div>" +
            '<div class="keypad-display" id="padDisplay">0</div>' +
            '<div class="keypad">' +
            padKey("7") + padKey("8") + padKey("9") +
            padKey("4") + padKey("5") + padKey("6") +
            padKey("1") + padKey("2") + padKey("3") +
            padKey(".") + padKey("0") +
            '<button class="btn" data-key="del" aria-label="Backspace">' +
            '<svg class="pad-glyph" viewBox="0 0 24 24" fill="none" stroke="currentColor" ' +
            'stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
            '<path d="M22 5H8L2 12l6 7h14V5z"/><path d="M17 9l-6 6"/><path d="M11 9l6 6"/>' +
            "</svg></button>" +
            "</div>";

        openSheet("Counted quantity", html,
            '<button class="btn" id="padCancel">Cancel</button>' +
            '<button class="btn primary" id="padOk"><i class="check icon"></i>Set quantity</button>');


        var display = $("padDisplay");
        var keys = $("sheetBody").querySelectorAll(".keypad .btn");
        for (var i = 0; i < keys.length; i++) {
            keys[i].onclick = function () {
                var key = this.getAttribute("data-key");
                if (key === "del") {
                    entry = entry.slice(0, -1);
                } else if (key === ".") {
                    if (entry.indexOf(".") === -1) entry += (entry === "" ? "0." : ".");
                } else if (entry.length < 9) {
                    entry = (entry === "0") ? key : entry + key;
                }
                display.textContent = entry === "" ? "0" : entry;
            };
        }

        $("padCancel").onclick = closeSheet;

        $("padOk").onclick = function () {
            var value = parseFloat(entry === "" ? "0" : entry);
            if (isNaN(value) || value < 0) {
                toast("Enter a quantity of zero or more", "err");
                return;
            }
            closeSheet();
            runStockOp({ itemId: item.id, op: "set", amount: value, note: "Stock take" });
        };
    }

    function padKey(label) {
        return '<button class="btn" data-key="' + label + '">' + label + "</button>";
    }

    function openMoveDialog(item) {
        var options = '<option value="">- pick a destination -</option>';
        for (var i = 0; i < state.locations.length; i++) {
            options += '<option value="' + esc(state.locations[i]) + '">' + esc(state.locations[i]) + "</option>";
        }
        options += '<option value="__new__">+ New location...</option>';

        openSheet("Move item",
            '<div class="card"><div class="result-name">' + esc(item.name) + "</div>" +
            '<div class="result-sub">currently in ' + esc(item.location || "no location") + "</div></div>" +
            '<div class="field"><label>Move to</label><select id="moveTo">' + options + "</select></div>",
            '<button class="btn" id="moveCancel">Cancel</button>' +
            '<button class="btn primary" id="moveOk"><i class="exchange icon"></i>Move</button>');

        var select = $("moveTo");
        select.addEventListener("change", function () {
            if (select.value === "__new__") {
                promptNewLocation(function (name) {
                    var option = document.createElement("option");
                    option.value = name;
                    option.text = name;
                    select.add(option, select.options[select.options.length - 1]);
                    select.value = name;
                });
                select.value = "";
            }
        });

        $("moveCancel").onclick = closeSheet;
        $("moveOk").onclick = function () {
            if (!select.value || select.value === "__new__") {
                toast("Pick a destination", "err");
                return;
            }
            closeSheet();
            runStockOp({ itemId: item.id, op: "move", toLocation: select.value });
        };
    }

    function openLocationManager() {
        renderLocationManager();
    }

    function renderLocationManager() {
        var html = '<div class="field"><label>Add a location</label>' +
            '<div class="btn-row"><input type="text" class="input-lg" id="newLocation" placeholder="e.g. Aisle A3 / Shelf 2">' +
            '<button class="btn primary square" id="addLocation"><i class="plus icon"></i></button></div></div>';

        if (!state.locations.length) {
            html += '<div class="empty"><i class="map marker alternate icon"></i>No locations yet.</div>';
        } else {
            html += '<div class="card flush">';
            for (var i = 0; i < state.locations.length; i++) {
                var name = state.locations[i];
                var count = 0;
                for (var j = 0; j < state.items.length; j++) {
                    if (state.items[j].location === name) count++;
                }
                html += '<div class="log-row"><div class="log-icon lookup"><i class="map marker alternate icon"></i></div>' +
                    '<div class="body"><div class="t">' + esc(name) + "</div>" +
                    '<div class="s">' + count + (count === 1 ? " item" : " items") + "</div></div>" +
                    '<button class="btn small ghost rm-location" data-name="' + esc(name) + '">' +
                    '<i class="trash icon"></i></button></div>';
            }
            html += "</div>";
        }

        openSheet("Locations", html, "");

        var input = $("newLocation");
        var commit = function () {
            var name = input.value.trim();
            if (name === "") return;
            var next = state.locations.slice();
            next.push(name);
            saveLocations(next, renderLocationManager);
        };

        $("addLocation").onclick = commit;
        input.addEventListener("keydown", function (event) {
            if (event.key === "Enter") { event.preventDefault(); commit(); }
        });

        var removeButtons = $("sheetBody").querySelectorAll(".rm-location");
        for (var r = 0; r < removeButtons.length; r++) {
            removeButtons[r].onclick = function () {
                var name = this.getAttribute("data-name");
                var next = [];
                for (var k = 0; k < state.locations.length; k++) {
                    if (state.locations[k] !== name) next.push(state.locations[k]);
                }
                // Items keep their location string; only the pick list shrinks
                saveLocations(next, renderLocationManager);
            };
        }
    }

    function saveLocations(list, onDone) {
        api("saveLocations.agi", { locationsJson: JSON.stringify(list) }, function (data) {
            state.locations = data.locations;
            renderContext();
            if (onDone) onDone();
        });
    }

    function promptNewLocation(onDone) {
        var name = window.prompt("New location name");
        if (name === null) return;
        name = name.trim();
        if (name === "") return;

        var next = state.locations.slice();
        next.push(name);
        saveLocations(next, function () {
            onDone(name);
        });
    }

    function confirmSheet(title, message, onConfirm, onCancel) {
        openSheet(title,
            '<div class="card"><div style="font-size:15px;">' + esc(message) + "</div></div>",
            '<button class="btn" id="confirmNo">Cancel</button>' +
            '<button class="btn danger" id="confirmYes"><i class="trash icon"></i>Delete</button>');

        $("confirmNo").onclick = function () {
            if (onCancel) {
                onCancel();
            } else {
                closeSheet();
            }
        };
        $("confirmYes").onclick = onConfirm;
    }

    /* ── Views ──────────────────────────────────────────────────────────── */

    function switchView(name) {
        state.view = name;

        var views = document.querySelectorAll(".view");
        for (var i = 0; i < views.length; i++) {
            views[i].className = "view" + (views[i].id === "view-" + name ? " active" : "");
        }
        var buttons = document.querySelectorAll(".nav-btn");
        for (var j = 0; j < buttons.length; j++) {
            buttons[j].className = "nav-btn" + (buttons[j].getAttribute("data-view") === name ? " active" : "");
        }
        $("views").scrollTop = 0;

        if (name === "search") renderSearch();
        if (name === "alerts") renderAlerts();
        if (name === "cables") renderCables();
        if (name === "more") renderMore();

        // Arm whichever field this view scans into, so a trigger pull always
        // lands somewhere without the operator having to tap first
        armInput();
    }

    function refreshAllViews() {
        updateSubtitle();
        renderAlertBadge();
        renderPairingBar();
        renderContext();
        if (state.view === "search") renderSearch();
        if (state.view === "alerts") renderAlerts();
        if (state.view === "cables") renderCables();
        if (state.view === "more") renderMore();
    }

    /* ── Full screen ────────────────────────────────────────────────────── */

    function fullscreenElement() {
        return document.fullscreenElement || document.webkitFullscreenElement || null;
    }

    /*
        Worth having on a handheld: the browser's own chrome eats 15% of a 4.7"
        screen, and a warehouse app is used one screen at a time.
    */
    function toggleFullscreen() {
        var root = document.documentElement;

        if (fullscreenElement()) {
            var exit = document.exitFullscreen || document.webkitExitFullscreen;
            if (exit) exit.call(document);
            return;
        }

        var request = root.requestFullscreen || root.webkitRequestFullscreen;
        if (!request) {
            toast("This browser will not go full screen", "warn");
            return;
        }

        try {
            var result = request.call(root);
            // A float window's iframe may not carry allowfullscreen, in which
            // case the request is rejected rather than throwing
            if (result && result.then) {
                result.then(syncFullscreenIcon, function () {
                    toast("Full screen was blocked - open the app in its own tab", "warn");
                });
            }
        } catch (e) {
            toast("Full screen is not available here", "warn");
        }
    }

    function syncFullscreenIcon() {
        var icon = $("fullscreenIcon");
        if (!icon) return;
        icon.className = fullscreenElement() ? "compress icon" : "expand icon";
    }

    /* ── Theme ──────────────────────────────────────────────────────────── */

    function applyTheme(theme) {
        document.body.className = (theme === "white" || theme === "light") ? "light-mode" : "";
        var icon = $("themeIcon");
        if (icon) icon.className = (theme === "white" || theme === "light") ? "sun icon" : "moon icon";
    }

    function initTheme() {
        if (typeof ao_module_getSystemThemeColor === "function") {
            ao_module_getSystemThemeColor(function (color) {
                applyTheme(color);
            });
        }
        if (typeof ao_module_onThemeChanged === "function") {
            ao_module_onThemeChanged(function (color) {
                applyTheme(color);
            });
        }
    }

    /* ── Boot ───────────────────────────────────────────────────────────── */

    function load(showToast) {
        api("init.agi", { movementLimit: 50 }, function (data) {
            state.items = data.items || [];
            state.locations = data.locations || [];
            state.settings = data.settings || {};
            state.movements = data.movements || [];
            state.runs = data.runs || [];
            state.catalogueSize = data.catalogueSize || 0;
            state.counting = data.countSession || null;
            state.loaded = true;

            if (state.settings.defaultStep) state.step = state.settings.defaultStep;

            applySettings();
            renderContext();
            setScanStatus(null, true);
            refreshAllViews();
            updateSubtitle();
            armInput();
            flushPendingScans();

            if (showToast) toast("Reloaded " + state.items.length + " items", "ok");
        }, function () {
            state.loaded = false;
            setScanStatus("Could not load the inventory", false);
        });
    }

    function updateSubtitle() {
        var parts = [state.items.length + " items", state.locations.length + " locations"];
        if (state.runs.length) parts.push(state.runs.length + " cable runs");
        if (state.counting) parts.push("count open");
        $("topSubtitle").textContent = parts.join(" · ");
    }

    function bindUi() {
        var modeButtons = document.querySelectorAll(".mode-btn");
        for (var i = 0; i < modeButtons.length; i++) {
            modeButtons[i].onclick = function () {
                setMode(this.getAttribute("data-mode"));
            };
        }

        var navButtons = document.querySelectorAll(".nav-btn");
        for (var j = 0; j < navButtons.length; j++) {
            navButtons[j].onclick = function () {
                switchView(this.getAttribute("data-view"));
            };
        }

        // Step chips live inside contextCard, which is re-rendered often
        $("contextCard").addEventListener("click", function (event) {
            var stepNode = closestClass(event.target, "step-chip");
            if (stepNode) {
                state.step = parseFloat(stepNode.getAttribute("data-step"));
                renderContext();
                setScanStatus(null, true);
                armInput();
                return;
            }
            var methodNode = closestClass(event.target, "count-method");
            if (methodNode) {
                state.countMethod = methodNode.getAttribute("data-method");
                renderContext();
                setScanStatus(null, true);
                armInput();
                return;
            }
            if (closestClass(event.target, "count-start") ||
                (event.target.id === "countStartBtn")) {
                startBlindCount(null);
                return;
            }
            if (event.target.id === "countReviewBtn" ||
                closestClass(event.target, "count-review")) {
                openCountReview();
                return;
            }
            if (event.target.id === "countCancelBtn" ||
                closestClass(event.target, "count-cancel")) {
                cancelBlindCount();
                return;
            }
            if (closestClass(event.target, "step-custom")) {
                var typed = window.prompt("Units per scan", qtyText(state.step));
                if (typed === null) return;
                var value = parseFloat(typed);
                if (isNaN(value) || value <= 0) {
                    toast("Enter a number above zero", "err");
                    return;
                }
                state.step = value;
                renderContext();
                setScanStatus(null, true);
                armInput();
            }
        });

        var scanInput = $("scanInput");

        // The scan box and the search box are scan fields like any other
        prepareScanFields(document);

        $("scanSubmit").onclick = function () {
            scanner.submit();
            armInput();
        };

        $("kbToggle").onclick = function () {
            // Tapping a box already lifts the keyboard for that one entry; this
            // pins the choice so it survives the next scan and the next reload
            var next = suppressSoftKeyboard() ? "always" : "auto";
            scanInput.removeAttribute("data-typing");
            saveSettings({ softKeyboard: next });
            scanInput.blur();
            setTimeout(function () { scanInput.focus(); }, 30);
            toast(next === "always"
                ? "On-screen keyboard stays on"
                : "On-screen keyboard off for scanning - tap the box to type", "");
        };

        $("btnAdd").onclick = function () { openItemEditor(null, ""); };

        $("btnFullscreen").onclick = toggleFullscreen;
        document.addEventListener("fullscreenchange", syncFullscreenIcon);
        document.addEventListener("webkitfullscreenchange", syncFullscreenIcon);

        $("btnTheme").onclick = function () {
            if (typeof ao_module_toggleSystemTheme === "function") {
                ao_module_toggleSystemTheme();
            } else {
                applyTheme(document.body.className === "light-mode" ? "dark" : "white");
            }
        };

        $("sheetClose").onclick = closeSheet;
        $("sheetBackdrop").onclick = closeSheet;

        var searchInput = $("searchInput");
        searchInput.addEventListener("input", function () {
            state.query = searchInput.value;
            $("searchClear").className = "search-clear" + (state.query ? " visible" : "");
            if (searchTimer) clearTimeout(searchTimer);
            // Just enough delay to coalesce a wedge burst into one render
            searchTimer = setTimeout(renderSearch, 60);
        });
        // The search box gets its own burst detection: a wedge fires into
        // whatever is focused, so scanning after an earlier query would
        // otherwise append to the leftover text and match nothing.
        var searchBurst = { text: "", at: 0 };
        searchInput.addEventListener("keydown", function (event) {
            var now = new Date().getTime();

            if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                event.preventDefault();
                moveSearchSelection(event.key === "ArrowDown" ? 1 : -1);
                return;
            }

            if (event.key === "Enter") {
                event.preventDefault();

                // Prefer the trailing fast-typed run (the scan) over the whole
                // field, then fall back to the field for hand-typed codes.
                var direct = findByBarcode(searchBurst.text) || findByBarcode(searchInput.value);
                searchBurst.text = "";

                if (direct) {
                    // Leave the box showing what was scanned, not a stale query
                    searchInput.value = direct.barcode;
                    state.query = direct.barcode;
                    $("searchClear").className = "search-clear visible";
                    renderSearch();
                    openItemDetail(direct.id);
                    return;
                }

                // Not a barcode: Enter opens the highlighted row, or the best
                // match, so a typed query always leads somewhere.
                if (searchTimer) {
                    clearTimeout(searchTimer);
                    searchTimer = null;
                    state.query = searchInput.value;
                    renderSearch();
                }
                var best = currentSearchItem();
                if (best) {
                    openItemDetail(best.id);
                } else if (searchInput.value !== "") {
                    InvScanner.feedbackError();
                    toast("Nothing matches that search", "err");
                }
                return;
            }

            if (!event.key || event.key.length !== 1) return;
            searchBurst.text = (now - searchBurst.at > SEARCH_BURST_GAP_MS)
                ? event.key
                : searchBurst.text + event.key;
            searchBurst.at = now;
        });

        $("searchClear").onclick = function () {
            searchInput.value = "";
            state.query = "";
            $("searchClear").className = "search-clear";
            renderSearch();
            searchInput.focus();
        };

        // Keep a field armed at all times: a button keeps focus after a click,
        // which would swallow the next trigger pull, so hand it straight back.
        document.addEventListener("click", function (event) {
            if (isSheetOpen()) return;
            var tag = (event.target.tagName || "").toLowerCase();
            if (tag === "input" || tag === "select" || tag === "textarea") return;
            setTimeout(armInput, 0);
        });

        window.addEventListener("focus", armInput);

        // Coming back from a locked screen or another app must re-arm too
        document.addEventListener("visibilitychange", function () {
            if (!document.hidden) setTimeout(armInput, 50);
        });

        // The layout swaps between handheld and desktop on resize; the Find
        // view has to re-render so its columns match the new layout.
        var wasDesktop = isDesktopLayout();
        window.addEventListener("resize", function () {
            var nowDesktop = isDesktopLayout();
            if (nowDesktop === wasDesktop) return;
            wasDesktop = nowDesktop;
            refreshAllViews();
        });

        bindShortcuts();
    }

    /*
        Desktop keyboard shortcuts. Function keys and Ctrl/Cmd chords are used
        deliberately: no keyboard-wedge scanner emits either, so these can never
        be triggered by a barcode arriving mid-shortcut.
    */
    function bindShortcuts() {
        var MODE_KEYS = { F1: "lookup", F2: "in", F3: "out", F4: "move", F5: "set" };

        document.addEventListener("keydown", function (event) {
            var chord = event.ctrlKey || event.metaKey;

            if (event.key === "Escape") {
                if (isSheetOpen()) {
                    closeSheet();
                } else if (state.view === "search" && state.query !== "") {
                    $("searchClear").click();
                } else {
                    switchView("scan");
                }
                event.preventDefault();
                return;
            }

            if (isSheetOpen()) return;

            if (!chord && MODE_KEYS[event.key]) {
                switchView("scan");
                setMode(MODE_KEYS[event.key]);
                event.preventDefault();
                return;
            }

            if (chord && (event.key === "f" || event.key === "F")) {
                switchView("search");
                $("searchInput").focus();
                $("searchInput").select();
                event.preventDefault();
                return;
            }

            if (chord && (event.key === "n" || event.key === "N")) {
                openItemEditor(null, "");
                event.preventDefault();
                return;
            }

            // Arrow keys drive the result list even when the box is not focused
            if (state.view === "search" && !chord &&
                (event.key === "ArrowDown" || event.key === "ArrowUp")) {
                moveSearchSelection(event.key === "ArrowDown" ? 1 : -1);
                event.preventDefault();
            }
        });
    }

    /* Walks up from `node` looking for an ancestor carrying `className` */
    function closestClass(node, className) {
        while (node && node.nodeType === 1) {
            if ((" " + node.className + " ").indexOf(" " + className + " ") !== -1) return node;
            node = node.parentNode;
        }
        return null;
    }

    function start() {
        if (typeof ao_module_setWindowTitle === "function") {
            ao_module_setWindowTitle("Inventory");
        }
        initTheme();

        // Render the mode buttons before anything binds to them
        var grid = $("modeGrid");
        var html = "";
        for (var i = 0; i < MODES.length; i++) {
            html += '<button class="mode-btn' + (MODES[i].id === state.mode ? " active" : "") +
                '" data-mode="' + MODES[i].id + '"><i class="' + MODES[i].icon + ' icon"></i>' +
                esc(MODES[i].label) + "</button>";
        }
        grid.innerHTML = html;

        bindUi();

        scanner = InvScanner.init({
            input: $("scanInput"),
            onScan: onScan
        });

        InvPairing.init({
            api: api,
            getContext: pairingContext,
            onScan: receiveRemoteScan,
            onDevices: function (devices) {
                // Refresh the list inside the open pairing panel in place: this
                // is exactly when the operator is watching for their handheld
                // to show up, and re-rendering the whole sheet would drop its
                // buttons mid-tap.
                var host = $("pairDevices");
                if (host) host.innerHTML = pairDeviceListHtml(devices);
            },
            onStatus: function (pairingStatus) {
                state.pairing = pairingStatus;
                renderPairingBar();
                if (state.view === "more") renderMore();
            },
            onReconnect: function (info) {
                toast(info.queued
                    ? "Back online - sending " + info.queued + " held scans"
                    : "Back online", "ok");
            },
            onError: function (message) {
                toast(message, "err");
            }
        });

        renderContext();
        setScanStatus(null, true);
        applyKeyboardPolicy();
        startFocusWatchdog();
        load(false);
        armInput();

        // start() runs at DOMContentLoaded; stylesheets, fonts and - in a float
        // window - the parent's own focus handling can still land after it
        window.addEventListener("load", armInput);
        setTimeout(armInput, 250);

        // The first touch anywhere is the point at which a browser will let this
        // frame take the window focus. Claim only that - moving DOM focus here
        // would pull it out of whatever the operator just put their finger on,
        // which breaks a select the moment its dropdown tries to open.
        document.addEventListener("pointerdown", function () {
            claimWindowFocus();
        }, true);
    }

    return {
        start: start,
        state: state
    };
})();

document.addEventListener("DOMContentLoaded", App.start);
