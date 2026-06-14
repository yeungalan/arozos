/*
    deleteTags.js

    Remove one or more user tags from a photo.

    Request body (JSON):
      { "filepath": "user:/Photo/IMG.jpg",
        "tags":     ["warm"],       // optional: specific tags to remove
        "source":   "user"          // optional: clear all tags with this source
      }

    If both tags and source are given, only tags matching both criteria are removed.
    Response: { "ok": true }
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
        sendJSONResp(JSON.stringify({ error: "index unavailable" }));
        return;
    }

    var tags   = payload.tags;
    var source = payload.source || "";

    if (Array.isArray(tags) && tags.length > 0) {
        for (var i = 0; i < tags.length; i++) {
            var t = ("" + tags[i]).toLowerCase().trim();
            if (!t) { continue; }
            if (source) {
                db.exec("DELETE FROM photo_tags WHERE filepath=? AND tag=? AND source=?", [filepath, t, source]);
            } else {
                db.exec("DELETE FROM photo_tags WHERE filepath=? AND tag=?", [filepath, t]);
            }
        }
    } else if (source) {
        db.exec("DELETE FROM photo_tags WHERE filepath=? AND source=?", [filepath, source]);
    } else {
        db.exec("DELETE FROM photo_tags WHERE filepath=?", [filepath]);
    }

    db.close();
    sendJSONResp(JSON.stringify({ ok: true }));
}

main();
