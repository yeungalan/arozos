# Telegram Bot (AGI port)

A self-hosted Telegram group assistant for ArozOS. This is a faithful port of
the n8n **"Telegram Bot"** workflow to a single ArozOS AGI app — no n8n, no
external automation server. It long-polls the Telegram Bot API on a schedule and
answers messages with any OpenAI-compatible model through the AGI `aimodel`
library.

## What it does

For every message in a chat the bot is in, it:

1. **Stores** the message in a per-chat history table.
2. **Decides whether to reply** — if the text mentions a trigger keyword
   (`arozosbot`, `arozbot`, …), starts with the command prefix (`/chat`), is a
   reply to the bot, **or** by a small random chance (1% by default).
3. **Applies guardrails** — messages containing banned keywords are dropped.
4. **Augments with web search** — an LLM turns the message into a search query,
   DuckDuckGo is queried, and the results are added to the prompt (optional).
5. **Adds chat history (RAG)** — recent messages from the same chat (last 24h)
   are added to the prompt (optional).
6. **Generates a reply** — the main model is called with the elaborate system
   prompt, the sender, the replied-to message, the current UTC time, the search
   results and the history.
7. **Replies in-thread** on Telegram and **stores** its own reply in history.

## Files

| File | Role |
|------|------|
| `init.agi` | Registers the desktop module |
| `cron.agi` | **The converted workflow** — scheduled poll + answer pipeline |
| `backend.agi` | Config/state API for the settings page |
| `index.html` | Settings & scheduler control panel |
| `prompts/system_prompt.txt` | Main system prompt (verbatim from the workflow) |
| `prompts/search_keywords.txt` | Search-keyword system prompt (verbatim) |
| `prompts/system_prompt_simple.txt` | The workflow's lighter `arozosbot` prompt (reference) |
| `img/icon.svg` | App icon |

The system prompts live as plain-text files so you can edit them without
touching code; `cron.agi` reads them at runtime via the `appdata` library.

## Setup

1. **Configure an AI endpoint** (admin): System Settings ▸ AI Integration ▸ AI
   Model. Point it at any OpenAI-compatible server (LM Studio, Ollama, vLLM,
   OpenAI, OpenRouter, …) and set a default model. The workflow used
   `google/gemma-3-12b` and `qwen/qwen3.5-9b`; set whatever your endpoint serves.
2. **Get cron permission**: the user enabling the bot needs Task Scheduler
   permission. The settings page will tell you if you don't have it.
3. **Open the Telegram Bot app**, paste your **@BotFather** token, and click
   *Test connection* to confirm it works.
4. Tune the trigger keywords, bot `@username`, models, etc., then *Save*.
5. Click **Enable Schedule**. The bot now polls once a minute.

> For groups, give the bot admin rights or disable Telegram's privacy mode in
> @BotFather (`/setprivacy` ▸ Disable) so it can see all messages.

The bot token is stored only in the ArozOS system database (per user) — never in
the repository.

## How the n8n workflow maps to AGI

| n8n node | AGI equivalent |
|----------|----------------|
| Telegram Trigger (webhook) | `getUpdates` long-poll loop in `cron.agi`, driven by the Task Scheduler, with the update offset persisted in the DB |
| Insert row / Get row(s) (data table) | `storeHistory()` / `getHistory()` over the system DB (`writeDBItem`/`listDBItem`) |
| If / If1 / Code in JavaScript (1% chance) | `shouldRespond()` |
| Guardrails | `passesGuardrails()` |
| Message a model1 (keyword gen) | `generateSearchKeywords()` → `aimodel.chat()` |
| DuckDuckGo + Aggregate | `webSearch()` (DuckDuckGo Instant Answer API) |
| Code Tool (current date) | current UTC time injected into the context block |
| Message a model (main) | `generateReply()` → `aimodel.request()` |
| Send a text message | `tgReply()` (HTML, `reply_to_message_id`) |
| Insert row1 | `storeHistory(..., isBot=true)` |
| Message a model2 / Markdown parser (Branch B) | Folded into the main path; the simpler prompt is kept in `prompts/system_prompt_simple.txt` |

## Differences from the original

These are deliberate adaptations to the ArozOS AGI runtime (ES5 / otto):

- **Trigger is polling, not a webhook.** Polling needs no public URL, so it
  works behind NAT on a home server. The Task Scheduler's minimum tick is 60s;
  each tick long-polls (returning instantly when a message arrives), so replies
  are typically fast. An overlap lock prevents concurrent pollers.
- **No native tool-calling.** AGI's `aimodel` has no function-calling, so the
  n8n *Calculator* tool is dropped and the *current date/time* tool is replaced
  by injecting the UTC time into the prompt.
- **Web search** uses DuckDuckGo's Instant Answer JSON API (best-effort, may be
  sparse) instead of the n8n community node. It degrades gracefully — the bot
  still answers if search returns nothing. Toggle it off in settings if unused.
- **Reply formatting** defaults to HTML with a best-effort Markdown→HTML
  conversion and an automatic plain-text fallback if Telegram rejects the
  entities, instead of the MarkdownV2 escaping chain in the original.
- **Single user/owner per bot.** The cron runs as the user who enabled it; all
  DB and history is scoped to that user.

## Safety / limits

- Each tick caps replies (`MAX_RESPONSES`) and respects the 300s VM limit, so a
  message flood cannot run the VM out of time.
- Message history is pruned to the configured window on read.
- Telegram messages are truncated to ~4000 chars.
- Update delivery is at-least-once: a crash mid-message may re-handle at most one
  message.
