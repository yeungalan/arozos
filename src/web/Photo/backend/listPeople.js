/*
    listPeople.js

    Return the distinct named people found in this user's photo library,
    with photo counts. Used by the People view and search suggestions.

    Response:
      { "people": [
          { "name": "Alice", "count": 47, "cluster_id": "uuid" },
          …
        ]
      }
*/

includes("imagedb.js");

function main() {
    var db = openIndexDB();
    if (db == null) {
        sendJSONResp(JSON.stringify({ people: [] }));
        return;
    }

    var rows = db.query(
        "SELECT person_name, cluster_id, COUNT(DISTINCT filepath) AS cnt" +
        " FROM photo_faces" +
        " WHERE person_name != ''" +
        " GROUP BY cluster_id" +
        " ORDER BY cnt DESC, person_name"
    );
    db.close();

    var people = [];
    for (var i = 0; i < (rows ? rows.length : 0); i++) {
        people.push({
            name:       rows[i].person_name,
            cluster_id: rows[i].cluster_id,
            count:      rows[i].cnt
        });
    }
    sendJSONResp(JSON.stringify({ people: people }));
}

main();
