/*
    getTags.js

    Return all tags for a given photo.

    Request body (JSON): { "filepath": "user:/Photo/IMG.jpg" }

    Response: { "tags": [ { "tag": "warm", "source": "ai" }, … ] }
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
        sendJSONResp(JSON.stringify({ tags: [] }));
        return;
    }

    var rows = db.query(
        "SELECT tag, source FROM photo_tags WHERE filepath = ? ORDER BY source, tag",
        [filepath]
    );
    db.close();

    sendJSONResp(JSON.stringify({ tags: rows || [] }));
}

main();
