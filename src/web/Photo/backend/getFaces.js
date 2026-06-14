/*
    getFaces.js

    Return all face detections stored for a given photo.

    Request body (JSON): { "filepath": "user:/Photo/IMG.jpg" }

    Response:
      { "faces": [
          { "cluster_id": "uuid", "person_name": "Alice",
            "bbox_x": 0.12, "bbox_y": 0.08, "bbox_w": 0.18, "bbox_h": 0.24,
            "confidence": 0.82
          }, …
        ]
      }
*/

includes("imagedb.js");

function main() {
    var payload = {};
    try { payload = JSON.parse(POST_data) || {}; } catch (e) {}

    var filepath = payload.filepath || "";
    if (!filepath) {
        sendJSONResp(JSON.stringify({ error: "filepath required" }));
        return;
    }

    var db = openIndexDB();
    if (db == null) {
        sendJSONResp(JSON.stringify({ faces: [] }));
        return;
    }

    var rows = db.query(
        "SELECT cluster_id, person_name, bbox_x, bbox_y, bbox_w, bbox_h, confidence" +
        " FROM photo_faces WHERE filepath = ? ORDER BY bbox_x",
        [filepath]
    );
    db.close();

    sendJSONResp(JSON.stringify({ faces: rows || [] }));
}

main();
