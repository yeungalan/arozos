/*
    Inventory - barcode scanner input + operator feedback

    The Panasonic Toughbook FZ-N1 (like most rugged handhelds) presents its
    imager as a keyboard-wedge device: pulling the trigger types the decoded
    symbology as a burst of key events, terminated by Enter. There is no API to
    subscribe to - you read the keyboard and tell scans apart from typing.

    Two capture paths run at once:

      1. Field capture - the scan input is kept focused, so a wedge burst lands
         in it and Enter submits. This also lets an operator type a damaged
         barcode by hand.

      2. Global capture - when focus has drifted (after a dialog, a tap on a
         list, an Android IME closing), printable keys still get buffered and a
         terminating Enter commits them. Without this the first trigger pull
         after any UI interaction is silently lost, which is the single most
         common complaint about browser-based scanning apps.

    Bursts are told apart from human typing by inter-key timing: a wedge emits
    characters far faster than fingers can (typically 3-15ms apart), so a run of
    keys with gaps under BURST_GAP_MS is treated as a scan even if the operator
    never touched the field.
*/

var InvScanner = (function () {

    var BURST_GAP_MS = 40;      // max gap between two wedge keystrokes
    var BURST_MIN_LENGTH = 3;   // shorter runs are almost certainly typing
    var DUPLICATE_MS = 350;     // ignore the same code re-fired this quickly

    var state = {
        buffer: "",
        lastKeyAt: 0,
        lastCode: "",
        lastCodeAt: 0,
        enabled: true,
        globalCapture: true,
        onScan: null,
        audio: null,
        beep: true,
        vibrate: true
    };

    /* Strips the control characters some wedges wrap around the payload */
    function clean(code) {
        return ("" + code).replace(/[\x00-\x1F\x7F]/g, "").trim();
    }

    /* True when this exact code just fired - guards against trigger bounce */
    function isDuplicate(code) {
        var now = new Date().getTime();
        if (code === state.lastCode && (now - state.lastCodeAt) < DUPLICATE_MS) {
            return true;
        }
        state.lastCode = code;
        state.lastCodeAt = now;
        return false;
    }

    function emit(code, source) {
        var value = clean(code);
        if (value === "" || !state.onScan) return false;
        if (isDuplicate(value)) return false;
        state.onScan(value, source);
        return true;
    }

    /*
        Submit whatever is in the scan field. Called on Enter and by the manual
        submit button, so hand-typed codes take exactly the same path as scans.
    */
    function submitField(input) {
        var value = clean(input.value);
        input.value = "";
        if (value === "") return false;
        return emit(value, "field");
    }

    function isTextEntry(element) {
        if (!element) return false;
        var tag = (element.tagName || "").toLowerCase();
        if (tag === "textarea" || tag === "select") return true;
        if (tag !== "input") return false;
        var type = (element.getAttribute("type") || "text").toLowerCase();
        return type !== "button" && type !== "checkbox" && type !== "radio" && type !== "submit";
    }

    /*
        Global keydown capture. Runs only while focus is *outside* any text
        entry, so it never competes with someone filling in the item editor.
    */
    function onGlobalKeyDown(event, scanInput) {
        if (!state.enabled || !state.globalCapture) return;
        if (event.ctrlKey || event.altKey || event.metaKey) return;

        var active = document.activeElement;
        if (active === scanInput) return;   // path 1 already owns this
        if (isTextEntry(active)) return;    // operator is filling in a form

        var now = new Date().getTime();
        var gap = now - state.lastKeyAt;
        state.lastKeyAt = now;

        if (event.key === "Enter") {
            var code = state.buffer;
            state.buffer = "";
            if (code.length >= BURST_MIN_LENGTH) {
                event.preventDefault();
                emit(code, "global");
            }
            return;
        }

        // Only single printable characters belong to a wedge burst
        if (!event.key || event.key.length !== 1) return;

        // A slow keystroke starts a new candidate burst rather than extending
        // the previous one, so stray key presses cannot glue onto a real scan.
        if (gap > BURST_GAP_MS) {
            state.buffer = event.key;
        } else {
            state.buffer += event.key;
        }
    }

    /* Short square-wave chirp - audible over warehouse noise, no asset needed */
    function tone(frequency, durationMs, delayMs) {
        if (!state.beep) return;
        try {
            var AudioCtx = window.AudioContext || window.webkitAudioContext;
            if (!AudioCtx) return;
            if (!state.audio) state.audio = new AudioCtx();
            if (state.audio.state === "suspended") state.audio.resume();

            var startAt = state.audio.currentTime + (delayMs || 0) / 1000;
            var osc = state.audio.createOscillator();
            var gain = state.audio.createGain();

            osc.type = "square";
            osc.frequency.value = frequency;
            gain.gain.value = 0.0001;
            osc.connect(gain);
            gain.connect(state.audio.destination);

            // Ramped envelope, otherwise the square wave clicks on every beep
            gain.gain.exponentialRampToValueAtTime(0.12, startAt + 0.008);
            gain.gain.exponentialRampToValueAtTime(0.0001, startAt + durationMs / 1000);

            osc.start(startAt);
            osc.stop(startAt + durationMs / 1000 + 0.02);
        } catch (e) {
            // Audio is a nicety - never let it break the scan path
        }
    }

    function buzz(pattern) {
        if (!state.vibrate) return;
        try {
            if (navigator.vibrate) navigator.vibrate(pattern);
        } catch (e) {}
    }

    /* The three outcomes an operator needs to tell apart without looking */
    function feedbackOk() {
        tone(1180, 70, 0);
        buzz(35);
    }

    function feedbackWarn() {
        tone(760, 90, 0);
        tone(760, 90, 130);
        buzz([30, 60, 30]);
    }

    function feedbackError() {
        tone(320, 150, 0);
        tone(240, 200, 180);
        buzz([70, 70, 140]);
    }

    /*
        init(options)
            options.input        the scan <input> element
            options.onScan       function(code, source)
            options.getSettings  function() -> { beep, vibrate, scanAnywhere }
    */
    function init(options) {
        var input = options.input;
        state.onScan = options.onScan;

        input.addEventListener("keydown", function (event) {
            if (event.key === "Enter") {
                event.preventDefault();
                submitField(input);
            }
        });

        // Some wedges fire a change/paste instead of key events in fast mode
        input.addEventListener("paste", function () {
            setTimeout(function () { submitField(input); }, 0);
        });

        document.addEventListener("keydown", function (event) {
            onGlobalKeyDown(event, input);
        }, true);

        return {
            submit: function () { return submitField(input); }
        };
    }

    function applySettings(settings) {
        state.beep = settings.beep !== false;
        state.vibrate = settings.vibrate !== false;
        state.globalCapture = settings.scanAnywhere !== false;
    }

    function setEnabled(enabled) {
        state.enabled = enabled;
        if (!enabled) state.buffer = "";
    }

    /*
        True when the device has an on-screen keyboard that would cover the UI
        if a field took focus - handhelds, phones and tablets. A desktop with a
        USB or Bluetooth wedge reports false, so its fields behave normally.
    */
    function deviceHasSoftKeyboard() {
        try {
            if (navigator.maxTouchPoints > 0) return true;
            if (window.matchMedia && window.matchMedia("(pointer: coarse)").matches) return true;
            return "ontouchstart" in window;
        } catch (e) {
            return false;
        }
    }

    /* Lets the UI reset the bounce guard, e.g. to count the same item twice */
    function clearDuplicateGuard() {
        state.lastCode = "";
        state.lastCodeAt = 0;
    }

    return {
        init: init,
        applySettings: applySettings,
        setEnabled: setEnabled,
        deviceHasSoftKeyboard: deviceHasSoftKeyboard,
        clearDuplicateGuard: clearDuplicateGuard,
        feedbackOk: feedbackOk,
        feedbackWarn: feedbackWarn,
        feedbackError: feedbackError,
        clean: clean
    };
})();
