/*
    Keyframe animation tests: the CS.keyframes engine (interpolation,
    enable/disable, add/remove, split rebase), the compositor reading
    animated transform/opacity through it, and project persistence of
    the kf data. Uses an image clip so no MediaRecorder is needed.
*/
"use strict";

const { ok, fail, run } = require("../lib/harness");

run("KEYFRAMES", async (page) => {
    await page.evaluate(() => { try { localStorage.clear(); } catch (e) {} });

    /* ---- seed a solid green image and place it on V1 ---- */
    await page.evaluate(() => {
        const ic = document.createElement("canvas");
        ic.width = 320; ic.height = 180;
        const ictx = ic.getContext("2d");
        ictx.fillStyle = "#20c020"; ictx.fillRect(0, 0, 320, 180);
        window.__img = CS.media.register({ name: "Green.png", blobUrl: ic.toDataURL("image/png"), type: "image" });
    });
    await page.waitForFunction(() => window.__img.probed, { timeout: 30000 });

    // Read a pixel at the centre of the preview canvas.
    const centre = () => page.evaluate(() => {
        const cv = document.getElementById("preview-canvas");
        const d = cv.getContext("2d").getImageData(cv.width >> 1, cv.height >> 1, 1, 1).data;
        return [d[0], d[1], d[2]];
    });

    await page.evaluate(() => {
        // A 5s image clip filling the frame.
        const clip = CS.addClipToTimeline(window.__img, "V1", 0);
        clip.props.crop = "fill";
        CS.commit("seed");
        window.__clip = clip;
    });

    /* ---- 1. interpolation math ---- */
    const interp = await page.evaluate(() => {
        const clip = CS.getClip(window.__clip.id);
        CS.keyframes.enable(clip, "opacity", 0);
        CS.keyframes.setAt(clip, "opacity", 0, 100);
        CS.keyframes.setAt(clip, "opacity", 4, 0);
        return {
            start: CS.keyframes.valueAt(clip, "opacity", 0),
            mid:   CS.keyframes.valueAt(clip, "opacity", 2),
            end:   CS.keyframes.valueAt(clip, "opacity", 4),
            before: CS.keyframes.valueAt(clip, "opacity", -1), // holds first
            after:  CS.keyframes.valueAt(clip, "opacity", 9),  // holds last
            count: CS.keyframes.list(clip, "opacity").length
        };
    });
    if (interp.start !== 100 || interp.end !== 0) { fail("opacity endpoints wrong: " + JSON.stringify(interp)); }
    if (Math.abs(interp.mid - 50) > 0.5) { fail("linear midpoint should be ~50, got " + interp.mid); }
    if (interp.before !== 100 || interp.after !== 0) { fail("out-of-range keyframes should hold: " + JSON.stringify(interp)); }
    if (interp.count !== 2) { fail("expected 2 opacity keyframes, got " + interp.count); }
    ok("linear interpolation and clamping to endpoints");

    /* ---- 2. compositor honours opacity keyframes ---- */
    await page.evaluate(() => CS.player.seek(0));
    const full = await centre();
    await page.evaluate(() => CS.player.seek(2));
    const half = await centre();
    await page.evaluate(() => CS.player.seek(4));
    const none = await centre();
    // At t=0 the green shows; at t=4 opacity 0 -> black background.
    if (full[1] < 150) { fail("clip should be visible at t=0, got " + JSON.stringify(full)); }
    if (none[0] > 20 || none[1] > 20 || none[2] > 20) { fail("clip should be transparent at t=4, got " + JSON.stringify(none)); }
    // Midpoint is a partial blend over black: dimmer than full, brighter than none.
    if (!(half[1] < full[1] && half[1] > none[1])) { fail("midpoint should be a partial blend, got " + JSON.stringify(half)); }
    ok("compositor animates opacity between keyframes");

    /* ---- 3. easing changes the curve shape ---- */
    // The segment ending at t=4 ramps 100 -> 0. Ease-in starts slow, so at
    // the midpoint the value stays nearer the start (above the linear 50).
    const eased = await page.evaluate(() => {
        const clip = CS.getClip(window.__clip.id);
        CS.keyframes.setEaseAt(clip, "opacity", 4, "easein");
        return CS.keyframes.valueAt(clip, "opacity", 2);
    });
    if (!(eased > 51)) { fail("ease-in midpoint should sit above linear, got " + eased); }
    ok("ease-in reshapes the interpolation");

    /* ---- 4. position keyframes interpolate independently ---- */
    const moved = await page.evaluate(() => {
        const clip = CS.getClip(window.__clip.id);
        CS.keyframes.enable(clip, "x", 0);
        CS.keyframes.setAt(clip, "x", 0, 0);
        CS.keyframes.setAt(clip, "x", 4, 400);
        CS.commit("animate x");
        return {
            atStart: CS.keyframes.valueAt(clip, "x", 0),
            atMid:   CS.keyframes.valueAt(clip, "x", 2),
            atEnd:   CS.keyframes.valueAt(clip, "x", 4),
            // opacity keyframes from earlier are untouched by animating x
            opacityStillKeyed: CS.keyframes.has(clip, "opacity")
        };
    });
    if (moved.atStart !== 0 || moved.atEnd !== 400 || Math.abs(moved.atMid - 200) > 1) {
        fail("x should interpolate 0->400 (mid 200), got " + JSON.stringify(moved));
    }
    if (!moved.opacityStillKeyed) { fail("animating x must not disturb opacity keyframes"); }
    ok("multiple properties animate independently");

    /* ---- 5. remove keyframe falls back to a static value ---- */
    const removed = await page.evaluate(() => {
        const clip = CS.getClip(window.__clip.id);
        CS.keyframes.enable(clip, "rotation", 0);
        CS.keyframes.setAt(clip, "rotation", 0, 10);
        CS.keyframes.setAt(clip, "rotation", 2, 40);
        CS.keyframes.removeAt(clip, "rotation", 0);
        const one = CS.keyframes.has(clip, "rotation");
        CS.keyframes.removeAt(clip, "rotation", 2);
        return { stillHas: one, nowHas: CS.keyframes.has(clip, "rotation"), stat: clip.props.rotation };
    });
    if (!removed.stillHas) { fail("removing one of two keyframes should keep the track"); }
    if (removed.nowHas) { fail("removing the last keyframe should drop the track"); }
    if (removed.stat !== 40) { fail("last value should bake into the static prop, got " + removed.stat); }
    ok("removing keyframes bakes the final value");

    /* ---- 6. split rebases the right half's keyframes ---- */
    const split = await page.evaluate(() => {
        const clip = CS.getClip(window.__clip.id);
        // Fresh, single-property animation from a known state.
        clip.props.kf = {};
        CS.keyframes.enable(clip, "opacity", 0);
        CS.keyframes.setAt(clip, "opacity", 0, 0);
        CS.keyframes.setAt(clip, "opacity", 4, 100);
        const before = CS.keyframes.valueAt(clip, "opacity", 3); // 75
        CS.splitClip(clip, 2); // split at timeline t=2
        const right = CS.selectedClip(); // split selects the right half
        const after = CS.keyframes.valueAt(right, "opacity", 3); // same frame => still 75
        return { before: before, after: after, rightStart: right.start };
    });
    if (split.rightStart !== 2) { fail("right half should start at the split point"); }
    if (Math.abs(split.before - split.after) > 0.5) {
        fail("keyframes should stay locked to content across a split: " + JSON.stringify(split));
    }
    ok("split keeps keyframes anchored to the same frames");

    /* ---- 7. persistence round-trip ---- */
    const persist = await page.evaluate(() => {
        const json = CS.fileio.serializeProject();
        const parsed = JSON.parse(json);
        const clip = parsed.clips.find((c) => c.props && c.props.kf && c.props.kf.opacity);
        return { has: !!clip, keys: clip ? clip.props.kf.opacity.length : 0 };
    });
    if (!persist.has || persist.keys < 2) { fail("keyframes should serialize with the project: " + JSON.stringify(persist)); }
    ok("keyframes persist through save/load");
});
