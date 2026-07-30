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

/* Accepted values for the softKeyboard policy */
var INV_SOFT_KEYBOARD_MODES = ["auto", "always", "never"];

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

        // On-screen keyboard policy for the scan and search fields:
        //   "auto"   - decided by the device (suppressed wherever a soft
        //              keyboard exists, so a handheld never pops the IME when
        //              the app re-arms a field for the next trigger pull)
        //   "always" - never suppressed, for typing codes by hand
        //   "never"  - always suppressed
        softKeyboard: "auto"
    };
}

function invNow() {
    return new Date().getTime();
}

/* Collision-resistant enough for a single-user store without a DB sequence */
function invNewId(prefix) {
    return prefix + invNow().toString(36) + Math.random().toString(36).slice(2, 7);
}

function invNum(v, fallback) {
    var n = parseFloat(v);
    return isNaN(n) ? fallback : n;
}

function invStr(v) {
    return (v === undefined || v === null) ? "" : ("" + v).trim();
}

/*
    One batch (lot) of a product: its own lot number, expiry and quantity.
    Nothing else about the product is duplicated - a batch belongs to exactly
    one item, which is why search can show the item once with all its batches.
*/
function invNormaliseBatch(batch) {
    if (!batch || typeof batch !== "object") return null;
    return {
        id: invStr(batch.id) || invNewId("bt_"),
        batch: invStr(batch.batch),            // lot / batch number as printed
        expiryDate: invStr(batch.expiryDate),  // ISO yyyy-mm-dd, "" when N/A
        qty: invNum(batch.qty, 0),
        receivedAt: invNum(batch.receivedAt, invNow()),
        note: invStr(batch.note)
    };
}

/*
    Rolls batch quantities and expiries up onto the item.

    Everything else in the app - search, alerts, the CSV export, the item rows -
    reads item.qty and item.expiryDate. Deriving those from the batches keeps all
    of it working unchanged, with the earliest expiry surfacing as the item's,
    which is the one that matters for a shelf-life warning.
*/
function invDeriveFromBatches(item) {
    if (!item || !Array.isArray(item.batches) || !item.batches.length) return item;

    var total = 0;
    var earliest = "";
    for (var i = 0; i < item.batches.length; i++) {
        total += item.batches[i].qty;
        var expiry = item.batches[i].expiryDate;
        if (expiry === "") continue;
        if (earliest === "" || expiry < earliest) earliest = expiry;
    }
    item.qty = total;
    item.expiryDate = earliest;
    return item;
}

function invNormaliseItem(item) {
    if (!item || typeof item !== "object") return null;

    var num = invNum;
    var str = invStr;

    var batches = [];
    if (Array.isArray(item.batches)) {
        for (var i = 0; i < item.batches.length; i++) {
            var batch = invNormaliseBatch(item.batches[i]);
            if (batch) batches.push(batch);
        }
    }

    var kind = str(item.kind).toLowerCase();
    if (kind !== "cable") kind = "item";

    var normalised = {
        id: str(item.id),
        kind: kind,                            // "item" | "cable"
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

        // Lots, when this product is batch tracked. Empty means it is not, and
        // the item's own qty and expiryDate are used directly.
        batches: batches,

        // Cable stock: the details that decide whether a given cable will do
        cableType: str(item.cableType),         // Cat6a, HDMI 2.1, IEC C13...
        cableLength: num(item.cableLength, 0),  // metres, 0 when not applicable
        connectorA: str(item.connectorA),
        connectorB: str(item.connectorB),

        createdAt: num(item.createdAt, invNow()),
        updatedAt: num(item.updatedAt, invNow())
    };

    return invDeriveFromBatches(normalised);
}

/* Index of a batch within an item, or -1 */
function invIndexOfBatch(item, batchId) {
    if (!item || !Array.isArray(item.batches)) return -1;
    for (var i = 0; i < item.batches.length; i++) {
        if (item.batches[i].id === batchId) return i;
    }
    return -1;
}

/* True when stock operations on this item have to name a batch */
function invIsBatchTracked(item) {
    return !!item && Array.isArray(item.batches) && item.batches.length > 0;
}

/*
    Batches ordered by expiry, earliest first, with undated lots last. Used to
    present the batch picker in the order an operator would normally pick.
*/
function invBatchesByExpiry(item) {
    var ordered = item.batches.slice();
    ordered.sort(function (a, b) {
        if (a.expiryDate === b.expiryDate) return a.receivedAt - b.receivedAt;
        if (a.expiryDate === "") return 1;
        if (b.expiryDate === "") return -1;
        return a.expiryDate < b.expiryDate ? -1 : 1;
    });
    return ordered;
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

    /*
        Bring every stored item up to the current shape. Items written before
        batches, cable fields or the kind flag existed simply do not have them,
        and any code reading item.batches would fault on the first legacy row.
        Normalising here means every consumer sees one consistent shape, and the
        next save quietly persists the migration.
    */
    var repaired = [];
    for (var n = 0; n < data.items.length; n++) {
        var normalised = invNormaliseItem(data.items[n]);
        if (normalised && normalised.id !== "") repaired.push(normalised);
    }
    data.items = repaired;
    if (!Array.isArray(data.locations)) data.locations = [];
    if (!data.settings || typeof data.settings !== "object") data.settings = {};

    // Merge in any setting key added by a newer version of the app
    var defaults = invDefaultSettings();
    for (var key in defaults) {
        if (data.settings[key] === undefined) data.settings[key] = defaults[key];
    }

    // softKeyboard used to be a boolean; carry old stores over to the policy
    if (typeof data.settings.softKeyboard === "boolean") {
        data.settings.softKeyboard = data.settings.softKeyboard ? "always" : "auto";
    }
    if (INV_SOFT_KEYBOARD_MODES.indexOf(data.settings.softKeyboard) === -1) {
        data.settings.softKeyboard = "auto";
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
        batch: entry.batch || "",        // lot number, when the item is batch tracked
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
