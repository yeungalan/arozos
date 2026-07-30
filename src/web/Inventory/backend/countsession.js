/*
    Inventory - blind cycle count sessions

    A blind count is one where the operator never sees the book quantity while
    counting: they just keep pulling the trigger, one scan per unit, and the
    tally builds up. The comparison against what the system thinks happens
    afterwards, on a review screen, which is the whole point - seeing the
    expected number first is what biases a count.

    The tally lives on the server so a long count survives a reload, a flat
    battery or a swap to another device mid-aisle.
*/

var COUNT_PATH = INV_DIR + "/countsession.json";

function countLoad() {
    if (!filelib.fileExists(COUNT_PATH)) return null;
    try {
        var session = JSON.parse(filelib.readFile(COUNT_PATH));
        if (session && session.id && Array.isArray(session.lines)) return session;
    } catch (e) {}
    return null;
}

function countSave(session) {
    filelib.mkdir(INV_DIR);
    return filelib.writeFile(COUNT_PATH, JSON.stringify(session));
}

function countClear() {
    if (filelib.fileExists(COUNT_PATH)) filelib.deleteFile(COUNT_PATH);
}

/*
    One line per item, or per batch when the item is batch tracked - counting a
    batch-tracked product without saying which lot would produce a number that
    cannot be posted anywhere.
*/
function countLineKey(itemId, batchId) {
    return itemId + "|" + (batchId || "");
}

function countIndexOfLine(session, itemId, batchId) {
    var key = countLineKey(itemId, batchId);
    for (var i = 0; i < session.lines.length; i++) {
        if (countLineKey(session.lines[i].itemId, session.lines[i].batchId) === key) return i;
    }
    return -1;
}
