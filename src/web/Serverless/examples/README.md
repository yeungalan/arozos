# Daily Reminder — serverless example

`daily_reminder.agi` is a single-file [AGI serverless](../../../mod/agi/README.md)
port of an n8n "Daily reminder" workflow. It gathers upcoming items from two
sources — one or more Google Calendar `.ics` feeds and (optionally) your Trello
to-do cards — asks the configured LLM to pull out the 5-7 most important /
urgent ones, and pushes the result to a Telegram chat as HTML.

```
fetch .ics feeds + Trello cards  ->  llm.chat() summary  ->  markdown->HTML  ->  Telegram sendMessage
```

Overdue Trello cards are kept and the model is told today's date, so it can flag
them (e.g. "已延遲2天"), matching the original workflow's behaviour.

## Install

1. Copy `daily_reminder.agi` into your ArozOS storage (e.g.
   `user:/Desktop/daily_reminder.agi`).
2. Open the **Serverless** web app and register that file as an endpoint. You
   get a URL like `https://<host>/api/remote/<uuid>`.
3. Hit the URL once. On the first run the script has no config yet, so it
   writes a template to `user:/.appdata/DailyReminder/config.json` and returns
   an error telling you to fill it in.

## Configure

Edit `user:/.appdata/DailyReminder/config.json`:

| Field | Meaning |
|-------|---------|
| `telegramToken` | Your BotFather token (kept out of source control — set it here). |
| `chatId` | Target chat / channel id (defaults to the original workflow's `-1001392519602`). |
| `model` | LLM model override; `""` uses the admin-configured default. |
| `lookaheadDays` | How many days ahead to include (default `7`). |
| `tzOffsetHours` | Timezone offset (default `9` = Asia/Tokyo). |
| `calendars` | Array of `{ name, url }`. Paste your **private** personal `.ics` URL into the `"Personal"` entry (replace the `PUT_YOUR_PRIVATE_ICS_URL_HERE` placeholder). US + Japan public holiday feeds are pre-filled. |
| `trello` | Trello to-do source (see below). |

### Trello

The `trello` block pulls open cards from Trello and merges them with the
calendar items:

```json
"trello": {
    "enabled": false,
    "apiKey": "",
    "token": "",
    "boardIds": [],
    "includeNoDueDate": false
}
```

- Set `enabled: true` and fill `apiKey` + `token` (get both from
  <https://trello.com/app-key> — the token via the "Token" link there).
- `boardIds` — optional list of board ids to read; leave empty to pull the cards
  assigned to you (`/members/me/cards`).
- Cards already marked done (`dueComplete`) are skipped. Cards with a due date
  within the window — **including overdue ones** — are summarised alongside
  calendar events. Set `includeNoDueDate: true` to also list open cards that
  have no due date (shown as `(無期限)`).

Secrets (bot token, private calendar URL, Trello key/token) live in this config
file, **not** in the committed script, so the endpoint stays shareable while
your credentials do not.

## Run on a schedule

Point any scheduler at the endpoint URL — the n8n Schedule Trigger you already
have, a cron job, or ArozOS's own scheduler. The original workflow ran at
`0 14 * * 1-4,7`.

## Test without sending

Add query parameters when calling the endpoint:

- `?dryRun=1` — build the message and return it as JSON, but do **not** send to Telegram.
- `?lookahead=14` — widen the window for one call.
- `?token=...`, `?chat_id=...`, `?model=...` — override config per request.

## Supported iCalendar features

- Line unfolding (RFC 5545), `SUMMARY` / `DTSTART` / `LOCATION` / `RRULE` / `EXDATE`.
- All-day (`VALUE=DATE`), UTC (`...Z`) and local/`TZID` date-times (interpreted in `tzOffsetHours`).
- Recurring events: `FREQ=DAILY/WEEKLY/MONTHLY/YEARLY` with `INTERVAL`, `COUNT`,
  `UNTIL`, weekly `BYDAY`, and `EXDATE` exclusions — expanded within the
  lookahead window only.
