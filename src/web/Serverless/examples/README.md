# Daily Reminder — serverless example

`daily_reminder.agi` is a single-file [AGI serverless](../../../mod/agi/README.md)
port of an n8n "Daily reminder" workflow. It fetches one or more Google
Calendar `.ics` feeds, asks the configured LLM to pull out the 5-7 most
important upcoming items, and pushes the result to a Telegram chat as HTML.

```
fetch .ics feeds  ->  llm.chat() summary  ->  markdown->HTML  ->  Telegram sendMessage
```

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
| `useLLM` | `true` summarises events with the LLM; `false` sends the raw formatted list (handy when the AI endpoint is slow/unavailable). |
| `model` | LLM model override; `""` uses the admin-configured default. |
| `lookaheadDays` | How many days ahead to include (default `7`). |
| `tzOffsetHours` | Timezone offset (default `9` = Asia/Tokyo). |
| `calendars` | Array of `{ name, url }`. Paste your **private** personal `.ics` URL into the `"Personal"` entry (replace the `PUT_YOUR_PRIVATE_ICS_URL_HERE` placeholder). US + Japan public holiday feeds are pre-filled. |

Secrets (bot token, private calendar URL) live in this config file, **not** in
the committed script, so the endpoint stays shareable while your credentials do
not.

## Run on a schedule

Point any scheduler at the endpoint URL — the n8n Schedule Trigger you already
have, a cron job, or ArozOS's own scheduler. The original workflow ran at
`0 14 * * 1-4,7`.

## Test without sending

Add query parameters when calling the endpoint:

- `?dryRun=1` — build the message and return it as JSON, but do **not** send to Telegram.
- `?lookahead=14` — widen the window for one call.
- `?useLLM=0` — skip the LLM for this call and send the raw event list.
- `?token=...`, `?chat_id=...`, `?model=...` — override config per request.

If the LLM endpoint times out, the script logs the error and automatically
falls back to sending the raw event list, so a reminder still goes out.

## Supported iCalendar features

- Line unfolding (RFC 5545), `SUMMARY` / `DTSTART` / `LOCATION` / `RRULE` / `EXDATE`.
- All-day (`VALUE=DATE`), UTC (`...Z`) and local/`TZID` date-times (interpreted in `tzOffsetHours`).
- Recurring events: `FREQ=DAILY/WEEKLY/MONTHLY/YEARLY` with `INTERVAL`, `COUNT`,
  `UNTIL`, weekly `BYDAY`, and `EXDATE` exclusions — expanded within the
  lookahead window only.
