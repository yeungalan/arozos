/*
    saveFaces.js

    Store face detection results (from the photoai subservice) into the per-user
    photo index so the Photo app can search by person name.

    Request body (JSON):
      { "filepath": "user:/Photo/IMG.jpg",
        "faces": [
          { "cluster_id":   "uuid",
            "person_name":  "Alice",   // may be empty if not yet labelled
            "bbox_x": 0.12, "bbox_y": 0.08,
            "bbox_w": 0.18, "bbox_h": 0.24,
            "confidence":   0.82
          }, …
        ]
      }

    Upserts on (filepath, cluster_id): later syncs from the subservice
    (e.g. after the user labels a cluster) update the person_name.

    Response: { "ok": true }
*/

includes("imagedb.js");

function main() {
    var payload = {};
    try { payload = JSON.parse(POST_data) || {}; } catch (e) {}

    var filepath = payload.filepath || "";
    var faces    = payload.faces || [];

    if (!filepath) {
        sendJSONResp(JSON.stringify({ error: "filepath required" }));
        return;
    }
    if (!Array.isArray(faces) || faces.length === 0) {
        sendJSONResp(JSON.stringify({ ok: true, saved: 0 }));
        return;
    }

    var db = openIndexDB();
    if (db == null) {
        sendJSONResp(JSON.stringify({ error: "index unavailable" }));
        return;
    }

    var now = Math.floor(Date.now() / 1000);
    var saved = 0;

    for (var i = 0; i < faces.length; i++) {
        var f = faces[i];
        if (!f || !f.cluster_id) { continue; }
        var personName = f.person_name || "";
        db.exec(
            "INSERT INTO photo_faces " +
            "    (filepath, cluster_id, person_name, bbox_x, bbox_y, bbox_w, bbox_h, confidence, created_at)" +
            " VALUES (?,?,?,?,?,?,?,?,?)" +
            " ON CONFLICT(filepath, cluster_id) DO UPDATE SET" +
            "    person_name = excluded.person_name," +
            "    bbox_x = excluded.bbox_x, bbox_y = excluded.bbox_y," +
            "    bbox_w = excluded.bbox_w, bbox_h = excluded.bbox_h," +
            "    confidence = excluded.confidence",
            [filepath, f.cluster_id, personName,
             f.bbox_x || 0, f.bbox_y || 0, f.bbox_w || 0, f.bbox_h || 0,
             f.confidence || 0, now]
        );
        saved++;
    }

    db.close();
    sendJSONResp(JSON.stringify({ ok: true, saved: saved }));
}

main();
