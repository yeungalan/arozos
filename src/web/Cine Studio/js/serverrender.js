/*
    Cine Studio - server side (backend) rendering

    Alternative export pipeline that renders the timeline on the
    ArozOS host with ffmpeg instead of recording the preview canvas
    in real time. The project is serialized into a render spec
    (media sources + video/audio clip lists) understood by
    ffmpeg.renderTimeline; title, color and Pixel Studio clips are
    pre-rendered to PNG stills in the browser and uploaded next to
    the export so the server needs no font or project-format support.

    Renders run faster than real time on most hosts and keep going
    even when the tab is hidden. Progress is polled from
    backend/renderprogress.js; the task file written by
    backend/render.js is the source of truth for completion, so a
    dropped connection does not lose the job.
*/
"use strict";

window.CS = window.CS || {};

CS.serverrender = {
    active: false,
    taskId: null,
    tempDir: "",
    tempFiles: [],
    pollTimer: 0,
    ui: null,
    finishedFired: false,
    requestFailed: false,
    notFoundPolls: 0,

    /* ---------- feasibility check ---------- */

    //Media the timeline needs but the server cannot read
    validate: function () {
        var reasons = [];
        var warnings = [];
        var seen = {};
        CS.project.clips.forEach(function (clip) {
            if (clip.kind === "title" || clip.kind === "color") { return; }
            var media = CS.getMedia(clip.mediaId);
            if (!media || seen[clip.mediaId]) { return; }
            seen[clip.mediaId] = true;
            if (media.offline) {
                reasons.push(media.name + " is offline");
            } else if (media.srcKind === "pxs" || media.srcKind === "asproj") {
                if (!media.compositeUrl) {
                    reasons.push(media.name + " is still being imported");
                }
            } else if (!media.vpath) {
                reasons.push(media.name + " only exists in this browser");
            }
        });
        CS.project.clips.forEach(function (clip) {
            if (clip.props && clip.props.blend && clip.props.blend !== "normal") {
                if (warnings.indexOf("blend") < 0) { warnings.push("blend"); }
            }
        });
        return {
            ok: reasons.length === 0,
            reason: reasons.length ? "Cannot render on server: " + reasons[0] : "",
            warnings: warnings
        };
    },

    /* ---------- spec building ---------- */

    //Convert the fade effects into fade fields; pass the rest through
    effectsOf: function (clip) {
        var out = { fadeIn: 0, fadeOut: 0, effects: [] };
        ((clip.props && clip.props.effects) || []).forEach(function (e) {
            if (e.type === "fadein") { out.fadeIn = e.amount || 1; }
            else if (e.type === "fadeout") { out.fadeOut = e.amount || 1; }
            else { out.effects.push({ type: e.type, amount: e.amount === undefined ? 0 : e.amount }); }
        });
        return out;
    },

    //stillVpaths maps "clip:<id>" / "media:<id>" to an uploaded temp file
    buildSpec: function (stillVpaths) {
        var spec = {
            width: CS.project.settings.width,
            height: CS.project.settings.height,
            fps: CS.project.settings.fps,
            duration: CS.timelineDuration(),
            quality: 0,
            sources: [],
            video: [],
            audio: []
        };
        var sources = {};
        function sourceFor(id, vpath, type) {
            if (!sources[id]) {
                sources[id] = true;
                spec.sources.push({ id: id, vpath: vpath, type: type });
            }
            return id;
        }

        //Video: paint order is bottom track first, clips by start time
        CS.videoTracksInRenderOrder().forEach(function (track) {
            if (!track.visible) { return; }
            var entryByClipId = {};
            CS.clipsOnTrack(track.id).forEach(function (clip) {
                var srcId = null;
                if (clip.kind === "title" || clip.kind === "color") {
                    var still = stillVpaths["clip:" + clip.id];
                    if (!still) { return; }
                    srcId = sourceFor("st_" + clip.id, still, "image");
                } else {
                    var media = CS.getMedia(clip.mediaId);
                    if (!media || media.offline || media.type === "audio") { return; }
                    if (media.srcKind === "pxs") {
                        var mstill = stillVpaths["media:" + media.id];
                        if (!mstill) { return; }
                        srcId = sourceFor("sm_" + media.id, mstill, "image");
                    } else {
                        srcId = sourceFor("m_" + media.id, media.vpath, media.type === "image" ? "image" : "video");
                    }
                }

                var p = clip.props || {};
                var fx = CS.serverrender.effectsOf(clip);
                var entry = {
                    source: srcId,
                    start: clip.start,
                    in: clip.in,
                    out: clip.out,
                    speed: CS.clipSpeed(clip),
                    x: p.x || 0,
                    y: p.y || 0,
                    scale: p.scale === undefined ? 100 : p.scale,
                    rotation: p.rotation || 0,
                    opacity: p.opacity === undefined ? 100 : p.opacity,
                    crop: p.crop || "fit",
                    flipH: !!p.flipH,
                    flipV: !!p.flipV,
                    exposure: p.exposure || 0,
                    contrast: p.contrast || 0,
                    saturation: p.saturation === undefined ? 1 : p.saturation,
                    preset: p.preset || "default",
                    effects: fx.effects,
                    fadeIn: fx.fadeIn,
                    fadeOut: fx.fadeOut,
                    transFadeIn: 0,
                    transFadeInOffset: 0,
                    extend: 0,
                    extendFadeOut: 0
                };

                //Transition into this clip: freeze the previous clip's last
                //frame and ramp this clip's alpha (wipe falls back to dissolve)
                var tr = p.transition;
                if (tr && tr.type !== "none") {
                    var d = Math.min(tr.duration || 1, CS.clipDuration(clip));
                    var prev = CS.transitions.prevOf(clip);
                    var prevEntry = prev ? entryByClipId[prev.id] : null;
                    if (tr.type === "fade") {
                        entry.transFadeIn = d / 2;
                        entry.transFadeInOffset = d / 2;
                        if (prevEntry) {
                            prevEntry.extend += d / 2;
                            prevEntry.extendFadeOut = d / 2;
                        }
                    } else {
                        entry.transFadeIn = d;
                        if (prevEntry) { prevEntry.extend += d; }
                    }
                }

                entryByClipId[clip.id] = entry;
                spec.video.push(entry);
            });
        });

        //Audio: everything audible, matching the preview player's rules
        var anySolo = CS.project.tracks.some(function (t) { return t.kind === "audio" && t.solo; });
        CS.project.clips.forEach(function (clip) {
            if (clip.kind === "title" || clip.kind === "color") { return; }
            var media = CS.getMedia(clip.mediaId);
            if (!media || media.offline || media.type === "image" || media.srcKind === "pxs") { return; }
            var track = CS.getTrack(clip.trackId);
            if (!track || track.muted || !track.visible) { return; }
            if (anySolo && track.kind === "audio" && !track.solo) { return; }
            var p = clip.props || {};
            var vol = p.volume === undefined ? 100 : p.volume;
            if (vol <= 0) { return; }

            var srcId;
            if (media.srcKind === "asproj") {
                var comp = stillVpaths["media:" + media.id];
                if (!comp) { return; }
                srcId = sourceFor("sm_" + media.id, comp, "audio");
            } else {
                srcId = sourceFor("m_" + media.id, media.vpath, media.type === "video" ? "video" : "audio");
            }
            var fx = CS.serverrender.effectsOf(clip);
            spec.audio.push({
                source: srcId,
                start: clip.start,
                in: clip.in,
                out: clip.out,
                speed: CS.clipSpeed(clip),
                volume: vol,
                fadeIn: fx.fadeIn,
                fadeOut: fx.fadeOut
            });
        });

        return spec;
    },

    /* ---------- browser-rendered stills ---------- */

    //Jobs for the frames/sounds the server cannot decode itself:
    //title and color clips plus Pixel/Audio Studio project imports
    collectUploadJobs: function () {
        var jobs = [];
        var W = CS.project.settings.width;
        var H = CS.project.settings.height;
        var mediaDone = {};

        CS.project.clips.forEach(function (clip) {
            var track = CS.getTrack(clip.trackId);
            if (!track || track.kind !== "video" || !track.visible) { return; }
            if (clip.kind === "title" || clip.kind === "color") {
                jobs.push({
                    key: "clip:" + clip.id,
                    filename: "clip_" + clip.id + ".png",
                    make: function (done, fail) {
                        CS.titles.renderSource(clip, W, H).toBlob(function (blob) {
                            if (!blob) { fail("could not render " + (clip.kind === "title" ? "title" : "color") + " clip"); return; }
                            done(blob);
                        }, "image/png");
                    }
                });
            }
        });

        CS.project.clips.forEach(function (clip) {
            if (clip.kind === "title" || clip.kind === "color") { return; }
            var media = CS.getMedia(clip.mediaId);
            if (!media || mediaDone[clip.mediaId]) { return; }
            if (media.srcKind !== "pxs" && media.srcKind !== "asproj") { return; }
            mediaDone[clip.mediaId] = true;
            var ext = media.srcKind === "pxs" ? ".png" : ".wav";
            jobs.push({
                key: "media:" + media.id,
                filename: "media_" + media.id + ext,
                make: function (done, fail) {
                    fetch(media.compositeUrl)
                        .then(function (r) { return r.blob(); })
                        .then(function (blob) { done(blob); })
                        .catch(function () { fail("could not read " + media.name); });
                }
            });
        });

        return jobs;
    },

    uploadJobs: function (jobs, onDone, onFail) {
        var idx = 0;
        var stillVpaths = {};
        var next = function () {
            if (idx >= jobs.length) { onDone(stillVpaths); return; }
            var job = jobs[idx];
            idx++;
            CS.serverrender.setLabel("Preparing media (" + idx + "/" + jobs.length + ")...");
            job.make(function (blob) {
                var file = new File([blob], job.filename, { type: blob.type || "application/octet-stream" });
                ao_module_uploadFile(file, CS.serverrender.tempDir, function () {
                    var vpath = CS.serverrender.tempDir + "/" + job.filename;
                    CS.serverrender.tempFiles.push(vpath);
                    stillVpaths[job.key] = vpath;
                    next();
                }, undefined, function () {
                    onFail("could not upload " + job.filename);
                });
            }, onFail);
        };
        next();
    },

    /* ---------- orchestration ---------- */

    start: function (settings) {
        if (CS.serverrender.active) {
            CS.toast("A server render is already running", true);
            return;
        }
        var check = CS.serverrender.validate();
        if (!check.ok) {
            CS.toast(check.reason, true);
            return;
        }
        if (check.warnings.indexOf("blend") >= 0) {
            CS.toast("Blend modes are ignored by server rendering");
        }

        CS.serverrender.active = true;
        CS.serverrender.finishedFired = false;
        CS.serverrender.requestFailed = false;
        CS.serverrender.notFoundPolls = 0;
        CS.serverrender.taskId = CS.uid();
        CS.serverrender.tempFiles = [];
        CS.serverrender.tempDir = CS.APP_ROOT + "/Exports/.render_" + CS.serverrender.taskId;
        CS.serverrender.settings = settings;

        CS.serverrender.showProgress();

        var jobs = CS.serverrender.collectUploadJobs();
        var launch = function (stillVpaths) {
            var spec = CS.serverrender.buildSpec(stillVpaths);
            if (!spec.video.length && !spec.audio.length) {
                CS.serverrender.fail("Nothing on the timeline can be rendered");
                return;
            }
            CS.serverrender.setLabel("Rendering on server...");
            CS.serverrender.launchRender(spec);
        };

        if (!jobs.length) {
            launch({});
            return;
        }
        //Stills go into a per-task temp folder next to the export
        ao_module_agirun("Cine Studio/backend/ffmpegtools.js", {
            action: "preparedir",
            target: CS.serverrender.tempDir
        }, function (resp) {
            var data;
            try { data = typeof resp === "string" ? JSON.parse(resp) : resp; } catch (e) { data = {}; }
            if (!data || !data.ok) {
                CS.serverrender.fail("Could not create the temporary render folder");
                return;
            }
            CS.serverrender.uploadJobs(jobs, launch, CS.serverrender.fail);
        }, function () {
            CS.serverrender.fail("Could not create the temporary render folder");
        });
    },

    launchRender: function (spec) {
        var s = CS.serverrender.settings;
        var output = s.destDir + "/" + s.base + "." + s.format;
        ao_module_agirun("Cine Studio/backend/render.js", {
            spec: JSON.stringify(spec),
            taskId: CS.serverrender.taskId,
            output: output
        }, function (resp) {
            var data;
            try { data = typeof resp === "string" ? JSON.parse(resp) : resp; } catch (e) { data = null; }
            if (data && data.success) {
                CS.serverrender.succeed();
            } else {
                CS.serverrender.fail("Render failed: " + ((data && data.error) || "unknown error"));
            }
        }, function () {
            //The connection may drop on long renders; the poller keeps
            //watching the task file and decides the outcome
            CS.serverrender.requestFailed = true;
        }, 0);
        CS.serverrender.pollTimer = setInterval(CS.serverrender.poll, 1000);
    },

    poll: function () {
        ao_module_agirun("Cine Studio/backend/renderprogress.js", {
            id: CS.serverrender.taskId
        }, function (resp) {
            var data;
            try { data = typeof resp === "string" ? JSON.parse(resp) : resp; } catch (e) { data = null; }
            if (!data) { return; }
            if (data.error === "not_found") {
                CS.serverrender.notFoundPolls++;
                //The render request died before it could register the task
                if (CS.serverrender.requestFailed && CS.serverrender.notFoundPolls > 5) {
                    CS.serverrender.fail("The render request never reached the server");
                }
                return;
            }
            if (data.status === "completed") {
                CS.serverrender.succeed();
            } else if (data.status === "failed") {
                CS.serverrender.fail("Render failed: " + (data.error || "unknown error"));
            } else if (data.progress && typeof data.progress.percentage === "number") {
                CS.serverrender.setProgress(data.progress.percentage);
            }
        }, function () { /* transient poll error: keep trying */ }, 10000);
    },

    succeed: function () {
        if (CS.serverrender.finishedFired) { return; }
        CS.serverrender.finishedFired = true;
        CS.serverrender.stop();
        CS.serverrender.cleanupTemp();
        var s = CS.serverrender.settings;
        CS.closeModal();
        CS.exporter.finished(s.destDir, s.base + "." + s.format);
    },

    fail: function (message) {
        if (CS.serverrender.finishedFired) { return; }
        CS.serverrender.finishedFired = true;
        CS.serverrender.stop();
        CS.serverrender.cleanupTemp();
        CS.closeModal();
        CS.toast(message, true);
    },

    stop: function () {
        CS.serverrender.active = false;
        clearInterval(CS.serverrender.pollTimer);
        CS.serverrender.pollTimer = 0;
    },

    //Delete the uploaded stills, then the (now empty) temp folder
    cleanupTemp: function () {
        var files = CS.serverrender.tempFiles.slice();
        var dir = CS.serverrender.tempDir;
        CS.serverrender.tempFiles = [];
        if (!files.length && !dir) { return; }
        var remaining = files.length;
        var removeDir = function () {
            if (!dir) { return; }
            ao_module_agirun("Cine Studio/backend/ffmpegtools.js", {
                action: "cleanup",
                target: dir
            }, function () {}, function () {});
        };
        if (!remaining) { removeDir(); return; }
        files.forEach(function (vpath) {
            ao_module_agirun("Cine Studio/backend/ffmpegtools.js", {
                action: "cleanup",
                target: vpath
            }, function () {
                remaining--;
                if (remaining === 0) { removeDir(); }
            }, function () {
                remaining--;
                if (remaining === 0) { removeDir(); }
            });
        });
    },

    /* ---------- progress UI ---------- */

    showProgress: function () {
        var fill, label;
        CS.modal({
            title: "Rendering on Server...",
            build: function (body) {
                var bar = document.createElement("div");
                bar.className = "modal-progress";
                fill = document.createElement("div");
                fill.className = "fill";
                bar.appendChild(fill);
                body.appendChild(bar);
                label = document.createElement("div");
                label.className = "modal-note";
                label.textContent = "Preparing media...";
                body.appendChild(label);
                var hint = document.createElement("div");
                hint.className = "modal-note";
                hint.textContent = "The server renders with ffmpeg - you can keep editing or hide this window.";
                body.appendChild(hint);
            },
            buttons: [
                { label: "Hide" }
            ]
        });
        CS.serverrender.ui = { fill: fill, label: label };
    },

    setLabel: function (text) {
        if (CS.serverrender.ui && CS.serverrender.ui.label.isConnected) {
            CS.serverrender.ui.label.textContent = text;
        }
    },

    setProgress: function (pct) {
        if (CS.serverrender.ui && CS.serverrender.ui.fill.isConnected) {
            CS.serverrender.ui.fill.style.width = CS.clamp(pct, 0, 100).toFixed(1) + "%";
            CS.serverrender.ui.label.textContent = "Rendering on server... " + Math.round(pct) + "%";
        }
    }
};
