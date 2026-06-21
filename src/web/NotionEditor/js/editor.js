/*
    NotionEditor - Block editor core

    A block based, contentEditable editor. Each block is its own editable region
    which keeps structural edits, collaborative patching and per-block presence
    simple. The editor is transport agnostic: it emits operations through
    callbacks and exposes applyRemoteOp() so the collaboration layer can drive it.

    Operations (also the collaboration wire ops):
      { kind:"update", block }                full block replace by id
      { kind:"insert", block, afterId|null }  insert after a block (null = head)
      { kind:"delete", id }
      { kind:"move",   id, afterId|null }
*/
(function (global) {
    "use strict";

    var MD = global.NEMarkdown;

    // ---- small DOM / caret helpers --------------------------------------------
    var NEUtil = {
        debounce: function (fn, ms) {
            var t;
            return function () {
                var ctx = this, args = arguments;
                clearTimeout(t);
                t = setTimeout(function () { fn.apply(ctx, args); }, ms);
            };
        },
        caretOffset: function (el) {
            var sel = window.getSelection();
            if (!sel || sel.rangeCount === 0) return 0;
            var range = sel.getRangeAt(0);
            if (!el.contains(range.endContainer)) return 0;
            var pre = range.cloneRange();
            pre.selectNodeContents(el);
            pre.setEnd(range.endContainer, range.endOffset);
            return pre.toString().length;
        },
        atStart: function (el) { return NEUtil.caretOffset(el) === 0; },
        atEnd: function (el) { return NEUtil.caretOffset(el) === el.textContent.length; },
        setCaret: function (el, offset) {
            el.focus();
            var walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT, null, false);
            var node, remaining = offset, last = null;
            while ((node = walker.nextNode())) {
                last = node;
                if (node.length >= remaining) {
                    var r = document.createRange();
                    r.setStart(node, remaining);
                    r.collapse(true);
                    var s = window.getSelection();
                    s.removeAllRanges();
                    s.addRange(r);
                    return;
                }
                remaining -= node.length;
            }
            NEUtil.caretToEnd(el);
        },
        caretToEnd: function (el) {
            el.focus();
            var r = document.createRange();
            r.selectNodeContents(el);
            r.collapse(false);
            var s = window.getSelection();
            s.removeAllRanges();
            s.addRange(r);
        },
        caretToStart: function (el) {
            el.focus();
            var r = document.createRange();
            r.selectNodeContents(el);
            r.collapse(true);
            var s = window.getSelection();
            s.removeAllRanges();
            s.addRange(r);
        },
        // Split the inline HTML of a contentEditable at the caret into before/after.
        splitAtCaret: function (el) {
            var sel = window.getSelection();
            var before = el.innerHTML, after = "";
            if (sel && sel.rangeCount) {
                var caret = sel.getRangeAt(0);
                var pre = caret.cloneRange();
                pre.selectNodeContents(el);
                pre.setEnd(caret.endContainer, caret.endOffset);
                var post = caret.cloneRange();
                post.selectNodeContents(el);
                post.setStart(caret.endContainer, caret.endOffset);
                var d1 = document.createElement("div");
                d1.appendChild(pre.cloneContents());
                var d2 = document.createElement("div");
                d2.appendChild(post.cloneContents());
                before = d1.innerHTML;
                after = d2.innerHTML;
            }
            return { before: before, after: after };
        }
    };

    // ---- slash command catalogue ----------------------------------------------
    // Each command transforms or inserts a block. Media / date / ai delegate to
    // editor option callbacks.
    var SLASH_COMMANDS = [
        { id: "paragraph", label: "Text", icon: "font", keys: "text paragraph plain", set: "paragraph" },
        { id: "h1", label: "Heading 1", icon: "heading", keys: "h1 title big heading", set: "h1" },
        { id: "h2", label: "Heading 2", icon: "heading", keys: "h2 subtitle heading", set: "h2" },
        { id: "h3", label: "Heading 3", icon: "heading", keys: "h3 small heading", set: "h3" },
        { id: "bulleted", label: "Bulleted list", icon: "list ul", keys: "bullet unordered list", set: "bulleted" },
        { id: "numbered", label: "Numbered list", icon: "list ol", keys: "number ordered list", set: "numbered" },
        { id: "todo", label: "To-do list", icon: "check square outline", keys: "todo task checkbox check", set: "todo" },
        { id: "quote", label: "Quote", icon: "quote left", keys: "quote blockquote", set: "quote" },
        { id: "callout", label: "Callout", icon: "info circle", keys: "callout note info", set: "callout" },
        { id: "code", label: "Code", icon: "code", keys: "code snippet pre", set: "code" },
        { id: "divider", label: "Divider", icon: "minus", keys: "divider hr line separator", set: "divider" },
        { id: "image", label: "Image", icon: "image outline", keys: "image picture photo", action: "image" },
        { id: "video", label: "Video", icon: "film", keys: "video movie clip", action: "video" },
        { id: "date", label: "Today's date", icon: "calendar alternate outline", keys: "date today time", action: "date" },
        { id: "ai", label: "Ask AI", icon: "magic", keys: "ai gpt assistant write generate", action: "ai" }
    ];

    function NEEditor(root, opts) {
        this.root = root;
        this.opts = opts || {};
        this.blocks = [];
        this.byId = {};            // id -> { block, el, contentEl }
        this.focusedId = null;
        this.applyingRemote = false;
        this.pendingRemote = {};   // id -> block update deferred while focused
        this.presenceEls = [];     // elements currently flagged with remote presence
        this._dirtyIds = {};       // block ids with pending debounced update emission
        this._flush = NEUtil.debounce(this._flushUpdates.bind(this), 180);
        this._buildSlashMenu();
        this._buildToolbar();
        this._bind();
    }

    NEEditor.prototype.emptyDoc = function () {
        this.setBlocks([this._newBlock("paragraph")]);
    };

    NEEditor.prototype._newBlock = function (type, fields) {
        var b = { id: MD.uid(), type: type || "paragraph", text: "", checked: false, src: "", alt: "", lang: "" };
        if (fields) { for (var k in fields) { b[k] = fields[k]; } }
        return b;
    };

    // ---- model accessors -------------------------------------------------------
    NEEditor.prototype.getBlocks = function () { return JSON.parse(JSON.stringify(this.blocks)); };
    NEEditor.prototype.setBlocks = function (blocks) {
        this.blocks = (blocks && blocks.length) ? blocks : [this._newBlock("paragraph")];
        this.render();
    };
    NEEditor.prototype.loadMarkdown = function (md) { this.setBlocks(MD.markdownToBlocks(md)); };
    NEEditor.prototype.toMarkdown = function () { return MD.blocksToMarkdown(this.blocks); };
    NEEditor.prototype.indexOf = function (id) {
        for (var i = 0; i < this.blocks.length; i++) { if (this.blocks[i].id === id) return i; }
        return -1;
    };

    // ---- rendering -------------------------------------------------------------
    NEEditor.prototype.render = function () {
        this.root.innerHTML = "";
        this.byId = {};
        for (var i = 0; i < this.blocks.length; i++) {
            this.root.appendChild(this._renderBlock(this.blocks[i]));
        }
        this._refreshListMarkers();
    };

    NEEditor.prototype._renderBlock = function (block) {
        var self = this;
        var el = document.createElement("div");
        el.className = "ne-block";
        el.setAttribute("data-id", block.id);
        el.setAttribute("data-type", block.type);

        var gutter = document.createElement("div");
        gutter.className = "ne-gutter";
        var add = document.createElement("button");
        add.className = "ne-add";
        add.title = "Add block below";
        add.innerHTML = '<i class="plus icon"></i>';
        add.addEventListener("click", function (e) {
            e.preventDefault();
            var nb = self.insertAfter(block.id, self._newBlock("paragraph"));
            self.focusBlock(nb.id);
            self._openSlash(nb.id);
        });
        var handle = document.createElement("span");
        handle.className = "ne-handle";
        handle.title = "Drag to move";
        handle.setAttribute("draggable", "true");
        handle.innerHTML = '<i class="ellipsis vertical icon"></i>';
        this._wireDrag(handle, el, block);
        gutter.appendChild(add);
        gutter.appendChild(handle);

        var body = document.createElement("div");
        body.className = "ne-body";
        this._renderBody(block, body);

        el.appendChild(gutter);
        el.appendChild(body);
        this.byId[block.id] = { block: block, el: el, contentEl: el.querySelector(".ne-content") || null };
        return el;
    };

    // Build the type-specific body. Stores .ne-content reference via querySelector.
    NEEditor.prototype._renderBody = function (block, body) {
        var self = this;
        body.innerHTML = "";

        if (block.type === "divider") {
            var hr = document.createElement("hr");
            hr.className = "ne-divider";
            body.appendChild(hr);
            return;
        }

        if (block.type === "image" || block.type === "video") {
            var media = document.createElement(block.type === "image" ? "img" : "video");
            media.className = "ne-media";
            media.src = this.opts.mediaUrl ? this.opts.mediaUrl(block.src) : block.src;
            if (block.type === "video") { media.controls = true; }
            if (block.type === "image") { media.alt = block.alt || ""; }
            body.appendChild(media);
            var cap = document.createElement("div");
            cap.className = "ne-content ne-caption";
            cap.setAttribute("contenteditable", "true");
            cap.setAttribute("data-ph", "Write a caption...");
            cap.innerHTML = block.alt || "";
            cap.addEventListener("input", function () {
                block.alt = cap.textContent;
                self._emitUpdate(block.id);
            });
            body.appendChild(cap);
            return;
        }

        if (block.type === "code") {
            var head = document.createElement("div");
            head.className = "ne-code-head";
            var langInput = document.createElement("input");
            langInput.className = "ne-code-lang";
            langInput.placeholder = "language";
            langInput.value = block.lang || "";
            langInput.addEventListener("input", function () {
                block.lang = langInput.value.trim();
                self._emitUpdate(block.id);
            });
            head.appendChild(langInput);
            var ta = document.createElement("textarea");
            ta.className = "ne-content ne-code";
            ta.spellcheck = false;
            ta.value = block.text || "";
            ta.addEventListener("input", function () {
                block.text = ta.value;
                self._autoGrow(ta);
                self._emitUpdate(block.id);
            });
            ta.addEventListener("focus", function () { self.focusedId = block.id; self._emitPresence(); });
            body.appendChild(head);
            body.appendChild(ta);
            setTimeout(function () { self._autoGrow(ta); }, 0);
            return;
        }

        // text-like blocks (paragraph, headings, list items, quote, callout)
        var wrap = document.createElement("div");
        wrap.className = "ne-line";

        if (block.type === "todo") {
            var cb = document.createElement("input");
            cb.type = "checkbox";
            cb.className = "ne-check";
            cb.checked = !!block.checked;
            cb.addEventListener("change", function () {
                block.checked = cb.checked;
                content.classList.toggle("ne-done", cb.checked);
                self._emitUpdate(block.id);
            });
            wrap.appendChild(cb);
        } else if (block.type === "bulleted" || block.type === "numbered") {
            var marker = document.createElement("span");
            marker.className = "ne-marker";
            wrap.appendChild(marker);
        }

        var content = document.createElement("div");
        content.className = "ne-content";
        content.setAttribute("contenteditable", "true");
        content.setAttribute("data-ph", this._placeholder(block.type));
        content.innerHTML = block.text || "";
        if (block.type === "todo" && block.checked) { content.classList.add("ne-done"); }
        wrap.appendChild(content);
        body.appendChild(wrap);
    };

    NEEditor.prototype._placeholder = function (type) {
        switch (type) {
            case "h1": return "Heading 1";
            case "h2": return "Heading 2";
            case "h3": return "Heading 3";
            case "quote": return "Quote";
            case "callout": return "Callout";
            case "todo": return "To-do";
            case "bulleted":
            case "numbered": return "List item";
            default: return "Type '/' for commands";
        }
    };

    NEEditor.prototype._autoGrow = function (ta) {
        ta.style.height = "auto";
        ta.style.height = (ta.scrollHeight + 2) + "px";
    };

    NEEditor.prototype._refreshListMarkers = function () {
        var n = 0;
        for (var i = 0; i < this.blocks.length; i++) {
            var b = this.blocks[i];
            var entry = this.byId[b.id];
            if (!entry) continue;
            var marker = entry.el.querySelector(".ne-marker");
            if (b.type === "numbered") {
                if (i === 0 || this.blocks[i - 1].type !== "numbered") { n = 0; }
                n++;
                if (marker) marker.textContent = n + ".";
            } else if (b.type === "bulleted") {
                if (marker) marker.textContent = "•";
            } else {
                n = 0;
            }
        }
    };

    // ---- structural ops (also used to apply remote ops) ------------------------
    NEEditor.prototype.insertAfter = function (afterId, block) {
        var idx = afterId == null ? -1 : this.indexOf(afterId);
        this.blocks.splice(idx + 1, 0, block);
        var node = this._renderBlock(block);
        if (idx < 0) {
            this.root.insertBefore(node, this.root.firstChild);
        } else {
            var ref = this.byId[afterId];
            this.root.insertBefore(node, ref ? ref.el.nextSibling : null);
        }
        this._refreshListMarkers();
        if (!this.applyingRemote) {
            this._emit({ kind: "insert", block: JSON.parse(JSON.stringify(block)), afterId: afterId });
        }
        return block;
    };

    NEEditor.prototype.deleteBlock = function (id) {
        var idx = this.indexOf(id);
        if (idx < 0) return;
        this.blocks.splice(idx, 1);
        var entry = this.byId[id];
        if (entry && entry.el.parentNode) { entry.el.parentNode.removeChild(entry.el); }
        delete this.byId[id];
        this._refreshListMarkers();
        if (!this.applyingRemote) { this._emit({ kind: "delete", id: id }); }
    };

    NEEditor.prototype.setType = function (id, type) {
        var entry = this.byId[id];
        if (!entry) return;
        var block = entry.block;
        block.type = type;
        if (type === "divider") { block.text = ""; }
        entry.el.setAttribute("data-type", type);
        var body = entry.el.querySelector(".ne-body");
        this._renderBody(block, body);
        entry.contentEl = entry.el.querySelector(".ne-content");
        this._refreshListMarkers();
        this._emitUpdate(id);
        if (type !== "divider" && type !== "image" && type !== "video" && entry.contentEl) {
            NEUtil.caretToEnd(entry.contentEl);
        }
    };

    // ---- change emission -------------------------------------------------------
    NEEditor.prototype._emit = function (op) {
        if (this.applyingRemote) return;
        if (this.opts.onOp) this.opts.onOp(op);
        if (this.opts.onDirty) this.opts.onDirty();
    };

    NEEditor.prototype._emitUpdate = function (id) {
        var entry = this.byId[id];
        if (!entry) return;
        this._emit({ kind: "update", block: JSON.parse(JSON.stringify(entry.block)) });
    };

    NEEditor.prototype._emitPresence = function () {
        if (this.opts.onPresence) this.opts.onPresence(this.focusedId || "");
    };

    // ---- event binding ---------------------------------------------------------
    NEEditor.prototype._bind = function () {
        var self = this;

        this.root.addEventListener("input", function (e) {
            var content = e.target.closest ? e.target.closest(".ne-content") : null;
            if (!content || content.classList.contains("ne-code") || content.classList.contains("ne-caption")) return;
            var block = self._blockFromNode(content);
            if (!block) return;
            block.text = content.innerHTML;
            if (self._maybeInputRule(block, content)) return;
            // While composing a slash command don't sync the transient "/query".
            if (self._maybeSlash(block, content)) return;
            self._scheduleUpdate(block.id);
        });

        this.root.addEventListener("keydown", function (e) { self._onKeydown(e); });

        this.root.addEventListener("focusin", function (e) {
            var content = e.target.closest ? e.target.closest(".ne-content") : null;
            if (!content) return;
            var block = self._blockFromNode(content);
            if (block) { self.focusedId = block.id; self._emitPresence(); }
        });

        this.root.addEventListener("focusout", function (e) {
            var content = e.target.closest ? e.target.closest(".ne-content") : null;
            if (!content) return;
            var block = self._blockFromNode(content);
            if (block && self.pendingRemote[block.id]) {
                self._applyUpdateNow(self.pendingRemote[block.id]);
                delete self.pendingRemote[block.id];
            }
            // Close the slash menu when focus genuinely leaves the editor. Menu
            // item clicks use mousedown+preventDefault so they don't blur first.
            setTimeout(function () {
                if (!document.activeElement || !document.activeElement.closest ||
                    !document.activeElement.closest(".ne-block")) {
                    self._closeSlash();
                }
            }, 0);
        });

        document.addEventListener("selectionchange", function () {
            self._updateToolbar();
        });

        this.root.addEventListener("paste", function (e) { self._onPaste(e); });
    };

    // Batch per-block update emission so editing several blocks within the debounce
    // window never drops an update (a single shared debounce would keep only the
    // last id).
    NEEditor.prototype._scheduleUpdate = function (id) {
        this._dirtyIds[id] = true;
        this._flush();
    };
    NEEditor.prototype._flushUpdates = function () {
        for (var id in this._dirtyIds) {
            if (this.byId[id]) this._emitUpdate(id);
        }
        this._dirtyIds = {};
    };

    NEEditor.prototype._blockFromNode = function (node) {
        var blockEl = node.closest(".ne-block");
        if (!blockEl) return null;
        var entry = this.byId[blockEl.getAttribute("data-id")];
        return entry ? entry.block : null;
    };

    // Markdown shortcuts at the start of a paragraph (e.g. "# ", "- ", "1. ").
    NEEditor.prototype._maybeInputRule = function (block, content) {
        if (block.type !== "paragraph") return false;
        var text = content.textContent;
        var rules = [
            { re: /^#\s/, type: "h1" },
            { re: /^##\s/, type: "h2" },
            { re: /^###\s/, type: "h3" },
            { re: /^[-*]\s/, type: "bulleted" },
            { re: /^1\.\s/, type: "numbered" },
            { re: /^\[\]\s/, type: "todo" },
            { re: /^\[\s\]\s/, type: "todo" },
            { re: /^>\s/, type: "quote" },
            { re: /^```$/, type: "code" }
        ];
        for (var i = 0; i < rules.length; i++) {
            if (rules[i].re.test(text)) {
                content.innerHTML = "";
                block.text = "";
                this.setType(block.id, rules[i].type);
                return true;
            }
        }
        return false;
    };

    // ---- keyboard --------------------------------------------------------------
    NEEditor.prototype._onKeydown = function (e) {
        if (this.slashOpen) {
            if (this._slashKey(e)) return;
        }

        var content = e.target.closest ? e.target.closest(".ne-content") : null;
        if (!content) return;
        var block = this._blockFromNode(content);
        if (!block) return;

        if (content.classList.contains("ne-code") || content.classList.contains("ne-caption")) {
            return; // textarea / caption handle their own keys natively
        }

        if (e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            this._onEnter(block, content);
        } else if (e.key === "Backspace") {
            if (NEUtil.atStart(content)) {
                this._onBackspaceStart(e, block, content);
            }
        } else if (e.key === "ArrowUp") {
            if (NEUtil.atStart(content)) { this._focusSibling(block.id, -1, true); e.preventDefault(); }
        } else if (e.key === "ArrowDown") {
            if (NEUtil.atEnd(content)) { this._focusSibling(block.id, 1, false); e.preventDefault(); }
        } else if (e.key === "Escape") {
            this._closeSlash();
        } else if ((e.ctrlKey || e.metaKey) && !e.altKey) {
            var k = e.key.toLowerCase();
            if (k === "b") { e.preventDefault(); this._format("bold"); }
            else if (k === "i") { e.preventDefault(); this._format("italic"); }
            else if (k === "u") { e.preventDefault(); this._format("underline"); }
            else if (k === "e") { e.preventDefault(); this._format("code"); }
            else if (k === "s" && this.opts.onSave) { e.preventDefault(); this.opts.onSave(); }
        }
    };

    NEEditor.prototype._onEnter = function (block, content) {
        var parts = NEUtil.splitAtCaret(content);
        // List item: empty Enter exits the list.
        if (MD.isList(block.type) && content.textContent.trim() === "") {
            this.setType(block.id, "paragraph");
            return;
        }
        var newType = "paragraph";
        if (MD.isList(block.type)) { newType = block.type; }

        block.text = parts.before;
        content.innerHTML = parts.before;
        this._emitUpdate(block.id);

        var nb = this._newBlock(newType, { text: parts.after, checked: false });
        this.insertAfter(block.id, nb);
        this.focusBlock(nb.id);
    };

    NEEditor.prototype._onBackspaceStart = function (e, block, content) {
        // First backspace on a styled/list block reverts it to a paragraph.
        if (block.type !== "paragraph") {
            e.preventDefault();
            this.setType(block.id, "paragraph");
            return;
        }
        var idx = this.indexOf(block.id);
        if (idx <= 0) return;
        var prev = this.blocks[idx - 1];
        var prevEntry = this.byId[prev.id];
        if (!prevEntry || prev.type === "divider" || prev.type === "image" || prev.type === "video" || prev.type === "code") {
            return; // don't merge into non-text blocks
        }
        e.preventDefault();
        var prevContent = prevEntry.contentEl;
        var caret = prevContent.textContent.length;
        prevContent.innerHTML = prevContent.innerHTML + content.innerHTML;
        prev.text = prevContent.innerHTML;
        this._emitUpdate(prev.id);
        this.deleteBlock(block.id);
        NEUtil.setCaret(prevContent, caret);
    };

    NEEditor.prototype._focusSibling = function (id, dir, toEnd) {
        var idx = this.indexOf(id);
        var target = this.blocks[idx + dir];
        if (!target) return;
        var entry = this.byId[target.id];
        if (!entry) return;
        var c = entry.contentEl || entry.el.querySelector(".ne-content, .ne-code");
        if (!c) { this._focusSibling(target.id, dir, toEnd); return; }
        if (toEnd) { NEUtil.caretToEnd(c); } else { NEUtil.caretToStart(c); }
    };

    NEEditor.prototype.focusBlock = function (id) {
        var entry = this.byId[id];
        if (!entry) return;
        var c = entry.contentEl || entry.el.querySelector(".ne-content, .ne-code");
        if (c) { NEUtil.caretToEnd(c); this.focusedId = id; this._emitPresence(); }
    };

    // ---- inline formatting toolbar --------------------------------------------
    NEEditor.prototype._buildToolbar = function () {
        var self = this;
        var bar = document.createElement("div");
        bar.className = "ne-toolbar";
        bar.style.display = "none";
        var buttons = [
            { cmd: "bold", icon: "bold", t: "Bold" },
            { cmd: "italic", icon: "italic", t: "Italic" },
            { cmd: "underline", icon: "underline", t: "Underline" },
            { cmd: "strikeThrough", icon: "strikethrough", t: "Strikethrough" },
            { cmd: "code", icon: "code", t: "Inline code" },
            { cmd: "link", icon: "linkify", t: "Link" }
        ];
        buttons.forEach(function (b) {
            var btn = document.createElement("button");
            btn.innerHTML = '<i class="' + b.icon + ' icon"></i>';
            btn.title = b.t;
            btn.addEventListener("mousedown", function (e) {
                e.preventDefault();
                self._format(b.cmd);
            });
            bar.appendChild(btn);
        });
        document.body.appendChild(bar);
        this.toolbar = bar;
    };

    NEEditor.prototype._updateToolbar = function () {
        var sel = window.getSelection();
        if (!sel || sel.rangeCount === 0 || sel.isCollapsed) { this.toolbar.style.display = "none"; return; }
        var range = sel.getRangeAt(0);
        var container = range.commonAncestorContainer;
        var el = container.nodeType === 1 ? container : container.parentNode;
        var content = el.closest ? el.closest(".ne-content") : null;
        if (!content || content.classList.contains("ne-code") || content.classList.contains("ne-caption")) {
            this.toolbar.style.display = "none";
            return;
        }
        var rect = range.getBoundingClientRect();
        this.toolbar.style.display = "flex";
        this.toolbar.style.top = (window.scrollY + rect.top - this.toolbar.offsetHeight - 8) + "px";
        this.toolbar.style.left = (window.scrollX + rect.left + rect.width / 2 - this.toolbar.offsetWidth / 2) + "px";
    };

    NEEditor.prototype._format = function (cmd) {
        var sel = window.getSelection();
        if (!sel || sel.rangeCount === 0) return;
        var content = this._activeContent();
        if (!content) return;
        if (cmd === "code") {
            this._wrapInline("code");
        } else if (cmd === "link") {
            var url = window.prompt("Link URL");
            if (url) { document.execCommand("createLink", false, url); }
        } else {
            document.execCommand(cmd, false, null);
        }
        var block = this._blockFromNode(content);
        if (block) { block.text = content.innerHTML; this._emitUpdate(block.id); }
        this._updateToolbar();
    };

    // Wrap the current selection in a tag (used for inline code where execCommand
    // has no native command).
    NEEditor.prototype._wrapInline = function (tag) {
        var sel = window.getSelection();
        if (!sel.rangeCount) return;
        var range = sel.getRangeAt(0);
        var existing = range.commonAncestorContainer.parentNode;
        if (existing && existing.tagName && existing.tagName.toLowerCase() === tag) {
            // unwrap
            var parent = existing.parentNode;
            while (existing.firstChild) { parent.insertBefore(existing.firstChild, existing); }
            parent.removeChild(existing);
            return;
        }
        var wrapper = document.createElement(tag);
        try {
            wrapper.appendChild(range.extractContents());
            range.insertNode(wrapper);
        } catch (err) { /* ignore complex selections */ }
    };

    NEEditor.prototype._activeContent = function () {
        var sel = window.getSelection();
        if (!sel || sel.rangeCount === 0) return null;
        var n = sel.getRangeAt(0).commonAncestorContainer;
        var el = n.nodeType === 1 ? n : n.parentNode;
        return el.closest ? el.closest(".ne-content") : null;
    };

    // ---- paste (media + markdown) ---------------------------------------------
    NEEditor.prototype._onPaste = function (e) {
        var self = this;
        var dt = e.clipboardData;
        if (!dt) return;
        var files = [];
        if (dt.items) {
            for (var i = 0; i < dt.items.length; i++) {
                var it = dt.items[i];
                if (it.kind === "file") {
                    var f = it.getAsFile();
                    if (f && (/^image\//.test(f.type) || /^video\//.test(f.type))) { files.push(f); }
                }
            }
        }
        if (files.length && this.opts.uploadBlob) {
            e.preventDefault();
            var block = this._blockFromNode(e.target);
            var afterId = block ? block.id : (this.blocks.length ? this.blocks[this.blocks.length - 1].id : null);
            files.forEach(function (file) {
                self.opts.uploadBlob(file, function (res) {
                    if (!res || !res.vpath) return;
                    var kind = /^video\//.test(file.type) ? "video" : "image";
                    var nb = self._newBlock(kind, { src: res.vpath, alt: file.name || "" });
                    self.insertAfter(afterId, nb);
                    afterId = nb.id;
                });
            });
            return;
        }

        // Plain-text / markdown paste: keep it clean, parse multi-line as markdown.
        var text = dt.getData("text/plain");
        if (text && text.indexOf("\n") !== -1) {
            e.preventDefault();
            var cur = this._blockFromNode(e.target);
            var newBlocks = MD.markdownToBlocks(text);
            var anchor = cur ? cur.id : null;
            for (var j = 0; j < newBlocks.length; j++) {
                this.insertAfter(anchor, newBlocks[j]);
                anchor = newBlocks[j].id;
            }
            if (newBlocks.length) this.focusBlock(newBlocks[newBlocks.length - 1].id);
        } else if (text) {
            e.preventDefault();
            document.execCommand("insertText", false, text);
        }
    };

    // ---- drag to reorder -------------------------------------------------------
    NEEditor.prototype._wireDrag = function (handle, el, block) {
        var self = this;
        handle.addEventListener("dragstart", function (e) {
            e.dataTransfer.setData("text/ne-block", block.id);
            e.dataTransfer.effectAllowed = "move";
            el.classList.add("ne-dragging");
        });
        handle.addEventListener("dragend", function () { el.classList.remove("ne-dragging"); });
        el.addEventListener("dragover", function (e) {
            var types = e.dataTransfer.types;
            var has = types && (types.indexOf ? types.indexOf("text/ne-block") !== -1
                : (types.contains && types.contains("text/ne-block")));
            if (!has) return;
            e.preventDefault();
            el.classList.add("ne-dragover");
        });
        el.addEventListener("dragleave", function () { el.classList.remove("ne-dragover"); });
        el.addEventListener("drop", function (e) {
            el.classList.remove("ne-dragover");
            var srcId = e.dataTransfer.getData("text/ne-block");
            if (!srcId || srcId === block.id) return;
            e.preventDefault();
            self.moveBlock(srcId, block.id);
        });
    };

    NEEditor.prototype.moveBlock = function (id, afterId) {
        var idx = this.indexOf(id);
        if (idx < 0) return;
        var block = this.blocks.splice(idx, 1)[0];
        var targetIdx = afterId == null ? -1 : this.indexOf(afterId);
        this.blocks.splice(targetIdx + 1, 0, block);
        var entry = this.byId[id];
        var ref = this.byId[afterId];
        if (entry && ref) { this.root.insertBefore(entry.el, ref.el.nextSibling); }
        this._refreshListMarkers();
        if (!this.applyingRemote) { this._emit({ kind: "move", id: id, afterId: afterId }); }
    };

    // ---- slash menu ------------------------------------------------------------
    NEEditor.prototype._buildSlashMenu = function () {
        var menu = document.createElement("div");
        menu.className = "ne-slash";
        menu.style.display = "none";
        document.body.appendChild(menu);
        this.slashMenu = menu;
        this.slashOpen = false;
        this.slashBlockId = null;
        this.slashIndex = 0;
    };

    NEEditor.prototype._maybeSlash = function (block, content) {
        var text = content.textContent;
        if (text.charAt(0) === "/") {
            this._openSlash(block.id, text.slice(1));
            return true;
        }
        if (this.slashOpen && this.slashBlockId === block.id) {
            this._closeSlash();
        }
        return false;
    };

    NEEditor.prototype._openSlash = function (blockId, query) {
        this.slashOpen = true;
        this.slashBlockId = blockId;
        this.slashIndex = 0;
        this._renderSlash(query || "");
        var entry = this.byId[blockId];
        if (entry) {
            var rect = (entry.contentEl || entry.el).getBoundingClientRect();
            this.slashMenu.style.display = "block";
            this.slashMenu.style.top = (window.scrollY + rect.bottom + 4) + "px";
            this.slashMenu.style.left = (window.scrollX + rect.left) + "px";
        }
    };

    NEEditor.prototype._renderSlash = function (query) {
        var self = this;
        var q = (query || "").toLowerCase();
        this.slashFiltered = SLASH_COMMANDS.filter(function (c) {
            return q === "" || c.label.toLowerCase().indexOf(q) !== -1 || c.keys.indexOf(q) !== -1;
        });
        if (this.slashIndex >= this.slashFiltered.length) this.slashIndex = 0;
        this.slashMenu.innerHTML = "";
        if (this.slashFiltered.length === 0) {
            var none = document.createElement("div");
            none.className = "ne-slash-empty";
            none.textContent = "No matching blocks";
            this.slashMenu.appendChild(none);
            return;
        }
        this.slashFiltered.forEach(function (cmd, i) {
            var item = document.createElement("div");
            item.className = "ne-slash-item" + (i === self.slashIndex ? " active" : "");
            item.innerHTML = '<i class="' + cmd.icon + ' icon"></i><span>' + cmd.label + "</span>";
            item.addEventListener("mousedown", function (e) {
                e.preventDefault();
                self._applySlash(cmd);
            });
            self.slashMenu.appendChild(item);
        });
    };

    NEEditor.prototype._slashKey = function (e) {
        if (e.key === "ArrowDown") {
            this.slashIndex = Math.min(this.slashIndex + 1, this.slashFiltered.length - 1);
            this._renderSlash(this._slashQuery());
            e.preventDefault();
            return true;
        }
        if (e.key === "ArrowUp") {
            this.slashIndex = Math.max(this.slashIndex - 1, 0);
            this._renderSlash(this._slashQuery());
            e.preventDefault();
            return true;
        }
        if (e.key === "Enter") {
            var cmd = this.slashFiltered[this.slashIndex];
            if (cmd) { this._applySlash(cmd); }
            e.preventDefault();
            return true;
        }
        if (e.key === "Escape") { this._closeSlash(); e.preventDefault(); return true; }
        return false;
    };

    NEEditor.prototype._slashQuery = function () {
        var entry = this.byId[this.slashBlockId];
        if (!entry || !entry.contentEl) return "";
        var t = entry.contentEl.textContent;
        return t.charAt(0) === "/" ? t.slice(1) : "";
    };

    NEEditor.prototype._applySlash = function (cmd) {
        var blockId = this.slashBlockId;
        var entry = this.byId[blockId];
        this._closeSlash();
        if (!entry) return;
        // Clear the "/query" text.
        if (entry.contentEl) { entry.contentEl.innerHTML = ""; }
        entry.block.text = "";

        if (cmd.set) {
            this.setType(blockId, cmd.set);
            return;
        }
        this._runAction(cmd.action, blockId);
    };

    NEEditor.prototype._runAction = function (action, blockId) {
        var self = this;
        if (action === "date") {
            var entry = this.byId[blockId];
            var d = new Date().toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" });
            if (entry && entry.contentEl) {
                entry.contentEl.innerHTML = NEMarkdown.escapeHtml(d);
                entry.block.text = entry.contentEl.innerHTML;
                this._emitUpdate(blockId);
                NEUtil.caretToEnd(entry.contentEl);
            }
        } else if (action === "image" || action === "video") {
            if (this.opts.pickFromStorage) {
                this.opts.pickFromStorage(action, function (vpath) {
                    if (!vpath) return;
                    var nb = self._newBlock(action, { src: vpath });
                    self.insertAfter(blockId, nb);
                    // If the trigger block is an empty paragraph, drop it.
                    var b = self.byId[blockId];
                    if (b && b.block.type === "paragraph" && b.block.text === "") { self.deleteBlock(blockId); }
                });
            }
        } else if (action === "ai") {
            if (this.opts.onAI) this.opts.onAI(blockId);
        }
    };

    NEEditor.prototype._closeSlash = function () {
        this.slashOpen = false;
        this.slashBlockId = null;
        this.slashMenu.style.display = "none";
    };

    // ---- remote operations (collaboration) ------------------------------------
    NEEditor.prototype.applyRemoteOp = function (op) {
        this.applyingRemote = true;
        try {
            if (op.kind === "update") {
                if (op.block.id === this.focusedId) {
                    // Defer until the local user leaves the block to avoid caret jumps.
                    this.pendingRemote[op.block.id] = op.block;
                } else if (this.byId[op.block.id]) {
                    this._applyUpdateNow(op.block);
                } else if (this.opts.onResync) {
                    // Unknown block id: our block ids have not aligned with the room
                    // (e.g. both clients joined a cold room at once). Pull the
                    // authoritative snapshot to re-align.
                    this.opts.onResync();
                }
            } else if (op.kind === "insert") {
                if (this.indexOf(op.block.id) === -1) { this.insertAfter(op.afterId, op.block); }
            } else if (op.kind === "delete") {
                if (op.id !== this.focusedId) { this.deleteBlock(op.id); }
            } else if (op.kind === "move") {
                this.moveBlock(op.id, op.afterId);
            }
        } finally {
            this.applyingRemote = false;
        }
    };

    NEEditor.prototype._applyUpdateNow = function (nb) {
        var entry = this.byId[nb.id];
        if (!entry) return;
        var typeChanged = entry.block.type !== nb.type;
        entry.block.type = nb.type;
        entry.block.text = nb.text;
        entry.block.checked = nb.checked;
        entry.block.src = nb.src;
        entry.block.alt = nb.alt;
        entry.block.lang = nb.lang;
        if (typeChanged) {
            entry.el.setAttribute("data-type", nb.type);
            this._renderBody(entry.block, entry.el.querySelector(".ne-body"));
            entry.contentEl = entry.el.querySelector(".ne-content");
        } else if (entry.contentEl) {
            if (entry.contentEl.tagName === "TEXTAREA") {
                if (entry.contentEl.value !== nb.text) entry.contentEl.value = nb.text;
            } else if (entry.contentEl.innerHTML !== nb.text) {
                entry.contentEl.innerHTML = nb.text;
            }
        }
        this._refreshListMarkers();
    };

    // ---- remote presence -------------------------------------------------------
    NEEditor.prototype.setRemotePresence = function (members, selfId) {
        // Clear previous highlights.
        for (var i = 0; i < this.presenceEls.length; i++) {
            var el = this.presenceEls[i];
            el.classList.remove("ne-remote");
            el.style.removeProperty("--ne-remote-color");
            var chip = el.querySelector(".ne-remote-chip");
            if (chip) chip.parentNode.removeChild(chip);
        }
        this.presenceEls = [];
        for (var j = 0; j < members.length; j++) {
            var m = members[j];
            if (m.id === selfId || !m.cursor) continue;
            var entry = this.byId[m.cursor];
            if (!entry) continue;
            entry.el.classList.add("ne-remote");
            entry.el.style.setProperty("--ne-remote-color", m.color);
            var existing = entry.el.querySelector(".ne-remote-chip");
            if (!existing) {
                var c = document.createElement("span");
                c.className = "ne-remote-chip";
                c.textContent = m.name;
                c.style.background = m.color;
                entry.el.appendChild(c);
            }
            this.presenceEls.push(entry.el);
        }
    };

    global.NEUtil = NEUtil;
    global.NEEditor = NEEditor;
})(window);
