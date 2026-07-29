/*
    Inventory - keyword + fuzzy search

    Runs entirely in the browser over the item list loaded at start-up, so a
    query is answered without a round trip. That matters on a handheld: the
    operator types with one thumb while holding a carton, and every keystroke
    re-ranks the list instantly even on a weak warehouse Wi-Fi link.

    Ranking, strongest first:
        exact field match > field starts with term > word in field starts with
        term > field contains term > fuzzy subsequence match

    A query is split on whitespace and every term must match *some* field
    (AND semantics), which is what makes "milk a3" find the milk in aisle A3.
*/

var InvSearch = (function () {

    // Field weights: scan keys beat descriptive text, so typing a partial
    // barcode outranks an item that merely mentions those digits in its notes.
    var FIELDS = [
        { key: "barcode", weight: 1.30 },
        { key: "sku", weight: 1.15 },
        { key: "serial", weight: 1.05 },
        { key: "name", weight: 1.00 },
        { key: "location", weight: 0.85 },
        { key: "category", weight: 0.80 },
        { key: "supplier", weight: 0.65 },
        { key: "notes", weight: 0.55 }
    ];

    function norm(value) {
        return (value === undefined || value === null) ? "" : ("" + value).toLowerCase().trim();
    }

    /* Splits a query into terms; quoted phrases stay together */
    function tokenize(query) {
        var text = norm(query);
        if (text === "") return [];

        var terms = [];
        var re = /"([^"]+)"|(\S+)/g;
        var match;
        while ((match = re.exec(text)) !== null) {
            var term = (match[1] !== undefined ? match[1] : match[2]).trim();
            if (term !== "") terms.push(term);
        }
        return terms;
    }

    /*
        Subsequence match: every character of `term` appears in `hay` in order.
        Scores 0.15-0.65 based on how tightly packed the matched characters
        are, which keeps it strictly below a real substring hit (0.70+).
    */
    function fuzzyScore(term, hay) {
        var ti = 0;
        var streak = 0;
        var bestStreak = 0;
        var firstIndex = -1;
        var lastIndex = -1;

        for (var hi = 0; hi < hay.length && ti < term.length; hi++) {
            if (hay.charAt(hi) === term.charAt(ti)) {
                if (firstIndex < 0) firstIndex = hi;
                lastIndex = hi;
                ti++;
                streak++;
                if (streak > bestStreak) bestStreak = streak;
            } else {
                streak = 0;
            }
        }

        if (ti < term.length) return 0;

        var span = lastIndex - firstIndex + 1;
        var density = term.length / span;             // 1 when contiguous
        var contiguity = bestStreak / term.length;    // 1 when contiguous
        var startBonus = (firstIndex === 0) ? 0.10 : 0;

        return 0.15 + (0.30 * density) + (0.10 * contiguity) + startBonus;
    }

    /* Does any whitespace/punctuation-delimited word in `hay` start with `term`? */
    function wordStartsWith(term, hay) {
        var words = hay.split(/[\s\-_/.,;:()\[\]]+/);
        for (var i = 0; i < words.length; i++) {
            if (words[i].length > 0 && words[i].indexOf(term) === 0) return true;
        }
        return false;
    }

    /* Score of one term against one field value, 0 (no match) to 1 (exact) */
    function scoreField(term, value) {
        var hay = norm(value);
        if (hay === "" || term === "") return 0;

        // The bands below never overlap: exact 1.00 > prefix 0.84-0.95 >
        // word-start 0.83 > contains 0.70-0.78 > fuzzy 0.15-0.65. Keeping them
        // disjoint is what makes the ranking predictable to an operator.
        if (hay === term) return 1.00;
        if (hay.indexOf(term) === 0) {
            // Prefix hit: the closer the lengths, the better the match
            return 0.84 + (0.11 * (term.length / hay.length));
        }
        if (wordStartsWith(term, hay)) return 0.83;

        var at = hay.indexOf(term);
        if (at > 0) {
            // Later in the string is a weaker signal, but never below 0.70
            return 0.78 - Math.min(0.08, at * 0.004);
        }

        // Single characters are too noisy to fuzzy match
        if (term.length < 2) return 0;
        return fuzzyScore(term, hay);
    }

    /*
        Scores one item against all query terms.
        Returns 0 when any term fails to match, so results stay relevant as the
        operator keeps typing rather than drifting into everything-matches.
    */
    function scoreItem(terms, item) {
        var total = 0;

        for (var t = 0; t < terms.length; t++) {
            var best = 0;
            for (var f = 0; f < FIELDS.length; f++) {
                var fieldScore = scoreField(terms[t], item[FIELDS[f].key]);
                if (fieldScore <= 0) continue;
                var weighted = fieldScore * FIELDS[f].weight;
                if (weighted > best) best = weighted;
            }
            if (best === 0) return 0;
            total += best;
        }

        // Nudge shorter names up so "AA battery" beats "AA battery holder kit"
        var nameLength = norm(item.name).length;
        if (nameLength > 0) total += 0.02 * (1 / (1 + (nameLength / 40)));

        return total;
    }

    /*
        search(items, query, options) -> [{ item, score }]
        options.limit   maximum results (default 200)
        options.filter  optional function(item) -> bool applied before scoring
    */
    function search(items, query, options) {
        options = options || {};
        var limit = options.limit || 200;
        var filter = options.filter;
        var terms = tokenize(query);
        var results = [];
        var i;

        if (terms.length === 0) {
            for (i = 0; i < items.length; i++) {
                if (filter && !filter(items[i])) continue;
                results.push({ item: items[i], score: 0 });
            }
            results.sort(function (a, b) {
                var an = norm(a.item.name), bn = norm(b.item.name);
                return an === bn ? 0 : (an < bn ? -1 : 1);
            });
            return results.slice(0, limit);
        }

        for (i = 0; i < items.length; i++) {
            if (filter && !filter(items[i])) continue;
            var score = scoreItem(terms, items[i]);
            if (score > 0) results.push({ item: items[i], score: score });
        }

        results.sort(function (a, b) {
            if (b.score !== a.score) return b.score - a.score;
            return norm(a.item.name) < norm(b.item.name) ? -1 : 1;
        });

        return results.slice(0, limit);
    }

    function escapeHtml(text) {
        return ("" + (text === undefined || text === null ? "" : text))
            .replace(/&/g, "&amp;")
            .replace(/</g, "&lt;")
            .replace(/>/g, "&gt;")
            .replace(/"/g, "&quot;");
    }

    /*
        Wraps literal term occurrences in <mark>. Fuzzy-only hits are left
        unmarked on purpose - highlighting scattered single letters reads as
        noise on a small screen.
    */
    function highlight(text, query) {
        var raw = (text === undefined || text === null) ? "" : ("" + text);
        var terms = tokenize(query);
        if (terms.length === 0) return escapeHtml(raw);

        // Longest first, so "battery" wins over "bat" on overlapping ranges
        terms.sort(function (a, b) { return b.length - a.length; });

        // Ranges are found in the raw text and escaped segment by segment, so a
        // term can never land inside an HTML entity we introduced ourselves.
        var lower = raw.toLowerCase();
        var marks = [];   // [start, end) ranges to wrap
        var i;

        for (i = 0; i < terms.length; i++) {
            var term = terms[i];
            if (term.length < 1) continue;
            var from = 0;
            while (true) {
                var at = lower.indexOf(term, from);
                if (at === -1) break;
                marks.push([at, at + term.length]);
                from = at + term.length;
            }
        }
        if (marks.length === 0) return escapeHtml(raw);

        marks.sort(function (a, b) { return a[0] - b[0]; });

        // Merge overlaps so nested <mark> tags can never be emitted
        var merged = [marks[0]];
        for (i = 1; i < marks.length; i++) {
            var last = merged[merged.length - 1];
            if (marks[i][0] <= last[1]) {
                if (marks[i][1] > last[1]) last[1] = marks[i][1];
            } else {
                merged.push(marks[i]);
            }
        }

        var out = "";
        var cursor = 0;
        for (i = 0; i < merged.length; i++) {
            out += escapeHtml(raw.slice(cursor, merged[i][0]));
            out += "<mark>" + escapeHtml(raw.slice(merged[i][0], merged[i][1])) + "</mark>";
            cursor = merged[i][1];
        }
        out += escapeHtml(raw.slice(cursor));
        return out;
    }

    return {
        search: search,
        tokenize: tokenize,
        scoreItem: scoreItem,
        scoreField: scoreField,
        fuzzyScore: fuzzyScore,
        highlight: highlight,
        escapeHtml: escapeHtml
    };
})();
