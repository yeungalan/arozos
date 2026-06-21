# NotionEditor

A block-based, Notion-style document editor WebApp for ArozOS with real-time
multi-user collaboration, slash commands, media paste, AI assistance and
Markdown import/export.

## Features

| # | Capability | Where it lives |
|---|------------|----------------|
| 1 | **Real-time collaboration** on the same document | Go hub `mod/collab` + `collab.go`, client `js/collab.js` |
| 2 | **Paste image / video** (upload + embed) | `js/editor.js` paste handler, `index.html` `uploadBlob` |
| 3 | **Markdown output** (save to storage + download) | `js/markdown.js`, `backend/save.agi`, `backend/load.agi` |
| 4 | **Slash commands** to insert blocks / media / date / AI | `js/editor.js` (`SLASH_COMMANDS`) |
| 5 | **AI / LLM assistance** | `js/ai.js`, `backend/ai.agi` (uses the `aimodel` library) |
| 6 | **Notion formatting** (headings, lists, to-dos, quote, callout, code, divider, media, bold/italic/underline/strike/code/link) | `js/editor.js`, `js/markdown.js`, `css/editor.css` |
| 7 | **Live collaborators** (presence avatars + remote block highlight) | `js/collab.js`, `js/editor.js` `setRemotePresence`, `index.html` |

## Architecture

```
Browser (this WebApp)                         ArozOS core
┌───────────────────────────┐                 ┌───────────────────────────────┐
│ index.html                │   WebSocket     │ collab.go  (/api/collab/ws)   │
│  ├─ js/markdown.js  (md ⇄ blocks)  ◄────────►│   room-based pub/sub relay    │
│  ├─ js/editor.js    (blocks, slash, paste)   │   + presence + snapshot       │
│  ├─ js/collab.js    (ops, presence, sync) ──►│ mod/collab  (tested engine)   │
│  └─ js/ai.js        (AI panel)               │                               │
│        │  AGI (ao_module_agirun)             │ backend/*.agi (Otto VM):      │
│        └───────────────────────────────────►│   ai.agi      -> aimodel lib  │
│                                              │   save/load   -> filelib      │
└───────────────────────────┘                 └───────────────────────────────┘
```

- **Collaboration** is keyed by the document's virtual path: everyone who opens
  the same file shares a room. Edits are relayed as block **operations**
  (`insert` / `update` / `delete` / `move`) for low latency, while the hub also
  keeps one **snapshot** so late joiners sync instantly. Conflict handling is
  block-level last-write-wins (not character-level CRDT/OT) which suits document
  editing where collaborators usually work on different blocks.
- The **primary** member (oldest in the room, decided by the hub) is the single
  client that persists the document to disk, so a file is never written by
  several editors at once. A lone or offline editor always autosaves.
- Identity for presence comes from the authenticated ArozOS session, never the
  client, so names cannot be spoofed.

## Files

```
NotionEditor/
├── init.agi              module registration
├── index.html            shell + bootstrap (theme, load, save, header)
├── css/editor.css        light / dark themed styles
├── js/markdown.js        block <-> Markdown engine (incl. inline formatting)
├── js/editor.js          block editor core (NEUtil + NEEditor)
├── js/collab.js          collaboration client (NECollab)
├── js/ai.js              AI assistant panel (NEAI)
├── backend/load.agi      read a .md document
├── backend/save.agi      write a .md document
├── backend/ai.agi        LLM bridge via the aimodel library
├── backend/aistatus.agi  report whether AI is configured
└── img/icon.svg          module icon
```

## Notes

- AI requires an admin to configure a model under **System Settings &rsaquo; AI
  Integration**; the AI button hides itself when no model is available.
- Pasted media is uploaded to `user:/Documents/NotionEditor/media/` and embedded
  via the standard `media?file=` endpoint.
- Markdown is the on-disk format. Images export as `![alt](vpath)` and videos as
  a raw `<video>` tag, both of which round-trip back into blocks on load.

## Troubleshooting collaboration

The header shows a status pill: **Live** (connected), **Connecting**, or
**Offline**. Hover it for the room id and the last socket close code. The
browser console also logs the connection lifecycle (prefixed `[NotionEditor]`).

If the pill stays **Offline**:

1. **Rebuild and restart the server.** Collaboration is served by Go code
   (`/api/collab/ws`). Copying the web assets alone is not enough — you must
   `cd src && go build && ./arozos` so the new endpoint exists. A 404 on the
   WebSocket handshake (close code 1006) is the tell-tale sign of a stale binary.
2. **Open the *same saved file* on both sides.** The collaboration room is keyed
   by the document's virtual path, so both editors must point at the same `.md`
   file. Two freshly-created (unsaved) documents get separate rooms — save first,
   then open that file on the other side.
3. **Check module access.** The endpoint is gated by the `NotionEditor` module
   permission. Admins always have it; for other users an admin must grant the
   module to their permission group.
4. **Reverse proxies** must forward WebSocket upgrades for `/api/collab/ws`.

Block ids are generated per client when Markdown is parsed; they are aligned
across clients via the room snapshot on join, and self-heal (a snapshot is
re-requested) if an operation ever references an unknown block.

