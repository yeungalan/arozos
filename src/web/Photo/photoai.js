/*
    photoai.js  —  Photo AI integration for ArozOS Photo app

    Adds AI-powered tagging and face recognition to the Photo module.
    Requires the photoai subservice to be installed and running.

    Public API (attached to window.PhotoAI):
      PhotoAI.checkAvailable()           → Promise<bool>
      PhotoAI.analyzePhoto(vpath, img)   → Promise<{tags, faces}>
      PhotoAI.renderFaceOverlay(canvas, faces, natW, natH)
      PhotoAI.renderTagBar(container, tags, onTagClick)
      PhotoAI.renderPeoplePanel(container)
      PhotoAI.renderTagPanel(container)
      PhotoAI.syncPersonLabel(clusterID, name) → Promise
*/

(function (w) {
    "use strict";

    var PHOTOAI_BASE = "/photoai";
    var AGI_BASE = "/system/ajgi/interface";

    /* ------------------------------------------------------------------ *
     *  Availability check
     * ------------------------------------------------------------------ */

    var _available = null; // cached: null=unknown, true/false

    function checkAvailable() {
        if (_available !== null) {
            return Promise.resolve(_available);
        }
        return fetch(PHOTOAI_BASE + "/api/status", { method: "GET" })
            .then(function (r) { return r.json(); })
            .then(function (d) {
                _available = !!(d && d.ok);
                return _available;
            })
            .catch(function () {
                _available = false;
                return false;
            });
    }

    /* ------------------------------------------------------------------ *
     *  Image resize helpers
     * ------------------------------------------------------------------ */

    function resizeImageToDataURL(imgEl, maxDim) {
        var w = imgEl.naturalWidth  || imgEl.width;
        var h = imgEl.naturalHeight || imgEl.height;
        if (!w || !h) { return null; }

        var scale = Math.min(maxDim / w, maxDim / h, 1);
        var dw = Math.round(w * scale);
        var dh = Math.round(h * scale);

        var canvas = document.createElement("canvas");
        canvas.width  = dw;
        canvas.height = dh;
        var ctx = canvas.getContext("2d");
        ctx.drawImage(imgEl, 0, 0, dw, dh);
        return canvas.toDataURL("image/jpeg", 0.80);
    }

    /* ------------------------------------------------------------------ *
     *  Main analysis entry point
     * ------------------------------------------------------------------ */

    function analyzePhoto(vpath, imgEl, exifHints) {
        var dataURL = resizeImageToDataURL(imgEl, 800);
        if (!dataURL) {
            return Promise.reject(new Error("Could not read image pixels"));
        }

        var body = {
            imageData: dataURL,
            vpath: vpath,
            hints: exifHints || {}
        };

        return fetch(PHOTOAI_BASE + "/api/analyze", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body)
        })
        .then(function (r) { return r.json(); })
        .then(function (result) {
            if (result.error) { throw new Error(result.error); }

            var promises = [];

            // Persist tags.
            if (result.tags && result.tags.length) {
                promises.push(
                    callAGI("Photo/backend/saveTags.js", {
                        filepath: vpath,
                        tags: result.tags,
                        source: "ai"
                    })
                );
            }

            // Persist face detections.
            if (result.faces && result.faces.length) {
                promises.push(
                    callAGI("Photo/backend/saveFaces.js", {
                        filepath: vpath,
                        faces: result.faces
                    })
                );
            }

            return Promise.all(promises).then(function () {
                return result;
            });
        });
    }

    /* ------------------------------------------------------------------ *
     *  AGI call helper
     * ------------------------------------------------------------------ */

    function callAGI(scriptPath, payload) {
        var fd = new FormData();
        fd.append("script", scriptPath);
        fd.append("POST_data", JSON.stringify(payload || {}));
        return fetch(AGI_BASE, { method: "POST", body: fd })
            .then(function (r) { return r.json(); });
    }

    /* ------------------------------------------------------------------ *
     *  Face overlay renderer
     * ------------------------------------------------------------------ */

    function renderFaceOverlay(container, faces, natW, natH) {
        // Remove any existing overlay.
        var old = container.querySelector(".photoai-faces");
        if (old) { old.parentNode.removeChild(old); }

        if (!faces || !faces.length) { return; }

        var overlay = document.createElement("div");
        overlay.className = "photoai-faces";
        overlay.style.cssText =
            "position:absolute;top:0;left:0;width:100%;height:100%;pointer-events:none;z-index:10";

        var cW = container.offsetWidth  || natW;
        var cH = container.offsetHeight || natH;

        faces.forEach(function (f) {
            var box = document.createElement("div");
            var x = (f.bbox_x * cW) + "px";
            var y = (f.bbox_y * cH) + "px";
            var bw = (f.bbox_w * cW) + "px";
            var bh = (f.bbox_h * cH) + "px";

            box.style.cssText =
                "position:absolute;border:2px solid rgba(79,142,247,.85);" +
                "border-radius:4px;box-sizing:border-box;" +
                "left:" + x + ";top:" + y + ";width:" + bw + ";height:" + bh;

            var label = document.createElement("div");
            label.style.cssText =
                "position:absolute;bottom:-22px;left:0;background:rgba(79,142,247,.9);" +
                "color:#fff;font-size:11px;padding:1px 6px;border-radius:3px;white-space:nowrap";
            label.textContent = f.person_name || "Unknown";
            box.appendChild(label);
            overlay.appendChild(box);
        });

        container.appendChild(overlay);
    }

    /* ------------------------------------------------------------------ *
     *  Tag bar renderer
     * ------------------------------------------------------------------ */

    function renderTagBar(container, tags, onTagClick) {
        container.innerHTML = "";
        if (!tags || !tags.length) {
            container.innerHTML =
                '<span style="color:#999;font-size:12px">No tags — click Analyse to generate</span>';
            return;
        }

        tags.forEach(function (t) {
            var tagStr = t.tag || t;
            var source = t.source || "user";
            var chip = document.createElement("span");
            chip.textContent = tagStr;
            chip.className = "photoai-tag";
            chip.dataset.tag = tagStr;
            chip.style.cssText =
                "display:inline-block;padding:2px 10px;margin:2px 3px;border-radius:12px;font-size:12px;cursor:pointer;" +
                "background:" + (source === "ai" ? "#e8f0fe" : "#f0fdf4") + ";" +
                "color:" + (source === "ai" ? "#4F8EF7" : "#16a34a") + ";" +
                "border:1px solid " + (source === "ai" ? "#c7d9fd" : "#bbf7d0");

            chip.addEventListener("click", function () {
                if (typeof onTagClick === "function") { onTagClick(tagStr); }
            });
            chip.title = source === "ai" ? "AI tag — click to search" : "User tag — click to search";
            container.appendChild(chip);
        });
    }

    /* ------------------------------------------------------------------ *
     *  People panel renderer (for the sidebar / People view)
     * ------------------------------------------------------------------ */

    function renderPeoplePanel(container, onPersonClick) {
        container.innerHTML =
            '<p style="color:#999;font-size:12px;padding:8px">Loading people…</p>';

        callAGI("Photo/backend/listPeople.js", {})
            .then(function (data) {
                var people = (data && data.people) || [];
                if (!people.length) {
                    container.innerHTML =
                        '<p style="color:#999;font-size:12px;padding:8px">No named people yet. Analyse photos and label faces.</p>';
                    return;
                }
                container.innerHTML = "";
                people.forEach(function (p) {
                    var item = document.createElement("div");
                    item.style.cssText =
                        "display:flex;align-items:center;gap:8px;padding:6px 10px;cursor:pointer;border-radius:6px";
                    item.innerHTML =
                        '<span style="font-size:22px">👤</span>' +
                        '<span style="flex:1;font-size:13px">' + escHtml(p.name) + '</span>' +
                        '<span style="color:#999;font-size:11px">' + p.count + ' photos</span>';
                    item.addEventListener("mouseenter", function () {
                        item.style.background = "#f5f5f5";
                    });
                    item.addEventListener("mouseleave", function () {
                        item.style.background = "";
                    });
                    item.addEventListener("click", function () {
                        if (typeof onPersonClick === "function") {
                            onPersonClick(p.name);
                        }
                    });
                    container.appendChild(item);
                });
            })
            .catch(function () {
                container.innerHTML =
                    '<p style="color:#c00;font-size:12px;padding:8px">Could not load people list.</p>';
            });
    }

    /* ------------------------------------------------------------------ *
     *  Tag browser panel
     * ------------------------------------------------------------------ */

    function renderTagPanel(container, onTagClick) {
        container.innerHTML =
            '<p style="color:#999;font-size:12px;padding:8px">Loading tags…</p>';

        callAGI("Photo/backend/listTags.js", {})
            .then(function (data) {
                var tags = (data && data.tags) || [];
                if (!tags.length) {
                    container.innerHTML =
                        '<p style="color:#999;font-size:12px;padding:8px">No tags yet. Analyse some photos first.</p>';
                    return;
                }
                container.innerHTML = "";
                tags.forEach(function (t) {
                    var chip = document.createElement("span");
                    chip.textContent = t.tag + " (" + t.count + ")";
                    chip.style.cssText =
                        "display:inline-block;padding:3px 10px;margin:3px;border-radius:12px;" +
                        "font-size:12px;cursor:pointer;background:#e8f0fe;color:#4F8EF7;" +
                        "border:1px solid #c7d9fd";
                    chip.addEventListener("click", function () {
                        if (typeof onTagClick === "function") { onTagClick(t.tag); }
                    });
                    container.appendChild(chip);
                });
            })
            .catch(function () {
                container.innerHTML =
                    '<p style="color:#c00;font-size:12px;padding:8px">Could not load tags.</p>';
            });
    }

    /* ------------------------------------------------------------------ *
     *  Sync a cluster label back to the Photo index
     * ------------------------------------------------------------------ */

    function syncPersonLabel(clusterID, name) {
        return callAGI("Photo/backend/syncPersonLabel.js", {
            cluster_id:  clusterID,
            person_name: name
        });
    }

    /* ------------------------------------------------------------------ *
     *  Analyse panel (toolbar button → expandable panel)
     * ------------------------------------------------------------------ */

    function createAnalysePanel(vpath, imgEl, exifHints, onTagClick, onDone) {
        var panel = document.createElement("div");
        panel.className = "photoai-panel";
        panel.style.cssText =
            "background:#fff;border-top:1px solid #e5e7eb;padding:12px 16px;" +
            "font-family:system-ui,sans-serif;font-size:13px";

        panel.innerHTML =
            '<div style="display:flex;align-items:center;gap:8px;margin-bottom:8px">' +
            '  <span style="font-weight:600;color:#4F8EF7">✨ AI Analysis</span>' +
            '  <button id="photoai-btn-analyse" style="margin-left:auto;padding:4px 14px;' +
            '    background:#4F8EF7;color:#fff;border:none;border-radius:6px;cursor:pointer;font-size:12px">' +
            '    Analyse</button>' +
            '</div>' +
            '<div id="photoai-tagbar" style="min-height:24px"></div>' +
            '<div id="photoai-faceinfo" style="margin-top:6px;color:#666;font-size:12px"></div>';

        var tagBar   = panel.querySelector("#photoai-tagbar");
        var faceInfo = panel.querySelector("#photoai-faceinfo");
        var btn      = panel.querySelector("#photoai-btn-analyse");

        // Load existing tags immediately.
        callAGI("Photo/backend/getTags.js", { filepath: vpath })
            .then(function (d) {
                if (d && d.tags && d.tags.length) {
                    renderTagBar(tagBar, d.tags, onTagClick);
                }
            });

        callAGI("Photo/backend/getFaces.js", { filepath: vpath })
            .then(function (d) {
                if (d && d.faces && d.faces.length) {
                    var names = {};
                    d.faces.forEach(function (f) {
                        if (f.person_name) { names[f.person_name] = true; }
                    });
                    var nameList = Object.keys(names);
                    if (nameList.length) {
                        faceInfo.textContent = "People: " + nameList.join(", ");
                    } else {
                        faceInfo.textContent = d.faces.length + " face(s) detected";
                    }
                    if (typeof onDone === "function") { onDone(d.faces); }
                }
            });

        btn.addEventListener("click", function () {
            btn.disabled = true;
            btn.textContent = "Analysing…";
            tagBar.innerHTML = '<span style="color:#999">Running analysis…</span>';

            analyzePhoto(vpath, imgEl, exifHints)
                .then(function (result) {
                    btn.textContent = "Re-analyse";
                    btn.disabled = false;

                    renderTagBar(tagBar, (result.tags || []).map(function (t) {
                        return { tag: t, source: "ai" };
                    }), onTagClick);

                    if (result.faces && result.faces.length) {
                        var names = {};
                        result.faces.forEach(function (f) {
                            if (f.person_name) { names[f.person_name] = true; }
                        });
                        var nameList = Object.keys(names);
                        faceInfo.textContent = nameList.length
                            ? "People: " + nameList.join(", ")
                            : result.faces.length + " face(s) detected";
                    } else {
                        faceInfo.textContent = "No faces detected";
                    }

                    if (typeof onDone === "function") { onDone(result.faces || []); }
                })
                .catch(function (err) {
                    btn.textContent = "Retry";
                    btn.disabled = false;
                    tagBar.innerHTML =
                        '<span style="color:#c00">Analysis failed: ' + escHtml(err.message) + '</span>';
                });
        });

        return panel;
    }

    /* ------------------------------------------------------------------ *
     *  Utility
     * ------------------------------------------------------------------ */

    function escHtml(s) {
        return ("" + (s || ""))
            .replace(/&/g, "&amp;")
            .replace(/</g, "&lt;")
            .replace(/>/g, "&gt;")
            .replace(/"/g, "&quot;");
    }

    /* ------------------------------------------------------------------ *
     *  Public API
     * ------------------------------------------------------------------ */

    w.PhotoAI = {
        checkAvailable:      checkAvailable,
        analyzePhoto:        analyzePhoto,
        renderFaceOverlay:   renderFaceOverlay,
        renderTagBar:        renderTagBar,
        renderPeoplePanel:   renderPeoplePanel,
        renderTagPanel:      renderTagPanel,
        syncPersonLabel:     syncPersonLabel,
        createAnalysePanel:  createAnalysePanel
    };

}(window));
