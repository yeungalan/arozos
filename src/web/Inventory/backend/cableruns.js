/*
    Inventory - installed cable runs

    The stock side of cables lives on ordinary items (kind "cable", with type,
    length and the connector on each end). This is the other half: a register of
    cables that are already installed, recording what each end is plugged into.

    A run is not stock. It is one physical cable in place, so it has endpoints
    and a state rather than a quantity, and it can point back at the stock item
    it was cut or drawn from.
*/

var RUN_PATH = INV_DIR + "/cableruns.json";
var RUN_STATES = ["planned", "installed", "tested", "faulty", "retired"];

function runNormalise(run) {
    if (!run || typeof run !== "object") return null;

    var state = invStr(run.state).toLowerCase();
    if (RUN_STATES.indexOf(state) === -1) state = "planned";

    return {
        id: invStr(run.id),
        label: invStr(run.label),           // what is printed on the cable
        barcode: invStr(run.barcode),       // scan the label to open the run
        cableType: invStr(run.cableType),
        length: invNum(run.length, 0),      // metres
        fromLocation: invStr(run.fromLocation),
        fromPort: invStr(run.fromPort),
        toLocation: invStr(run.toLocation),
        toPort: invStr(run.toPort),
        state: state,
        itemId: invStr(run.itemId),         // stock item this cable came from
        testedOn: invStr(run.testedOn),     // ISO yyyy-mm-dd, "" when untested
        notes: invStr(run.notes),
        createdAt: invNum(run.createdAt, invNow()),
        updatedAt: invNum(run.updatedAt, invNow())
    };
}

function runLoad() {
    if (!filelib.fileExists(RUN_PATH)) return { runs: [] };
    try {
        var parsed = JSON.parse(filelib.readFile(RUN_PATH));
        if (parsed && Array.isArray(parsed.runs)) return parsed;
    } catch (e) {}
    return { runs: [] };
}

function runSave(store) {
    filelib.mkdir(INV_DIR);
    return filelib.writeFile(RUN_PATH, JSON.stringify(store));
}

function runIndexOfId(store, id) {
    for (var i = 0; i < store.runs.length; i++) {
        if (store.runs[i].id === id) return i;
    }
    return -1;
}

/* A run whose label or barcode matches a scan, case-insensitively */
function runFindByCode(store, code) {
    var needle = invStr(code).toLowerCase();
    if (needle === "") return null;
    for (var i = 0; i < store.runs.length; i++) {
        var run = store.runs[i];
        if (invStr(run.barcode).toLowerCase() === needle) return run;
        if (invStr(run.label).toLowerCase() === needle) return run;
    }
    return null;
}
