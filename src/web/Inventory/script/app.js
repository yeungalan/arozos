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
        searchTyping: false,    // operator lifted the IME to type a query
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
            if (onFail) onFail({ error: "Network error" });
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

    function onScan(code) {
        if (!state.loaded) {
            toast("Still loading the inventory", "warn");
            return;
        }
        if (state.busy) return;

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
            } else {
                InvScanner.feedbackError();
                showResult({ type: "unknown", barcode: code });
            }
            return;
        }

        if (state.mode === "set") {
            var target = findByBarcode(code);
            if (!target) {
                InvScanner.feedbackError();
                showResult({ type: "unknown", barcode: code });
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
                InvScanner.feedbackError();
                showResult({ type: "unknown", barcode: data.unknownBarcode });
                setScanStatus(null, true);
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
            InvScanner.feedbackError();
            setScanStatus(null, true);
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
            html += '<div class="context-label">Stock take</div>' +
                '<div class="muted" style="font-size:13px;">Scan an item, then key in the quantity you counted on the shelf.</div>';
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
        if (message === null || message === undefined) {
            var labels = {
                lookup: "Ready - scan to look up",
                in: "Ready - scan to add " + qtyText(state.step),
                out: "Ready - scan to remove " + qtyText(state.step),
                move: state.moveTarget ? ("Ready - scan to move to " + state.moveTarget) : "Pick a destination first",
                set: "Ready - scan, then key in the counted quantity"
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
        if (allowTyping) {
            input.removeAttribute("inputmode");
        } else {
            input.setAttribute("inputmode", "none");
        }
    }

    function applyKeyboardPolicy() {
        var suppress = suppressSoftKeyboard();
        setFieldKeyboard($("scanInput"), !suppress);
        // The search box follows the same policy; its keyboard button lifts it
        // for as long as the operator wants to type instead of scan.
        setFieldKeyboard($("searchInput"), !suppress || state.searchTyping);

        // Nothing to toggle on a device without an on-screen keyboard
        var hasSoftKeyboard = InvScanner.deviceHasSoftKeyboard();
        var scanBtn = $("kbToggle");
        var searchBtn = $("searchKb");
        if (scanBtn) {
            scanBtn.style.display = hasSoftKeyboard ? "" : "none";
            scanBtn.className = "btn square" + (suppress ? "" : " primary");
        }
        if (searchBtn) {
            searchBtn.style.display = hasSoftKeyboard ? "" : "none";
            searchBtn.className = "search-kb" + (state.searchTyping ? " active" : "");
        }
    }

    /*
        Re-arms the field that should receive the next trigger pull for the
        current view. Called after every operation, view change, sheet close,
        window focus and tap, plus a watchdog: a handheld that has quietly lost
        focus drops the next scan, which is the worst failure mode this app has.
    */
    function armInput() {
        if (isSheetOpen()) return;

        var target = null;
        if (state.view === "scan") target = $("scanInput");
        else if (state.view === "search") target = $("searchInput");
        if (!target) return;

        if (document.activeElement === target) return;
        try { target.focus(); } catch (e) {}
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
            host.innerHTML =
                '<div class="card result-card op-error">' +
                '<div class="result-name">Unknown barcode</div>' +
                '<div class="result-sub">' + esc(result.barcode) + "</div>" +
                '<div class="btn-row" style="margin-top:12px;">' +
                '<button class="btn primary" id="createFromScan"><i class="plus icon"></i>Add this item</button>' +
                '<button class="btn" id="searchFromScan"><i class="search icon"></i>Search</button>' +
                "</div></div>";
            $("createFromScan").onclick = function () { openItemEditor(null, result.barcode); };
            $("searchFromScan").onclick = function () {
                switchView("search");
                $("searchInput").value = result.barcode;
                state.query = result.barcode;
                renderSearch();
            };
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
            '<div class="btn-row">' +
            '<button class="btn" id="exportItems"><i class="file excel outline icon"></i>Export items</button>' +
            '<button class="btn" id="exportMoves"><i class="file alternate outline icon"></i>Export log</button>' +
            "</div></div>";

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
                ? "This device has an on-screen keyboard. Automatic keeps it down so the hardware scanner can type into the armed field without covering the screen."
                : "No on-screen keyboard on this device, so Automatic leaves every field typing normally.") +
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
        $("moreLocations").onclick = openLocationManager;
        $("moreHistory").onclick = openHistory;
        $("moreReload").onclick = function () { load(true); };
        $("exportItems").onclick = function () { runExport("items"); };
        $("exportMoves").onclick = function () { runExport("movements"); };

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

    /* ── Sheets: item detail, editor, keypad, locations, history ────────── */

    function openSheet(title, bodyHtml, footHtml) {
        // A sheet always owns the keyboard: its fields must not have to fight
        // the wedge capture for keystrokes.
        InvScanner.setEnabled(false);
        $("sheetTitle").textContent = title;
        $("sheetBody").innerHTML = bodyHtml;
        $("sheetFoot").innerHTML = footHtml || "";
        $("sheetFoot").style.display = footHtml ? "flex" : "none";
        $("sheet").className = "sheet open";
        $("sheetBackdrop").className = "sheet-backdrop open";
        $("sheetBody").scrollTop = 0;
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
    function openItemEditor(item, prefillBarcode) {
        var isNew = !item;
        var it = item || {
            id: "", barcode: prefillBarcode || "", name: "", sku: "", category: "",
            qty: 0, unit: "pcs", minQty: 0, location: state.moveTarget || "", price: 0,
            expiryDate: "", warrantyEnd: "", supplier: "", serial: "", notes: ""
        };

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
            '<input type="text" id="fBarcode" autocomplete="off" value="' + esc(it.barcode) + '" placeholder="Scan or type">' +
            '<div class="hint">The scan key. Must be unique across the inventory.</div></div>' +

            '<div class="field"><label>Name</label>' +
            '<input type="text" id="fName" value="' + esc(it.name) + '" placeholder="What is it"></div>' +

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

        $("fCancel").onclick = closeSheet;

        $("fSave").onclick = function () {
            var payload = {
                id: it.id,
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
                toast(isNew ? "Item added" : "Item saved", "ok");
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

        setTimeout(function () {
            var focusTarget = isNew && it.barcode ? $("fName") : $("fBarcode");
            if (focusTarget) focusTarget.focus();
        }, 60);
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
        if (name === "more") renderMore();

        // Arm whichever field this view scans into, so a trigger pull always
        // lands somewhere without the operator having to tap first
        armInput();
    }

    function refreshAllViews() {
        renderAlertBadge();
        renderContext();
        if (state.view === "search") renderSearch();
        if (state.view === "alerts") renderAlerts();
        if (state.view === "more") renderMore();
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
            state.loaded = true;

            if (state.settings.defaultStep) state.step = state.settings.defaultStep;

            applySettings();
            renderContext();
            setScanStatus(null, true);
            refreshAllViews();
            updateSubtitle();

            if (showToast) toast("Reloaded " + state.items.length + " items", "ok");
        }, function () {
            state.loaded = false;
            setScanStatus("Could not load the inventory", false);
        });
    }

    function updateSubtitle() {
        $("topSubtitle").textContent = state.items.length + " items · " +
            state.locations.length + " locations";
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

        $("scanSubmit").onclick = function () {
            scanner.submit();
            armInput();
        };

        $("kbToggle").onclick = function () {
            // One tap flips between the automatic policy and hand typing
            var next = suppressSoftKeyboard() ? "always" : "auto";
            saveSettings({ softKeyboard: next });
            var input = $("scanInput");
            input.blur();
            setTimeout(function () { input.focus(); }, 30);
            toast(next === "always" ? "On-screen keyboard on" : "On-screen keyboard off (automatic)", "");
        };

        $("btnAdd").onclick = function () { openItemEditor(null, ""); };

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

        // Lifts the on-screen keyboard for this field when the operator wants
        // to type a query rather than scan one
        $("searchKb").onclick = function () {
            state.searchTyping = !state.searchTyping;
            applyKeyboardPolicy();
            searchInput.blur();
            setTimeout(function () { searchInput.focus(); }, 30);
        };
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
                state.searchTyping = true;
                applyKeyboardPolicy();
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

        renderContext();
        setScanStatus(null, true);
        applyKeyboardPolicy();
        startFocusWatchdog();
        load(false);
        armInput();
    }

    return {
        start: start,
        state: state
    };
})();

document.addEventListener("DOMContentLoaded", App.start);
