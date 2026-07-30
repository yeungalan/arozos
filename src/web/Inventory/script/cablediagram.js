/*
    Inventory - cable connection diagram

    Draws the installed runs as a picture: every location that a cable reaches
    becomes a node, and every run becomes a link between two of them. A list of
    runs tells you what exists; this tells you how the place is wired, which is
    the question you actually have when you are standing in front of a rack.

    Pure string in, SVG out, no dependencies - so it renders identically in the
    app, in an export and in a test. SVG rather than a canvas because the labels
    have to stay legible when the same drawing is used on a 4.7" handheld and a
    desktop, and because nodes and links need to be tappable.

    Runs between the same pair of locations are bundled into one link carrying a
    count, otherwise a rack with forty patch leads to one room draws forty
    identical lines on top of each other.
*/

var InvCableDiagram = (function () {

    var SIZE = 420;            // viewBox is square; CSS scales it to fit
    var CENTRE = SIZE / 2;
    var NODE_H = 34;
    var MAX_NODES = 16;        // beyond this a circle stops being readable

    // Worst state in a bundle decides its colour, so a single faulty run in a
    // bundle of twenty is still visible
    var STATE_RANK = { faulty: 0, planned: 1, installed: 2, tested: 3, retired: 4 };
    var STATE_COLOUR = {
        faulty: "var(--danger)",
        planned: "var(--muted)",
        installed: "var(--accent)",
        tested: "var(--ok)",
        retired: "var(--surface-3)"
    };

    function esc(text) {
        return ("" + (text === undefined || text === null ? "" : text))
            .replace(/&/g, "&amp;").replace(/</g, "&lt;")
            .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
    }

    function shorten(text, max) {
        var value = "" + (text || "");
        return value.length > max ? value.slice(0, max - 1) + "…" : value;
    }

    /* "Rack A" and "rack a" are the same place */
    function locationKey(name) {
        return ("" + (name || "")).trim().toLowerCase();
    }

    function nodeWidth(name) {
        return Math.max(72, shorten(name, 16).length * 8.2 + 22);
    }

    /* Unit vector pointing away from the middle of the diagram */
    function outward(point) {
        var dx = point.x - CENTRE;
        var dy = point.y - CENTRE;
        var length = Math.sqrt(dx * dx + dy * dy);
        if (length < 1) return { x: 1, y: 0 };   // dead centre: go right
        return { x: dx / length, y: dy / length };
    }

    /*
        Evenly spaced around a circle, starting at the top. The radius is pulled
        in when any location has a cable to itself, because that loop and its
        label are drawn outside the ring and would otherwise be clipped by the
        edge of the drawing.
    */
    function layout(count, hasLoops) {
        var points = [];
        var radius = count <= 2 ? 120 : (count <= 5 ? 140 : 155);
        if (hasLoops) radius = Math.min(radius, 118);
        for (var i = 0; i < count; i++) {
            var angle = (2 * Math.PI * i / count) - (Math.PI / 2);
            points.push({
                x: CENTRE + radius * Math.cos(angle),
                y: CENTRE + radius * Math.sin(angle)
            });
        }
        return points;
    }

    /*
        render(runs, options) -> { svg, nodeCount, linkCount, dangling, truncated }
        options.highlight - only draw links touching this location (by name)
    */
    function render(runs, options) {
        options = options || {};
        runs = runs || [];

        var built = build(runs);
        var nodes = built.nodes;
        var bundles = built.bundles;

        if (!nodes.length) {
            return {
                svg: "", nodeCount: 0, linkCount: 0,
                dangling: built.dangling.length, truncated: 0
            };
        }

        var truncated = 0;
        if (nodes.length > MAX_NODES) {
            truncated = nodes.length - MAX_NODES;
            var kept = {};
            nodes = nodes.slice(0, MAX_NODES);
            for (var k = 0; k < nodes.length; k++) kept[nodes[k].key] = k;

            var keptBundles = [];
            for (var bi = 0; bi < bundles.length; bi++) {
                var fromKey = built.nodes[bundles[bi].a].key;
                var toKey = built.nodes[bundles[bi].b].key;
                if (kept[fromKey] === undefined || kept[toKey] === undefined) continue;
                keptBundles.push({
                    a: kept[fromKey], b: kept[toKey],
                    runs: bundles[bi].runs, state: bundles[bi].state
                });
            }
            bundles = keptBundles;
        }

        var hasLoops = false;
        for (var l = 0; l < bundles.length; l++) {
            if (bundles[l].a === bundles[l].b) { hasLoops = true; break; }
        }

        var points = layout(nodes.length, hasLoops);
        var highlight = options.highlight ? locationKey(options.highlight) : "";

        var widths = [];
        for (var w = 0; w < nodes.length; w++) {
            widths.push(nodeWidth(nodes[w].name));
        }

        var links = "";
        var labels = "";
        var exported = [];   // one entry per drawn link, in data-pair order
        var i;

        for (i = 0; i < bundles.length; i++) {
            var bundle = bundles[i];
            var from = points[bundle.a];
            var to = points[bundle.b];
            var colour = STATE_COLOUR[bundle.state] || STATE_COLOUR.planned;
            var dim = highlight !== "" &&
                nodes[bundle.a].key !== highlight && nodes[bundle.b].key !== highlight;
            var width = Math.min(6, 1.5 + bundle.runs.length * 0.6);
            var pairId = "" + i;   // index into the returned bundles array

            if (bundle.a === bundle.b) {
                /*
                    Both ends in one location - a patch lead inside a rack, say.
                    The loop is thrown outward, away from the middle of the
                    diagram, so it clears its own node instead of being drawn
                    underneath it.
                */
                var out = outward(from);
                var edge = Math.max(widths[bundle.a] / 2, NODE_H / 2) + 6;
                var loopX = from.x + out.x * (edge + 17);
                var loopY = from.y + out.y * (edge + 17);

                links += '<circle class="cd-link" data-pair="' + esc(pairId) +
                    '" cx="' + loopX + '" cy="' + loopY + '" r="16" fill="none" stroke="' +
                    colour + '" stroke-width="' + width +
                    '" opacity="' + (dim ? 0.18 : 0.9) + '"/>';

                // Beside the loop rather than beyond it, which keeps the label
                // inside the drawing however far out the node sits
                labels += linkLabel(loopX - out.y * 30, loopY + out.x * 30,
                    bundle, colour, dim, pairId);
                exported.push(bundleSummary(nodes, bundle));
                continue;
            }

            // Bowed slightly away from the centre so links do not sit on the
            // nodes they pass, and so a pair reads as one strand
            var midX = (from.x + to.x) / 2;
            var midY = (from.y + to.y) / 2;
            var bow = 0.16;
            var ctrlX = midX + (midX - CENTRE) * bow;
            var ctrlY = midY + (midY - CENTRE) * bow;

            links += '<path class="cd-link" data-pair="' + esc(pairId) + '" d="M ' +
                from.x + " " + from.y + " Q " + ctrlX + " " + ctrlY + " " + to.x + " " + to.y +
                '" fill="none" stroke="' + colour + '" stroke-width="' + width +
                '" stroke-linecap="round" opacity="' + (dim ? 0.18 : 0.9) + '"/>';

            // Quadratic midpoint, which is where the strand actually is
            var labelX = 0.25 * from.x + 0.5 * ctrlX + 0.25 * to.x;
            var labelY = 0.25 * from.y + 0.5 * ctrlY + 0.25 * to.y;
            labels += linkLabel(labelX, labelY, bundle, colour, dim, pairId);
            exported.push(bundleSummary(nodes, bundle));
        }

        var nodeSvg = "";
        for (i = 0; i < nodes.length; i++) {
            var point = points[i];
            var name = shorten(nodes[i].name, 16);
            var width2 = widths[i];
            var faded = highlight !== "" && nodes[i].key !== highlight;

            nodeSvg += '<g class="cd-node" data-location="' + esc(nodes[i].name) +
                '" opacity="' + (faded ? 0.45 : 1) + '">' +
                '<rect x="' + (point.x - width2 / 2) + '" y="' + (point.y - NODE_H / 2) +
                '" width="' + width2 + '" height="' + NODE_H + '" rx="9" ' +
                'fill="var(--surface-2)" stroke="' +
                (nodes[i].key === highlight ? "var(--accent)" : "var(--border)") +
                '" stroke-width="' + (nodes[i].key === highlight ? 2.5 : 1.5) + '"/>' +
                '<text x="' + point.x + '" y="' + (point.y + 5) +
                '" text-anchor="middle" font-size="13" font-weight="600" ' +
                'fill="var(--text)">' + esc(name) + "</text>" +
                "</g>";
        }

        var svg = '<svg class="cable-diagram" viewBox="0 0 ' + SIZE + " " + SIZE +
            '" xmlns="http://www.w3.org/2000/svg" role="img" ' +
            'aria-label="Cable runs between locations">' +
            links + labels + nodeSvg + "</svg>";

        return {
            svg: svg,
            bundles: exported,
            nodeCount: nodes.length,
            linkCount: bundles.length,
            dangling: built.dangling.length,
            truncated: truncated
        };
    }

    /* What a caller needs to list the runs behind one link */
    function bundleSummary(nodes, bundle) {
        return {
            from: nodes[bundle.a].name,
            to: nodes[bundle.b].name,
            state: bundle.state,
            runs: bundle.runs
        };
    }

    function linkLabel(x, y, bundle, colour, dim, pairId) {
        var text = bundle.runs.length === 1
            ? shorten(bundle.runs[0].label || bundle.runs[0].cableType || "cable", 14)
            : bundle.runs.length + " cables";
        var width = text.length * 6.6 + 14;

        return '<g class="cd-label" data-pair="' + esc(pairId) +
            '" opacity="' + (dim ? 0.2 : 1) + '">' +
            '<rect x="' + (x - width / 2) + '" y="' + (y - 10) + '" width="' + width +
            '" height="20" rx="6" fill="var(--surface)" stroke="' + colour +
            '" stroke-width="1"/>' +
            '<text x="' + x + '" y="' + (y + 4) + '" text-anchor="middle" font-size="11" ' +
            'font-weight="600" fill="var(--text)">' + esc(text) + "</text></g>";
    }

    /*
        Collects the locations and the bundles between them. Kept separate from
        render so a test can assert on the structure without parsing SVG.

        A run missing an end is reported separately rather than silently dropped -
        a cable with one end unrecorded is exactly what someone needs to fix.
    */
    function build(runs) {
        var nodes = [];
        var nodeIndex = {};
        var bundles = [];
        var bundleIndex = {};
        var dangling = [];
        var i;

        for (i = 0; i < runs.length; i++) {
            var run = runs[i];
            var from = ("" + (run.fromLocation || "")).trim();
            var to = ("" + (run.toLocation || "")).trim();

            if (from === "" || to === "") {
                dangling.push(run);
                continue;
            }

            var fromKey = locationKey(from);
            var toKey = locationKey(to);

            if (nodeIndex[fromKey] === undefined) {
                nodeIndex[fromKey] = nodes.length;
                nodes.push({ key: fromKey, name: from, links: 0 });
            }
            if (nodeIndex[toKey] === undefined) {
                nodeIndex[toKey] = nodes.length;
                nodes.push({ key: toKey, name: to, links: 0 });
            }

            var a = nodeIndex[fromKey];
            var b = nodeIndex[toKey];
            nodes[a].links++;
            if (b !== a) nodes[b].links++;

            var low = Math.min(a, b);
            var high = Math.max(a, b);
            var pairKey = low + "-" + high;

            if (bundleIndex[pairKey] === undefined) {
                bundleIndex[pairKey] = bundles.length;
                bundles.push({ a: low, b: high, runs: [], state: "" });
            }
            var bundle = bundles[bundleIndex[pairKey]];
            bundle.runs.push(run);

            var rank = STATE_RANK[run.state];
            if (rank === undefined) rank = STATE_RANK.planned;
            var current = bundle.state === "" ? 99 : STATE_RANK[bundle.state];
            if (current === undefined) current = 99;
            if (rank < current) bundle.state = run.state;
        }

        return { nodes: nodes, bundles: bundles, dangling: dangling };
    }

    return {
        render: render,
        build: build,
        locationKey: locationKey,
        STATE_COLOUR: STATE_COLOUR
    };
})();
