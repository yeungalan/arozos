/*
    Inventory - the dedicated handheld scanner page

    Deliberately small. The handheld's whole job here is: pair once, then forward
    every trigger pull and make it unmistakable whether the last one landed. No
    modes, no item list, nothing to navigate away from mid-count.

    The wedge handling and the transport are the shared modules - InvScanner and
    InvPairing - so this page cannot drift from the full app's behaviour.
*/

var InvRemote = (function () {

    var BACKEND = "Inventory/backend/";
    var LOG_MAX = 30;

    var scanner = null;
    var sending = false;
    var sent = [];              // this session's sends, newest first

    function $(id) {
        return document.getElementById(id);
    }

    function esc(text) {
        return ("" + (text === undefined || text === null ? "" : text))
            .replace(/&/g, "&amp;").replace(/</g, "&lt;")
            .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
    }

    function api(script, payload, onDone, onFail) {
        ao_module_agirun(BACKEND + script, payload || {}, function (data) {
            if (typeof data === "string") {
                try {
                    data = JSON.parse(data);
                } catch (e) {
                    if (onFail) onFail({ error: "Bad response" });
                    return;
                }
            }
            if (data && data.error) {
                if (onFail) onFail(data); else toast(data.error, "err");
                return;
            }
            if (onDone) onDone(data);
        }, function () {
            if (onFail) onFail({ error: "Cannot reach the server" , network: true });
        });
    }

    function toast(message, kind) {
        var host = $("toast");
        var node = document.createElement("div");
        node.className = "toast " + (kind || "");
        node.innerHTML = esc(message);
        host.appendChild(node);
        setTimeout(function () {
            if (node.parentNode) node.parentNode.removeChild(node);
        }, kind === "err" ? 4200 : 2400);
    }

    /* ── Keyboard and focus, same rules as the app ───────────────────────── */

    function suppressKeyboard() {
        return InvScanner.deviceHasSoftKeyboard();
    }

    function prepareScanField(input) {
        if (!input || input.getAttribute("data-scan-ready") === "1") return;
        input.setAttribute("data-scan-ready", "1");
        applyFieldKeyboard(input);

        input.addEventListener("pointerdown", function () {
            if (!suppressKeyboard()) return;
            if (document.activeElement !== input) return;   // first tap arms it
            input.setAttribute("data-typing", "1");
            applyFieldKeyboard(input);
        });
        input.addEventListener("blur", function () {
            if (input.getAttribute("data-typing") !== "1") return;
            input.removeAttribute("data-typing");
            applyFieldKeyboard(input);
        });
    }

    function applyFieldKeyboard(input) {
        var typing = input.getAttribute("data-typing") === "1";
        var allow = !suppressKeyboard() || typing;
        if (allow) {
            if (input.getAttribute("inputmode") !== null) input.removeAttribute("inputmode");
        } else if (input.getAttribute("inputmode") !== "none") {
            input.setAttribute("inputmode", "none");
        }
    }

    function armInput() {
        var target = isPaired() ? $("scanInput") : $("joinCode");
        if (!target) return;
        try {
            if (document.hasFocus && !document.hasFocus() && window.focus) window.focus();
        } catch (e) {}
        if (document.activeElement === target) return;
        try { target.focus(); } catch (e) {}
    }

    function isPaired() {
        return InvPairing.isRemote();
    }

    /* ── Sending ─────────────────────────────────────────────────────────── */

    var MODE_LABELS = {
        lookup: "Look up", in: "Stock in", out: "Stock out",
        move: "Move", set: "Count"
    };

    function onScan(code) {
        if (!isPaired()) {
            // Before pairing the only thing worth scanning is nothing at all;
            // a stray pull should not look like it did something
            InvScanner.feedbackError();
            toast("Pair with a desktop first", "err");
            return;
        }
        if (sending) return;

        sending = true;
        setStatus("Sending " + code + "...", false);
        showBig("sending", code, "");

        InvPairing.push(code, function (data) {
            sending = false;

            if (data.queued) {
                // The pull counted; it just has not landed yet
                InvScanner.feedbackWarn();
                showBig("held", code, data.queueLength +
                    (data.queueLength === 1 ? " scan waiting" : " scans waiting"));
                pushLog(code, true, "held");
                setStatus(null, true);
                armInput();
                return;
            }

            InvScanner.feedbackOk();
            var did = MODE_LABELS[data.hostMode] || "";
            showBig("ok", code, did ? "Desktop: " + did : "Delivered");
            pushLog(code, true, did);
            setStatus(null, true);
            armInput();
        }, function (message) {
            sending = false;
            InvScanner.feedbackError();
            showBig("err", code, message);
            pushLog(code, false, message);
            setStatus(null, true);
            armInput();
        });
    }

    /*
        The big card is the only thing an operator looks at between pulls, so it
        carries one word of state and the code, nothing else.
    */
    function showBig(kind, code, detail) {
        var card = $("lastCard");
        var titles = { sending: "Sending", ok: "Sent", held: "Held", err: "Not sent" };
        card.className = "remote-big " + kind;
        card.innerHTML =
            '<div class="remote-big-label">' + titles[kind] + "</div>" +
            '<div class="remote-big-code">' + esc(code) + "</div>" +
            (detail ? '<div class="remote-big-detail">' + esc(detail) + "</div>" : "");
    }

    function pushLog(code, ok, detail) {
        sent.unshift({ code: code, ok: ok, detail: detail, at: new Date() });
        if (sent.length > LOG_MAX) sent.pop();

        var html = "";
        for (var i = 0; i < sent.length; i++) {
            var entry = sent[i];
            var pad = function (n) { return (n < 10 ? "0" : "") + n; };
            var stamp = pad(entry.at.getHours()) + ":" + pad(entry.at.getMinutes()) +
                ":" + pad(entry.at.getSeconds());
            html += '<div class="log-row">' +
                '<div class="log-icon ' + (entry.ok ? "in" : "out") + '">' +
                '<i class="' + (entry.ok ? "check" : "exclamation") + ' icon"></i></div>' +
                '<div class="body"><div class="t">' + esc(entry.code) + "</div>" +
                '<div class="s">' + stamp + (entry.detail ? " &middot; " + esc(entry.detail) : "") +
                "</div></div></div>";
        }
        $("sentLog").innerHTML = html;
    }

    function setStatus(message, listening) {
        var box = $("scanBox");
        var text = $("scanStatusText");
        if (message === null || message === undefined) {
            var status = InvPairing.status();

            if (status.reconnecting) {
                // Still armed: scanning through a dead spot is the point
                text.textContent = status.queued
                    ? "Offline - " + status.queued + " held, keep scanning"
                    : "Offline - reconnecting, keep scanning";
                box.className = "scan-box offline" + (listening ? " listening" : "");
                return;
            }

            var mode = status.hostMode ? MODE_LABELS[status.hostMode] : "";
            text.textContent = mode
                ? "Ready - the desktop is on " + mode
                : "Ready - pull the trigger";
        } else {
            text.textContent = message;
        }
        box.className = "scan-box" + (listening ? " listening" : "");
    }

    /* ── Panes ───────────────────────────────────────────────────────────── */

    function renderPanes() {
        var paired = isPaired();
        var status = InvPairing.status();

        $("pairPane").style.display = paired ? "none" : "block";
        $("scanPane").style.display = paired ? "block" : "none";

        if (paired) {
            $("subtitle").textContent = status.reconnecting
                ? "Reconnecting" + (status.queued ? " - " + status.queued + " held" : "")
                : "Paired on " + status.code + " as " + status.deviceName;

            var mode = status.hostMode ? MODE_LABELS[status.hostMode] : "";
            $("targetCard").className = "remote-target" + (status.reconnecting ? " offline" : "");
            $("targetText").textContent = status.reconnecting
                ? "the desktop, as soon as it is reachable"
                : (mode ? "the desktop, currently on " + mode : "the desktop");
            setStatus(null, true);
        } else {
            $("subtitle").textContent = "Not paired";
        }
        armInput();
    }

    /* ── Full screen ─────────────────────────────────────────────────────── */

    function toggleFullscreen() {
        var root = document.documentElement;
        var current = document.fullscreenElement || document.webkitFullscreenElement;

        if (current) {
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
            if (result && result.then) {
                result.then(syncFullscreenIcon, function () {
                    toast("Full screen was blocked", "warn");
                });
            }
        } catch (e) {
            toast("Full screen is not available here", "warn");
        }
    }

    function syncFullscreenIcon() {
        var icon = $("fullscreenIcon");
        if (!icon) return;
        icon.className = (document.fullscreenElement || document.webkitFullscreenElement)
            ? "compress icon" : "expand icon";
    }

    /* ── Boot ────────────────────────────────────────────────────────────── */

    function start() {
        if (typeof ao_module_setWindowTitle === "function") {
            ao_module_setWindowTitle("Inventory Scanner");
        }
        if (typeof ao_module_getSystemThemeColor === "function") {
            ao_module_getSystemThemeColor(function (colour) {
                document.body.className = (colour === "white" || colour === "light")
                    ? "light-mode" : "";
            });
        }

        prepareScanField($("scanInput"));

        scanner = InvScanner.init({
            input: $("scanInput"),
            onScan: onScan
        });
        InvScanner.applySettings({ beep: true, vibrate: true, scanAnywhere: true });

        InvPairing.init({
            api: api,
            onStatus: renderPanes,
            onReconnect: function (info) {
                toast(info.queued
                    ? "Back online - sending " + info.queued + " held scans"
                    : "Back online", "ok");
            },
            onFlush: function (info) {
                pushLog(info.code, true, "sent after reconnect");
            },
            onError: function (message) { toast(message, "err"); }
        });

        var join = function () {
            var code = $("joinCode").value.trim();
            if (code === "") {
                toast("Enter the code shown on the desktop", "err");
                return;
            }
            InvPairing.join(code, $("joinName").value, function () {
                InvScanner.feedbackOk();
                toast("Paired - start scanning", "ok");
            }, function (message) {
                InvScanner.feedbackError();
                toast(message, "err");
            });
        };

        $("joinGo").onclick = join;
        $("joinCode").addEventListener("keydown", function (event) {
            if (event.key === "Enter") { event.preventDefault(); join(); }
        });

        $("scanSubmit").onclick = function () {
            scanner.submit();
            armInput();
        };

        $("leaveBtn").onclick = function () {
            InvPairing.leave(function () {
                toast("Unpaired", "");
                sent = [];
                $("sentLog").innerHTML = '<div class="empty">Nothing sent yet.</div>';
                $("lastCard").className = "remote-big";
                $("lastCard").innerHTML = '<div class="remote-big-label">No scans sent yet</div>';
            });
        };

        $("btnFullscreen").onclick = toggleFullscreen;
        document.addEventListener("fullscreenchange", syncFullscreenIcon);
        document.addEventListener("webkitfullscreenchange", syncFullscreenIcon);

        $("btnApp").onclick = function () {
            window.location.href = "index.html";
        };

        // Same self-arming discipline as the app: a handheld that has quietly
        // lost focus drops the next pull
        setInterval(function () {
            var active = document.activeElement;
            if (active && active !== document.body && active !== document.documentElement) return;
            armInput();
        }, 700);

        window.addEventListener("focus", armInput);
        document.addEventListener("visibilitychange", function () {
            if (!document.hidden) setTimeout(armInput, 50);
        });
        document.addEventListener("click", function (event) {
            var tag = (event.target.tagName || "").toLowerCase();
            if (tag === "input" || tag === "textarea" || tag === "select") return;
            setTimeout(armInput, 0);
        });

        renderPanes();
        armInput();
    }

    return { start: start };
})();

document.addEventListener("DOMContentLoaded", InvRemote.start);
