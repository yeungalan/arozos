/*
    NotionEditor - AI assistant panel

    A modal that talks to backend/ai.agi (which uses the aimodel library). It can
    generate Markdown from a free-form instruction or a quick action, then insert
    the result as blocks below the target block or replace it. AI features are
    hidden gracefully when no model is configured by the admin.
*/
(function (global) {
    "use strict";

    var MD = global.NEMarkdown;

    var QUICK_ACTIONS = [
        { label: "Continue writing", icon: "pencil alternate", prompt: "Continue writing the document naturally from where it currently ends. Output only the new content." },
        { label: "Summarize", icon: "compress", prompt: "Summarize the document as a short list of key points." },
        { label: "Improve writing", icon: "magic", prompt: "Improve the writing of the document for clarity and flow while preserving its meaning." },
        { label: "Fix grammar", icon: "check", prompt: "Fix spelling and grammar mistakes in the document. Return the corrected text." },
        { label: "Make shorter", icon: "minus", prompt: "Make the document more concise." },
        { label: "Brainstorm", icon: "lightbulb outline", prompt: "Brainstorm a list of ideas related to the document topic." }
    ];

    function NEAI(editor, opts) {
        this.editor = editor;
        this.opts = opts || {};
        this.configured = false;
        this.models = [];
        this.defaultModel = "";
        this.targetId = null;
        this.resultText = "";
        this._build();
        this.refreshStatus();
    }

    NEAI.prototype.refreshStatus = function () {
        var self = this;
        this.opts.agiRun("backend/aistatus.agi", {}, function (data) {
            var d = self._parse(data);
            self.configured = !!(d && d.configured);
            self.models = (d && d.models) || [];
            self.defaultModel = (d && d.default) || "";
            self._renderModelOptions();
            if (self.opts.onStatus) self.opts.onStatus(self.configured);
        });
    };

    NEAI.prototype._parse = function (data) {
        if (typeof data === "object") return data;
        try { return JSON.parse(data); } catch (e) { return null; }
    };

    NEAI.prototype._build = function () {
        var self = this;
        var overlay = document.createElement("div");
        overlay.className = "ne-ai-overlay";
        overlay.style.display = "none";
        overlay.addEventListener("mousedown", function (e) { if (e.target === overlay) self.close(); });

        var modal = document.createElement("div");
        modal.className = "ne-ai-modal";

        modal.innerHTML =
            '<div class="ne-ai-head"><i class="magic icon"></i><span>Ask AI</span>' +
            '<button class="ne-ai-close" title="Close"><i class="times icon"></i></button></div>' +
            '<div class="ne-ai-unconfigured" style="display:none">' +
            'AI is not configured yet. An administrator can enable it under ' +
            'System Settings &rsaquo; AI Integration.</div>' +
            '<div class="ne-ai-body">' +
            '<textarea class="ne-ai-prompt" rows="3" placeholder="Tell AI what to write or change..."></textarea>' +
            '<div class="ne-ai-quick"></div>' +
            '<div class="ne-ai-modelrow" style="display:none">Model: <select class="ne-ai-model"></select></div>' +
            '<div class="ne-ai-result" style="display:none"></div>' +
            '<div class="ne-ai-error" style="display:none"></div>' +
            '</div>' +
            '<div class="ne-ai-foot">' +
            '<button class="ne-ai-generate"><i class="paper plane icon"></i> Generate</button>' +
            '<div class="ne-ai-result-actions" style="display:none">' +
            '<button class="ne-ai-insert"><i class="plus icon"></i> Insert below</button>' +
            '<button class="ne-ai-replace"><i class="exchange icon"></i> Replace</button>' +
            '</div></div>';

        overlay.appendChild(modal);
        document.body.appendChild(overlay);

        this.overlay = overlay;
        this.modal = modal;
        this.promptEl = modal.querySelector(".ne-ai-prompt");
        this.quickEl = modal.querySelector(".ne-ai-quick");
        this.modelRow = modal.querySelector(".ne-ai-modelrow");
        this.modelSel = modal.querySelector(".ne-ai-model");
        this.resultEl = modal.querySelector(".ne-ai-result");
        this.errorEl = modal.querySelector(".ne-ai-error");
        this.unconfiguredEl = modal.querySelector(".ne-ai-unconfigured");
        this.genBtn = modal.querySelector(".ne-ai-generate");
        this.resultActions = modal.querySelector(".ne-ai-result-actions");

        modal.querySelector(".ne-ai-close").addEventListener("click", function () { self.close(); });
        this.genBtn.addEventListener("click", function () { self._generate(self.promptEl.value); });
        modal.querySelector(".ne-ai-insert").addEventListener("click", function () { self._insert(false); });
        modal.querySelector(".ne-ai-replace").addEventListener("click", function () { self._insert(true); });

        QUICK_ACTIONS.forEach(function (qa) {
            var b = document.createElement("button");
            b.className = "ne-ai-chip";
            b.innerHTML = '<i class="' + qa.icon + ' icon"></i>' + qa.label;
            b.addEventListener("click", function () {
                self.promptEl.value = qa.label;
                self._generate(qa.prompt);
            });
            self.quickEl.appendChild(b);
        });
    };

    NEAI.prototype._renderModelOptions = function () {
        if (!this.models || this.models.length <= 1) { this.modelRow.style.display = "none"; return; }
        this.modelSel.innerHTML = "";
        var self = this;
        this.models.forEach(function (m) {
            var o = document.createElement("option");
            o.value = m; o.textContent = m;
            if (m === self.defaultModel) o.selected = true;
            self.modelSel.appendChild(o);
        });
        this.modelRow.style.display = "";
    };

    NEAI.prototype.open = function (blockId) {
        this.targetId = blockId;
        this.resultText = "";
        this.resultEl.style.display = "none";
        this.resultEl.textContent = "";
        this.errorEl.style.display = "none";
        this.resultActions.style.display = "none";
        this.unconfiguredEl.style.display = this.configured ? "none" : "block";
        this.genBtn.disabled = !this.configured;
        this.overlay.style.display = "flex";
        var self = this;
        setTimeout(function () { self.promptEl.focus(); }, 30);
    };

    NEAI.prototype.close = function () {
        this.overlay.style.display = "none";
    };

    NEAI.prototype._generate = function (instruction) {
        if (!this.configured) return;
        instruction = (instruction || "").trim();
        if (!instruction) { this.promptEl.focus(); return; }
        var self = this;
        this.errorEl.style.display = "none";
        this.resultActions.style.display = "none";
        this.resultEl.style.display = "block";
        this.resultEl.innerHTML = '<i class="notched circle loading icon"></i> Thinking...';
        this.genBtn.disabled = true;

        var data = { prompt: instruction };
        var ctx = this.opts.getContext ? this.opts.getContext() : "";
        if (ctx) data.context = ctx;
        if (this.modelRow.style.display !== "none" && this.modelSel.value) data.model = this.modelSel.value;

        this.opts.agiRun("backend/ai.agi", data, function (resp) {
            self.genBtn.disabled = false;
            var d = self._parse(resp);
            if (!d || d.error) {
                self.resultEl.style.display = "none";
                self.errorEl.style.display = "block";
                self.errorEl.textContent = (d && d.error) ? d.error : "AI request failed.";
                return;
            }
            self.resultText = d.text || "";
            self.resultEl.textContent = self.resultText;
            self.resultActions.style.display = self.resultText ? "flex" : "none";
        }, function () {
            self.genBtn.disabled = false;
            self.resultEl.style.display = "none";
            self.errorEl.style.display = "block";
            self.errorEl.textContent = "AI request failed (network or server error).";
        }, 120000);
    };

    NEAI.prototype._insert = function (replace) {
        if (!this.resultText) return;
        var blocks = MD.markdownToBlocks(this.resultText);
        var anchor = this.targetId;
        var inserted = [];
        for (var i = 0; i < blocks.length; i++) {
            this.editor.insertAfter(anchor, blocks[i]);
            anchor = blocks[i].id;
            inserted.push(blocks[i].id);
        }
        if (replace && this.targetId) {
            this.editor.deleteBlock(this.targetId);
        }
        if (inserted.length) this.editor.focusBlock(inserted[inserted.length - 1]);
        this.close();
    };

    global.NEAI = NEAI;
})(window);
