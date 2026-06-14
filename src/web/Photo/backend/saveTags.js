/*
    saveTags.js

    Store AI-generated or user-defined tags for a photo.

    Request body (JSON):
      { "filepath": "user:/Photo/IMG.jpg",
        "tags":     ["warm","landscape","people"],
        "source":   "ai"   // "ai" or "user" (default: "user")
      }

    When source is "ai" the existing AI tags for this filepath are replaced
    entirely; when source is "user" the supplied tags are merged in.

    Response: { "ok": true }
*/

includes("imagedb.js");

function main() {
    var payload = {};
    try { payload = JSON.parse(POST_data) || {}; } catch (e) {}

    var filepath = payload.filepath || "";
    var tags     = payload.tags || [];
    var source   = payload.source || "user";

    if (!filepath) {
        sendJSONResp(JSON.stringify({ error: "filepath required" }));
        return;
    }
    if (!Array.isArray(tags) || tags.length === 0) {
        sendJSONResp(JSON.stringify({ error: "tags array required" }));
        return;
    }

    var db = openIndexDB();
    if (db == null) {
        sendJSONResp(JSON.stringify({ error: "index unavailable" }));
        return;
    }

    var now = Math.floor(Date.now() / 1000);

    if (source === "ai") {
        // Replace existing AI tags for this photo.
        db.exec("DELETE FROM photo_tags WHERE filepath = ? AND source = 'ai'", [filepath]);
    }

    for (var i = 0; i < tags.length; i++) {
        var tag = ("" + tags[i]).toLowerCase().trim();
        if (!tag) { continue; }
        db.exec(
            "INSERT OR IGNORE INTO photo_tags (filepath, tag, source, created_at) VALUES (?,?,?,?)",
            [filepath, tag, source, now]
        );
    }

    db.close();
    sendJSONResp(JSON.stringify({ ok: true }));
}

main();
