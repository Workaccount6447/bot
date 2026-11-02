# fsb-bot — Telegram bot (frontend only)

Half 1 of the split project. This repo talks to Telegram users; it does **not** serve files.
Pair it with **fsb-stream** (half 2), which streams/serves the files.

```
user ──▶ fsb-bot ──▶ storage channel (owner's or user's own)
              │                         ▲
              └──▶ MongoDB (shared) ◀───┴── fsb-stream ──▶ browser /stream/<id>?hash=…
```

## What it does
- `/start`, `/myfiles`, `/stats`, `/clearfiles`, admin commands (`/ban /unban /status /broadcast`)
- **Single upload** → one message with Download / Watch / Share links
- **`/multi` … `/done` (multi-link)** → send up to `MAX_BATCH` files, get **all links in one message**
  (auto-split if it's too long for Telegram). `/cancel` aborts and discards.
- **Storage choice**
  - default → **owner channel** (`LOG_CHANNEL`): files + links expire after `FILE_EXPIRY_HOURS` (default 6)
  - `/setchannel` → **user's own channel**: files saved there, **links never expire**
  - `/removechannel` → back to the owner channel, `/storage` shows the current mode
- **Expiry cleaner** (runs every `EXPIRY_CHECK_SEC`): for expired files it
  1. **edits the same message that delivered the links** → "⌛ Link Expired" with the struck-through name and an **expired link** button,
  2. deletes the file (+ log caption) from the owner channel,
  3. deletes the link record from MongoDB.
  Links stop working at `expires_at` even if the cleaner is down — fsb-stream checks the timestamp itself.

## Traffic divider (multiple stream instances)
Run several **fsb-stream** deployments (same DB/secret/token/channel) and list them in the config:
`STREAM_HOST`, `STREAM_HOST2`, `STREAM_HOST3`… (or `STREAM_HOSTS=a,b,c`). Each link is assigned to one host by hashing its ID,
so load spreads evenly, a link always keeps the same host (links in `/myfiles` and the "expired" edit match), and no extra state is stored.
Any instance can serve any link, so removing a host from the list never breaks old links as long as that instance is still running.

## Setup
1. Create the owner channel, add the bot as **admin** (post + delete messages), put its ID in `LOG_CHANNEL`.
2. Copy `fsb.sample.env` → `fsb.env`, fill it in. **`DATABASE_URL`, `HASH_SECRET`, `BOT_TOKEN`, `LOG_CHANNEL` must be identical to fsb-stream.** `STREAM_HOST` = public URL of fsb-stream.
3. `go run ./cmd/fsb run` or `docker build -t fsb-bot . && docker run --env-file fsb.env fsb-bot`.

## User-owned channel — how a user connects it
1. Create a channel, add **this bot** as admin with "Post messages".
2. `/setchannel` → forward any post from the channel to the bot (or `/setchannel @name` / `/setchannel -100…`).
3. The bot checks it can post, saves the channel, done. Links from then on never expire.
The bot never deletes anything in a user's channel (revoke / clear / expiry only touch the owner channel).

## Database (shared with fsb-stream)
`links` documents: `link_id, batch_id, user_id, channel_id, channel_access_hash, custom, message_id, log_msg_id,
file_name, file_size, mime_type, created_at, expires_at (absent = never), notify_*`.
Old records (no `link_id`) keep working as "legacy": same `/stream/<messageID>?hash=` URLs, owner channel, no expiry.

## Notes
- Links are now `STREAM_HOST/stream/<linkID>?hash=<hmac>`; the hash signs the link ID with `HASH_SECRET`.
- This repo has no web server beyond a tiny health endpoint on `PORT` (for hosts that need one).
