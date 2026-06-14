/*
    syncPersonLabel.js

    After the user renames a face cluster in the photoai subservice UI or
    through the Photo People panel, call this script to propagate the new
    person_name to all rows in photo_faces that share the same cluster_id.

    Request body (JSON):
      { "cluster_id": "uuid", "person_name": "Alice" }

    Response: { "ok": true, "updated": 12 }
*/

includes("imagedb.js");

function main() {
    var payload = {};
    try { payload = JSON.parse(POST_data) || {}; } catch (e) {}

    var clusterID  = payload.cluster_id  || "";
    var personName = payload.person_name || "";

    if (!clusterID) {
        sendJSONResp(JSON.stringify({ error: "cluster_id required" }));
        return;
    }

    var db = openIndexDB();
    if (db == null) {
        sendJSONResp(JSON.stringify({ error: "index unavailable" }));
        return;
    }

    db.exec(
        "UPDATE photo_faces SET person_name = ? WHERE cluster_id = ?",
        [personName, clusterID]
    );

    var row = db.queryRow(
        "SELECT COUNT(*) AS c FROM photo_faces WHERE cluster_id = ?",
        [clusterID]
    );
    db.close();

    sendJSONResp(JSON.stringify({ ok: true, updated: row ? row.c : 0 }));
}

main();
