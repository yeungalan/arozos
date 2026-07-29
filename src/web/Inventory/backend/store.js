/*
    Inventory - shared data store helpers

    Included by every Inventory backend script via includes("store.js").
    Runs inside the AGI (Otto, ES5) VM, so keep to ES5 syntax only.

    On-disk layout (per user):
        user:/Document/Inventory/inventory.json  { items, locations, settings }
        user:/Document/Inventory/movements.json  { movements }
*/
requirelib("filelib");

var INV_DIR = "user:/Document/Inventory";
var INV_DATA_PATH = INV_DIR + "/inventory.json";
var INV_MOVE_PATH = INV_DIR + "/movements.json";

// Movement log is trimmed to this many newest entries on every write so a
// long-running handheld never grows an unbounded journal file.
var INV_MOVE_CAP = 5000;

/* Default user settings, also the schema for saveSettings.agi */
function invDefaultSettings() {
    return {
        currency: "$",
        expiryWarnDays: 30,      // "expiring soon" window
        warrantyWarnDays: 30,    // "warranty ending soon" window
        defaultStep: 1,          // qty added/removed per scan
        beep: true,              // audible scan feedback
        vibrate: true,           // haptic scan feedback
        scanAnywhere: true,      // capture wedge scans even without field focus
        softKeyboard: false      // suppress the on-screen keyboard on handhelds
    };
}

function invNow() {
    return new Date().getTime();
}

/* Collision-resistant enough for a single-user store without a DB sequence */
function invNewId(prefix) {
    return prefix + invNow().toString(36) + Math.random().toString(36).slice(2, 7);
}

function invNormaliseItem(item) {
    if (!item || typeof item !== "object") return null;

    var num = function (v, fallback) {
        var n = parseFloat(v);
        return isNaN(n) ? fallback : n;
    };
    var str = function (v) {
        return (v === undefined || v === null) ? "" : ("" + v).trim();
    };

    return {
        id: str(item.id),
        barcode: str(item.barcode),
        name: str(item.name),
        sku: str(item.sku),
        category: str(item.category),
        qty: num(item.qty, 0),
        unit: str(item.unit) || "pcs",
        minQty: num(item.minQty, 0),
        location: str(item.location),
        price: num(item.price, 0),
        expiryDate: str(item.expiryDate),      // ISO yyyy-mm-dd, "" when N/A
        warrantyEnd: str(item.warrantyEnd),    // ISO yyyy-mm-dd, "" when N/A
        supplier: str(item.supplier),
        serial: str(item.serial),
        notes: str(item.notes),
        createdAt: num(item.createdAt, invNow()),
        updatedAt: num(item.updatedAt, invNow())
    };
}

/* Reads the store, repairing/seeding anything missing or corrupt */
function invLoad() {
    var data = null;
    if (filelib.fileExists(INV_DATA_PATH)) {
        try {
            var parsed = JSON.parse(filelib.readFile(INV_DATA_PATH));
            if (parsed && typeof parsed === "object") data = parsed;
        } catch (e) {
            data = null;
        }
    }
    if (!data) data = {};
    if (!Array.isArray(data.items)) data.items = [];
    if (!Array.isArray(data.locations)) data.locations = [];
    if (!data.settings || typeof data.settings !== "object") data.settings = {};

    // Merge in any setting key added by a newer version of the app
    var defaults = invDefaultSettings();
    for (var key in defaults) {
        if (data.settings[key] === undefined) data.settings[key] = defaults[key];
    }
    return data;
}

function invSave(data) {
    filelib.mkdir(INV_DIR);
    return filelib.writeFile(INV_DATA_PATH, JSON.stringify(data));
}

function invLoadMovements() {
    if (!filelib.fileExists(INV_MOVE_PATH)) return [];
    try {
        var parsed = JSON.parse(filelib.readFile(INV_MOVE_PATH));
        if (parsed && Array.isArray(parsed.movements)) return parsed.movements;
    } catch (e) {}
    return [];
}

function invSaveMovements(movements) {
    filelib.mkdir(INV_DIR);
    // Newest first, capped
    if (movements.length > INV_MOVE_CAP) {
        movements = movements.slice(0, INV_MOVE_CAP);
    }
    return filelib.writeFile(INV_MOVE_PATH, JSON.stringify({ movements: movements }));
}

/*
    Appends one journal entry and returns it.
    type: "in" | "out" | "set" | "move" | "create" | "edit" | "delete"
*/
function invAddMovement(entry) {
    var movements = invLoadMovements();
    var record = {
        id: invNewId("mv_"),
        itemId: entry.itemId || "",
        barcode: entry.barcode || "",
        name: entry.name || "",
        type: entry.type || "edit",
        delta: (entry.delta === undefined || entry.delta === null) ? 0 : parseFloat(entry.delta),
        qtyAfter: (entry.qtyAfter === undefined || entry.qtyAfter === null) ? 0 : parseFloat(entry.qtyAfter),
        fromLocation: entry.fromLocation || "",
        toLocation: entry.toLocation || "",
        note: entry.note || "",
        ts: invNow()
    };
    if (isNaN(record.delta)) record.delta = 0;
    if (isNaN(record.qtyAfter)) record.qtyAfter = 0;

    movements.unshift(record);
    invSaveMovements(movements);
    return record;
}

/* Index of the item with the given id, or -1 */
function invIndexOfId(data, id) {
    for (var i = 0; i < data.items.length; i++) {
        if (data.items[i].id === id) return i;
    }
    return -1;
}

/* Index of the item carrying the given barcode (case-insensitive), or -1 */
function invIndexOfBarcode(data, barcode) {
    var needle = ("" + barcode).trim().toLowerCase();
    if (needle === "") return -1;
    for (var i = 0; i < data.items.length; i++) {
        if (("" + data.items[i].barcode).trim().toLowerCase() === needle) return i;
    }
    return -1;
}

/* Registers a location name in the known-locations list if it is new */
function invRememberLocation(data, location) {
    var name = ("" + (location || "")).trim();
    if (name === "") return;
    for (var i = 0; i < data.locations.length; i++) {
        if (data.locations[i].toLowerCase() === name.toLowerCase()) return;
    }
    data.locations.push(name);
    data.locations.sort();
}
