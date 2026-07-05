/*
    Cine Studio - keyframe animation engine

    Any animatable clip property (position, scale, rotation, opacity,
    volume) can hold a list of keyframes instead of a single static
    value. Keyframes live in clip.props.kf[prop] as an ascending list of
    { t, v, ease } where t is seconds measured from the clip's start on
    the timeline. CS.keyframes.valueAt() interpolates the value under the
    playhead; the compositor (player.js) and audio mix bus read through
    it so animation flows into both the live preview and the export.
*/
"use strict";

window.CS = window.CS || {};

CS.keyframes = {
    //Animatable properties and their default (un-keyframed) values
    PROPS: {
        x:        { def: 0 },
        y:        { def: 0 },
        scale:    { def: 100 },
        rotation: { def: 0 },
        opacity:  { def: 100 },
        volume:   { def: 100 }
    },

    EASE_OPTIONS: [
        { v: "linear",    l: "Linear" },
        { v: "easein",    l: "Ease In" },
        { v: "easeout",   l: "Ease Out" },
        { v: "easeinout", l: "Ease In / Out" }
    ],

    //Merge duplicate keyframes closer than this many seconds apart
    EPS: 0.001,

    /* ---------- data access ---------- */

    list: function (clip, prop) {
        return (clip.props && clip.props.kf && clip.props.kf[prop]) || null;
    },

    has: function (clip, prop) {
        var l = CS.keyframes.list(clip, prop);
        return !!(l && l.length);
    },

    //Any animatable property on the clip is keyframed
    hasAny: function (clip) {
        var kf = clip.props && clip.props.kf;
        if (!kf) { return false; }
        for (var k in kf) {
            if (kf[k] && kf[k].length) { return true; }
        }
        return false;
    },

    staticValue: function (clip, prop) {
        var v = clip.props ? clip.props[prop] : undefined;
        if (v === undefined) {
            var def = CS.keyframes.PROPS[prop];
            return def ? def.def : 0;
        }
        return v;
    },

    //Local time (seconds from clip start) for a timeline time t
    localTime: function (clip, t) {
        return (t === undefined ? CS.state.playhead : t) - clip.start;
    },

    /* ---------- interpolation ---------- */

    ease: function (f, mode) {
        if (f <= 0) { return 0; }
        if (f >= 1) { return 1; }
        switch (mode) {
            case "easein":    return f * f;
            case "easeout":   return 1 - (1 - f) * (1 - f);
            case "easeinout": return f < 0.5 ? 2 * f * f : 1 - Math.pow(-2 * f + 2, 2) / 2;
            default:          return f; //linear
        }
    },

    //Value of prop under timeline time t, honouring keyframes if present
    valueAt: function (clip, prop, t) {
        var l = CS.keyframes.list(clip, prop);
        if (!l || !l.length) { return CS.keyframes.staticValue(clip, prop); }
        var lt = CS.keyframes.localTime(clip, t);
        if (lt <= l[0].t) { return l[0].v; }
        if (lt >= l[l.length - 1].t) { return l[l.length - 1].v; }
        for (var i = 0; i < l.length - 1; i++) {
            var a = l[i], b = l[i + 1];
            if (lt >= a.t && lt <= b.t) {
                var span = b.t - a.t;
                var f = span <= CS.keyframes.EPS ? 1 : (lt - a.t) / span;
                //Segment easing is carried on the keyframe it eases toward
                f = CS.keyframes.ease(f, b.ease || "linear");
                return a.v + (b.v - a.v) * f;
            }
        }
        return l[l.length - 1].v;
    },

    //Resolve every animatable transform property at time t at once
    resolveTransform: function (clip, t) {
        return {
            x:        CS.keyframes.valueAt(clip, "x", t),
            y:        CS.keyframes.valueAt(clip, "y", t),
            scale:    CS.keyframes.valueAt(clip, "scale", t),
            rotation: CS.keyframes.valueAt(clip, "rotation", t),
            opacity:  CS.keyframes.valueAt(clip, "opacity", t)
        };
    },

    /* ---------- editing (callers commit history) ---------- */

    ensureBag: function (clip) {
        if (!clip.props.kf) { clip.props.kf = {}; }
        return clip.props.kf;
    },

    //Index of a keyframe at (near) local time lt, or -1
    indexAt: function (l, lt) {
        for (var i = 0; i < l.length; i++) {
            if (Math.abs(l[i].t - lt) < CS.keyframes.EPS) { return i; }
        }
        return -1;
    },

    hasKeyAt: function (clip, prop, t) {
        var l = CS.keyframes.list(clip, prop);
        if (!l) { return false; }
        return CS.keyframes.indexAt(l, CS.keyframes.localTime(clip, t)) >= 0;
    },

    //Turn keyframing on for prop, seeding a keyframe at the playhead
    enable: function (clip, prop, t) {
        if (CS.keyframes.has(clip, prop)) { return; }
        var bag = CS.keyframes.ensureBag(clip);
        var v = CS.keyframes.staticValue(clip, prop);
        bag[prop] = [{ t: CS.keyframes.localTime(clip, t), v: v, ease: "linear" }];
    },

    //Turn keyframing off, baking the value under the playhead as static
    disable: function (clip, prop, t) {
        if (!CS.keyframes.has(clip, prop)) { return; }
        clip.props[prop] = CS.keyframes.valueAt(clip, prop, t);
        delete clip.props.kf[prop];
    },

    //Add or update the keyframe at the playhead for prop = v
    setAt: function (clip, prop, t, v) {
        var bag = CS.keyframes.ensureBag(clip);
        var lt = CS.keyframes.localTime(clip, t);
        var l = bag[prop] || (bag[prop] = []);
        var idx = CS.keyframes.indexAt(l, lt);
        if (idx >= 0) {
            l[idx].v = v;
        } else {
            l.push({ t: lt, v: v, ease: "linear" });
            l.sort(function (a, b) { return a.t - b.t; });
        }
    },

    //Remove the keyframe at the playhead; drops the track if it empties
    removeAt: function (clip, prop, t) {
        var l = CS.keyframes.list(clip, prop);
        if (!l) { return; }
        var idx = CS.keyframes.indexAt(l, CS.keyframes.localTime(clip, t));
        if (idx < 0) { return; }
        //Keep the last value as the static fallback before dropping the track
        var lastVal = l[idx].v;
        l.splice(idx, 1);
        if (!l.length) {
            delete clip.props.kf[prop];
            clip.props[prop] = lastVal;
        }
    },

    //Shift every keyframe's local time by -delta seconds. Used when a
    //clip's start moves independently of its content (e.g. the right
    //half of a split), so keyframes stay anchored to the same frames.
    rebase: function (clip, delta) {
        var kf = clip.props && clip.props.kf;
        if (!kf || !delta) { return; }
        for (var prop in kf) {
            (kf[prop] || []).forEach(function (k) { k.t -= delta; });
        }
    },

    setEaseAt: function (clip, prop, t, mode) {
        var l = CS.keyframes.list(clip, prop);
        if (!l) { return; }
        var idx = CS.keyframes.indexAt(l, CS.keyframes.localTime(clip, t));
        if (idx >= 0) { l[idx].ease = mode; }
    },

    /* ---------- navigation ---------- */

    //Unique keyframe local times across a set of props, ascending
    keyTimes: function (clip, props) {
        var seen = {};
        props.forEach(function (prop) {
            (CS.keyframes.list(clip, prop) || []).forEach(function (k) {
                seen[k.t.toFixed(4)] = k.t;
            });
        });
        return Object.keys(seen).map(function (key) { return seen[key]; })
            .sort(function (a, b) { return a - b; });
    },

    //Seek the playhead to the next / previous keyframe of the given props
    gotoAdjacent: function (clip, props, dir) {
        var times = CS.keyframes.keyTimes(clip, props);
        if (!times.length) { return; }
        var lt = CS.keyframes.localTime(clip);
        var targetLocal = null;
        if (dir > 0) {
            for (var i = 0; i < times.length; i++) {
                if (times[i] > lt + CS.keyframes.EPS) { targetLocal = times[i]; break; }
            }
        } else {
            for (var j = times.length - 1; j >= 0; j--) {
                if (times[j] < lt - CS.keyframes.EPS) { targetLocal = times[j]; break; }
            }
        }
        if (targetLocal === null) { return; }
        CS.player.seek(clip.start + targetLocal);
    }
};
