/*
    listTags.js

    Return all distinct tags in the user's library with photo counts.
    Used by the tag browser and search suggestions.

    Response:
      { "tags": [ { "tag": "warm", "count": 120 }, … ] }
*/

includes("imagedb.js");

function main() {
    var db = openIndexDB();
    if (db == null) {
        sendJSONResp(JSON.stringify({ tags: [] }));
        return;
    }

    var rows = db.query(
        "SELECT tag, COUNT(DISTINCT filepath) AS cnt" +
        " FROM photo_tags" +
        " GROUP BY tag" +
        " ORDER BY cnt DESC, tag"
    );
    db.close();

    var tags = [];
    for (var i = 0; i < (rows ? rows.length : 0); i++) {
        tags.push({ tag: rows[i].tag, count: rows[i].cnt });
    }
    sendJSONResp(JSON.stringify({ tags: tags }));
}

main();
