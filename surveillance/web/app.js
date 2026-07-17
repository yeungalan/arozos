/* Surveillance camera-management front-end.
 * Pure vanilla JS, no external dependencies. All API paths are relative to this
 * page (served at /surveillance/) so they proxy back to the subservice binary.
 */
(function () {
  "use strict";

  var state = {
    cameras: [],
    groups: [],
    tags: [],
    filter: { text: "", status: "", group: "", tag: "" }
  };

  var cameraIcon =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">' +
    '<path d="M4 8h11l2 3 3-1v7l-3-1-2 3H4z"/><circle cx="9" cy="12" r="2.2"/></svg>';

  // ---- API helpers -------------------------------------------------------
  function api(method, path, body) {
    var opts = { method: method, headers: {} };
    if (body !== undefined) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    return fetch("api/" + path, opts).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (data) {
        if (!r.ok) { throw new Error(data.error || ("HTTP " + r.status)); }
        return data;
      });
    });
  }

  function el(id) { return document.getElementById(id); }

  function toast(msg, kind) {
    var t = el("toast");
    t.textContent = msg;
    t.className = "toast " + (kind || "");
    t.hidden = false;
    clearTimeout(t._timer);
    t._timer = setTimeout(function () { t.hidden = true; }, 3000);
  }

  // ---- Data loading ------------------------------------------------------
  function refresh() {
    return Promise.all([
      api("GET", "cameras"),
      api("GET", "groups"),
      api("GET", "tags")
    ]).then(function (res) {
      state.cameras = res[0] || [];
      state.groups = res[1] || [];
      state.tags = res[2] || [];
      render();
    }).catch(function (e) { toast(e.message, "bad"); });
  }

  function filteredCameras() {
    var f = state.filter;
    var text = f.text.toLowerCase();
    return state.cameras.filter(function (c) {
      if (f.status && c.status !== f.status) return false;
      if (f.group && c.groupId !== f.group) return false;
      if (f.tag && (c.tags || []).map(function (t) { return t.toLowerCase(); }).indexOf(f.tag.toLowerCase()) === -1) return false;
      if (text) {
        var hay = (c.name + " " + c.description + " " + c.manufacturer).toLowerCase();
        if (hay.indexOf(text) === -1) return false;
      }
      return true;
    });
  }

  function groupName(id) {
    for (var i = 0; i < state.groups.length; i++) {
      if (state.groups[i].id === id) return state.groups[i].name;
    }
    return "";
  }

  // ---- Rendering ---------------------------------------------------------
  function render() {
    renderStats();
    renderGroups();
    renderTags();
    renderCameras();
  }

  function renderStats() {
    var online = 0, offline = 0, recording = 0;
    state.cameras.forEach(function (c) {
      if (c.status === "online") online++;
      if (c.status === "offline") offline++;
      if (c.recording && c.recording.mode && c.recording.mode !== "off") recording++;
    });
    el("stat-total").textContent = state.cameras.length;
    el("stat-online").textContent = online;
    el("stat-offline").textContent = offline;
    el("stat-recording").textContent = recording;
  }

  function renderGroups() {
    var list = el("group-list");
    list.innerHTML = "";
    var counts = {};
    state.cameras.forEach(function (c) { if (c.groupId) counts[c.groupId] = (counts[c.groupId] || 0) + 1; });

    var allLi = document.createElement("li");
    allLi.className = state.filter.group === "" ? "active" : "";
    allLi.innerHTML = '<span>All cameras</span><span class="count">' + state.cameras.length + "</span>";
    allLi.onclick = function () { state.filter.group = ""; render(); };
    list.appendChild(allLi);

    state.groups.forEach(function (g) {
      var li = document.createElement("li");
      li.className = state.filter.group === g.id ? "active" : "";
      var kind = g.kind ? '<span class="group-kind">' + g.kind + "</span>" : "";
      li.innerHTML = '<span>' + escapeHtml(g.name) + kind + '</span>' +
        '<span class="count">' + (counts[g.id] || 0) + ' <span class="del" title="Delete group">&times;</span></span>';
      li.querySelector("span:first-child").onclick = function () { state.filter.group = g.id; render(); };
      li.querySelector(".del").onclick = function (ev) {
        ev.stopPropagation();
        if (!confirm('Delete group "' + g.name + '"? Cameras stay but lose this group.')) return;
        api("DELETE", "groups/" + g.id).then(function () {
          if (state.filter.group === g.id) state.filter.group = "";
          toast("Group deleted", "ok");
          refresh();
        }).catch(function (e) { toast(e.message, "bad"); });
      };
      list.appendChild(li);
    });
  }

  function renderTags() {
    var cloud = el("tag-cloud");
    cloud.innerHTML = "";
    if (!state.tags.length) { cloud.innerHTML = '<span class="cam-meta">No tags yet</span>'; return; }
    state.tags.forEach(function (t) {
      var span = document.createElement("span");
      span.className = "tag" + (state.filter.tag.toLowerCase() === t.toLowerCase() ? " active" : "");
      span.textContent = t;
      span.onclick = function () {
        state.filter.tag = state.filter.tag.toLowerCase() === t.toLowerCase() ? "" : t;
        render();
      };
      cloud.appendChild(span);
    });
  }

  function renderCameras() {
    var grid = el("camera-grid");
    grid.innerHTML = "";
    var cams = filteredCameras();
    el("empty-state").hidden = cams.length > 0;

    cams.forEach(function (c) {
      var card = document.createElement("div");
      card.className = "camera-card";
      var badges = [];
      if (c.codec) badges.push(c.codec.toUpperCase());
      if (c.resolution) badges.push(c.resolution);
      if (c.fps) badges.push(c.fps + " fps");
      if (c.transport) badges.push(c.transport.toUpperCase());
      if (c.recording && c.recording.mode && c.recording.mode !== "off") badges.push("REC: " + c.recording.mode);
      var gname = groupName(c.groupId);

      card.innerHTML =
        '<div class="cam-preview">' + cameraIcon +
          '<span class="cam-status ' + c.status + '">' + c.status + '</span></div>' +
        '<div class="cam-body">' +
          '<div class="cam-name">' + escapeHtml(c.name) + '</div>' +
          '<div class="cam-meta">' + escapeHtml(c.rtspUrl || "") +
            (gname ? ' &middot; ' + escapeHtml(gname) : "") + '</div>' +
          '<div class="cam-badges">' + badges.map(function (b) { return '<span class="badge">' + escapeHtml(b) + '</span>'; }).join("") + '</div>' +
          '<div class="cam-tags">' + (c.tags || []).map(function (t) { return '<span class="tag">' + escapeHtml(t) + '</span>'; }).join("") + '</div>' +
        '</div>' +
        '<div class="cam-actions"></div>';

      var actions = card.querySelector(".cam-actions");
      actions.appendChild(mkBtn("Test", "", function () { testStored(c.id); }));
      actions.appendChild(mkBtn(c.enabled ? "Disable" : "Enable", "", function () { toggleEnabled(c); }));
      var spacer = document.createElement("div"); spacer.className = "spacer"; actions.appendChild(spacer);
      actions.appendChild(mkBtn("Edit", "", function () { openCameraModal(c); }));
      actions.appendChild(mkBtn("Delete", "danger", function () { deleteCamera(c); }));
      grid.appendChild(card);
    });
  }

  function mkBtn(label, cls, onclick) {
    var b = document.createElement("button");
    b.className = "btn " + cls;
    b.textContent = label;
    b.onclick = onclick;
    return b;
  }

  function escapeHtml(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (ch) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[ch];
    });
  }

  // ---- Camera actions ----------------------------------------------------
  function toggleEnabled(c) {
    api("POST", "cameras/" + c.id + "/" + (c.enabled ? "disable" : "enable")).then(function () {
      refresh();
    }).catch(function (e) { toast(e.message, "bad"); });
  }

  function deleteCamera(c) {
    if (!confirm('Delete camera "' + c.name + '"?')) return;
    api("DELETE", "cameras/" + c.id).then(function () {
      toast("Camera deleted", "ok");
      refresh();
    }).catch(function (e) { toast(e.message, "bad"); });
  }

  function testStored(id) {
    toast("Testing connection...", "");
    api("POST", "cameras/" + id + "/test").then(function (res) {
      if (res.reachable) toast("Camera reachable" + (res.codec ? " (" + res.codec + ")" : ""), "ok");
      else toast(res.detail || "Camera unreachable", "bad");
      refresh();
    }).catch(function (e) { toast(e.message, "bad"); });
  }

  // ---- Modal -------------------------------------------------------------
  function fillGroupSelect() {
    var sel = el("f-group");
    sel.innerHTML = '<option value="">No group</option>';
    state.groups.forEach(function (g) {
      var o = document.createElement("option");
      o.value = g.id; o.textContent = g.name;
      sel.appendChild(o);
    });
  }

  function openCameraModal(c) {
    fillGroupSelect();
    el("test-result").hidden = true;
    el("modal-title").textContent = c ? "Edit camera" : "Add camera";
    el("f-id").value = c ? c.id : "";
    el("f-name").value = c ? c.name : "";
    el("f-description").value = c ? c.description : "";
    el("f-rtsp").value = c ? c.rtspUrl : "";
    el("f-username").value = c ? c.username : "";
    el("f-password").value = "";
    el("f-password").placeholder = c && c.hasPassword ? "leave blank to keep unchanged" : "";
    el("f-transport").value = c ? (c.transport || "auto") : "auto";
    el("f-codec").value = c ? (c.codec || "") : "";
    el("f-streamtype").value = c ? (c.streamType || "main") : "main";
    el("f-resolution").value = c ? c.resolution : "";
    el("f-fps").value = c ? (c.fps || 0) : 0;
    el("f-manufacturer").value = c ? c.manufacturer : "";
    el("f-model").value = c ? c.model : "";
    el("f-tags").value = c && c.tags ? c.tags.join(", ") : "";
    var rec = (c && c.recording) || {};
    el("f-rec-mode").value = rec.mode || "off";
    el("f-rec-format").value = rec.format || "";
    el("f-rec-retention").value = rec.retentionDays || 0;
    el("f-rec-maxstorage").value = rec.maxStorageMb || 0;
    el("f-enabled").checked = c ? !!c.enabled : true;
    el("camera-modal").hidden = false;
  }

  function closeCameraModal() { el("camera-modal").hidden = true; }

  function readForm() {
    var tags = el("f-tags").value.split(",").map(function (t) { return t.trim(); }).filter(Boolean);
    return {
      name: el("f-name").value,
      description: el("f-description").value,
      rtspUrl: el("f-rtsp").value,
      username: el("f-username").value,
      password: el("f-password").value,
      transport: el("f-transport").value,
      codec: el("f-codec").value,
      streamType: el("f-streamtype").value,
      resolution: el("f-resolution").value,
      fps: parseInt(el("f-fps").value, 10) || 0,
      manufacturer: el("f-manufacturer").value,
      model: el("f-model").value,
      tags: tags,
      recording: {
        mode: el("f-rec-mode").value,
        format: el("f-rec-format").value,
        retentionDays: parseInt(el("f-rec-retention").value, 10) || 0,
        maxStorageMb: parseInt(el("f-rec-maxstorage").value, 10) || 0
      },
      enabled: el("f-enabled").checked
    };
  }

  function saveCamera(ev) {
    ev.preventDefault();
    var id = el("f-id").value;
    var body = readForm();
    var p = id ? api("PUT", "cameras/" + id, body) : api("POST", "cameras", body);
    p.then(function () {
      toast(id ? "Camera updated" : "Camera added", "ok");
      closeCameraModal();
      refresh();
    }).catch(function (e) { toast(e.message, "bad"); });
  }

  function testForm() {
    var box = el("test-result");
    box.hidden = false;
    box.className = "test-result busy";
    box.textContent = "Testing connection...";
    api("POST", "validate", {
      rtspUrl: el("f-rtsp").value,
      username: el("f-username").value,
      password: el("f-password").value
    }).then(function (res) {
      var ok = res.ok || res.reachable;
      box.className = "test-result " + (res.ok ? "ok" : (res.reachable ? "ok" : "bad"));
      var parts = [];
      parts.push(res.reachable ? "Reachable" : "Unreachable");
      if (res.authOk) parts.push("auth OK");
      if (res.codec) parts.push("codec " + res.codec);
      if (res.resolution) parts.push(res.resolution);
      if (res.detail) parts.push(res.detail);
      box.textContent = parts.join(" · ");
    }).catch(function (e) {
      box.className = "test-result bad";
      box.textContent = e.message;
    });
  }

  // ---- Group modal -------------------------------------------------------
  function openGroupModal() { el("g-name").value = ""; el("g-kind").value = ""; el("group-modal").hidden = false; }
  function closeGroupModal() { el("group-modal").hidden = true; }
  function saveGroup(ev) {
    ev.preventDefault();
    api("POST", "groups", { name: el("g-name").value, kind: el("g-kind").value }).then(function () {
      toast("Group created", "ok");
      closeGroupModal();
      refresh();
    }).catch(function (e) { toast(e.message, "bad"); });
  }

  // ---- Wire up -----------------------------------------------------------
  function init() {
    el("add-camera-btn").onclick = function () { openCameraModal(null); };
    el("modal-close").onclick = closeCameraModal;
    el("cancel-btn").onclick = closeCameraModal;
    el("camera-form").onsubmit = saveCamera;
    el("test-btn").onclick = testForm;

    el("add-group-btn").onclick = openGroupModal;
    el("group-close").onclick = closeGroupModal;
    el("group-cancel").onclick = closeGroupModal;
    el("group-form").onsubmit = saveGroup;

    el("search-input").oninput = function () { state.filter.text = this.value; renderCameras(); };
    el("filter-status").onchange = function () { state.filter.status = this.value; renderCameras(); };

    document.querySelectorAll(".modal-backdrop").forEach(function (m) {
      m.addEventListener("click", function (e) { if (e.target === m) m.hidden = true; });
    });

    refresh();
  }

  document.addEventListener("DOMContentLoaded", init);
})();
