/*
    Inventory - remote scanner pairing helpers

    Lets a phone or handheld act as the scanner for an Inventory page open on
    another machine: the handheld reads the barcode, the desktop receives it and
    processes it with whatever mode is selected there.

    Both ends are the same ArozOS user, so everything lives under that user's own
    storage and no cross-account access is possible - "user:/" resolves to the
    invoking user's home, so one account can never see another's session.

    File layout, arranged so every file has exactly one writer and a scan can
    never be lost to a concurrent write:

        pairing/session.json                  written by the desktop (host) only
        pairing/<session>.<device>.queue.json written by that one handheld only

    The host discovers handhelds by listing the queue files, and reads scans out
    of them; it never writes them. A handheld writes only its own queue.
*/

var PAIR_DIR = INV_DIR + "/pairing";
var PAIR_SESSION_PATH = PAIR_DIR + "/session.json";

// Ambiguous glyphs (0/O, 1/I/L, 2/Z, 5/S, 8/B) are left out: the code gets read
// off one screen and typed on another, often in bad warehouse light.
var PAIR_CODE_ALPHABET = "ACDEFGHJKMNPQRTUVWXY34679";
var PAIR_CODE_LENGTH = 6;

var PAIR_SESSION_TTL_MS = 12 * 60 * 60 * 1000;  // host inactive this long -> gone
var PAIR_DEVICE_STALE_MS = 25 * 1000;           // handheld quiet this long -> offline
var PAIR_QUEUE_CAP = 60;                        // scans kept per handheld

/*
    Ids are used to build file names, so anything outside this set is rejected
    rather than sanitised - a silently rewritten id would read from the wrong
    queue instead of failing.
*/
function pairIdIsSafe(id) {
    var text = "" + (id || "");
    if (text.length === 0 || text.length > 40) return false;
    for (var i = 0; i < text.length; i++) {
        var c = text.charAt(i);
        var ok = (c >= "a" && c <= "z") || (c >= "A" && c <= "Z") ||
            (c >= "0" && c <= "9") || c === "_" || c === "-";
        if (!ok) return false;
    }
    return true;
}

function pairNewCode() {
    var code = "";
    for (var i = 0; i < PAIR_CODE_LENGTH; i++) {
        var at = Math.floor(Math.random() * PAIR_CODE_ALPHABET.length);
        code += PAIR_CODE_ALPHABET.charAt(at);
    }
    return code;
}

/* Codes are compared without case, spaces or dashes so "abc-123" matches */
function pairNormaliseCode(code) {
    var text = ("" + (code || "")).toUpperCase();
    var out = "";
    for (var i = 0; i < text.length; i++) {
        var c = text.charAt(i);
        if (c === " " || c === "-" || c === "_") continue;
        out += c;
    }
    return out;
}

/*
    Reads the session file as it stands, tombstone included. A desktop that has
    deliberately stopped pairing leaves a marker behind rather than deleting the
    file, which is what lets a handheld tell "the desktop finished with me" from
    "I cannot reach the server right now" - the first is final, the second is
    worth retrying, and guessing wrong means either dropping a good pairing or
    letting an operator scan into a void.
*/
function pairReadSessionFile() {
    if (!filelib.fileExists(PAIR_SESSION_PATH)) return null;
    try {
        var raw = JSON.parse(filelib.readFile(PAIR_SESSION_PATH));
        return (raw && typeof raw === "object") ? raw : null;
    } catch (e) {
        return null;
    }
}

/* True when the desktop ended the pairing on purpose */
function pairSessionWasEnded() {
    var raw = pairReadSessionFile();
    return !!(raw && raw.ended);
}

function pairLoadSession() {
    if (!filelib.fileExists(PAIR_SESSION_PATH)) return null;
    try {
        var session = JSON.parse(filelib.readFile(PAIR_SESSION_PATH));
        if (!session || typeof session !== "object" || !session.id) return null;
        if (session.ended) return null;   // tombstone, not a live session
        if (!pairIdIsSafe(session.id)) return null;
        // An abandoned session must not keep accepting handhelds forever
        if (invNow() - (session.hostSeenAt || session.createdAt || 0) > PAIR_SESSION_TTL_MS) {
            return null;
        }
        return session;
    } catch (e) {
        return null;
    }
}

function pairSaveSession(session) {
    filelib.mkdir(PAIR_DIR);
    return filelib.writeFile(PAIR_SESSION_PATH, JSON.stringify(session));
}

function pairQueuePath(sessionId, deviceId) {
    return PAIR_DIR + "/" + sessionId + "." + deviceId + ".queue.json";
}

/* Every handheld queue belonging to one session, as {path, queue} pairs */
function pairListQueues(sessionId) {
    var found = [];
    if (!filelib.fileExists(PAIR_DIR)) return found;

    var prefix = sessionId + ".";
    var suffix = ".queue.json";
    var entries = filelib.readdir(PAIR_DIR, "default");

    for (var i = 0; i < entries.length; i++) {
        var name = entries[i].Filename;
        if (entries[i].IsDir) continue;
        if (name.indexOf(prefix) !== 0) continue;
        if (name.length <= suffix.length) continue;
        if (name.substring(name.length - suffix.length) !== suffix) continue;

        try {
            var queue = JSON.parse(filelib.readFile(entries[i].Filepath));
            if (!queue || !Array.isArray(queue.scans)) continue;
            found.push({ path: entries[i].Filepath, queue: queue });
        } catch (e) {
            // A torn read while the handheld is writing - it will be picked up
            // on the next pass a few hundred milliseconds later
        }
    }
    return found;
}

function pairSaveQueue(vpath, queue) {
    filelib.mkdir(PAIR_DIR);
    if (queue.scans.length > PAIR_QUEUE_CAP) {
        queue.scans = queue.scans.slice(queue.scans.length - PAIR_QUEUE_CAP);
    }
    return filelib.writeFile(vpath, JSON.stringify(queue));
}

/*
    Removes a session's queue files. When alsoSession is set the session is
    replaced by a tombstone rather than deleted, so any handheld still holding
    this pairing is told it is over instead of retrying against a missing file.
*/
function pairClear(sessionId, alsoSession) {
    var queues = pairListQueues(sessionId);
    for (var i = 0; i < queues.length; i++) {
        filelib.deleteFile(queues[i].path);
    }
    if (!alsoSession) return;

    var previous = pairReadSessionFile();
    filelib.mkdir(PAIR_DIR);
    filelib.writeFile(PAIR_SESSION_PATH, JSON.stringify({
        id: (previous && previous.id) || sessionId,
        code: (previous && previous.code) || "",
        ended: true,
        endedAt: invNow()
    }));
}

/* Handheld summary for the host's device list */
function pairDeviceSummary(queue, now) {
    var lastSeenAt = queue.lastSeenAt || queue.joinedAt || 0;
    return {
        deviceId: queue.deviceId || "",
        deviceName: queue.deviceName || "Handheld",
        joinedAt: queue.joinedAt || 0,
        lastSeenAt: lastSeenAt,
        scanCount: queue.sent || queue.scans.length,
        online: (now - lastSeenAt) < PAIR_DEVICE_STALE_MS
    };
}
