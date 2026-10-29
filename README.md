# Sala AI Credits

Prepaid credits for Sala's own AI apps (how.sala.company and others), sold at **ai.sala.site** and paid through **Tap**.

Credits are prepaid usage packs for Sala's services only. They are not a wallet or e-money: they can't be cashed out or transferred, they work only in apps run by Sala's legal entity, and they expire 12 months after purchase.

## What's here (Phase 1)

| Folder | What it is | Where it runs |
|---|---|---|
| `site/` | Hugo storefront in English and Arabic: packs, sign-in, credits, payment status, draft legal pages | GitHub Pages |
| `backend/` | Go API (standard library only): packs, Tap checkout and webhook, credits and history | Render |
| `supabase/migrations/` | Postgres schema and ledger functions | Supabase |
| `.github/workflows/` | `ci.yml` runs the Go tests and builds the site; `pages.yml` deploys the site | GitHub Actions |
| `render.yaml` | Render Blueprint for the API | Render |

Supabase provides both login and the database, so Neon isn't needed.

## How a purchase works

1. The user signs in with an emailed link (Supabase Auth) and picks a pack.
2. `POST /v1/checkout` creates a pending order, then a Tap charge, and returns Tap's hosted payment page.
3. After paying, Tap sends the user to `/checkout/return/?order=…` and posts the charge to `/webhooks/tap`.
4. The backend **fetches the charge from Tap's API** with the secret key. Only if it's `CAPTURED`, with the same amount and currency as the order, does `fulfill_order` add the credits. This is safe to repeat: a charge can only add credits once.
5. The return page polls `GET /v1/orders/{id}` until the order is paid. Each poll re-checks with Tap, so credits arrive even if the webhook is late.

## API

| Method | Path | Login | Purpose |
|---|---|---|---|
| GET | `/healthz` | – | Render health check |
| GET | `/v1/packs` | – | Active packs and prices (fils) |
| GET | `/v1/me` | ✓ | Email and unexpired credits |
| GET | `/v1/me/ledger` | ✓ | Last 100 credit movements |
| POST | `/v1/checkout` | ✓ | `{"pack_id":"plus"}` → `{"order_id","url"}` |
| GET | `/v1/orders/{id}` | ✓ | Order status and credits (re-checks Tap while pending) |
| POST | `/webhooks/tap` | Tap | Charge updates from Tap |

"Login" means the request sends `Authorization: Bearer <Supabase access token>`.

## Setup (all in the browser)

### 1. Supabase
1. Create a project at supabase.com (region: closest to Kuwait, e.g. Frankfurt or Mumbai).
2. Open **SQL Editor**, paste [`supabase/migrations/0001_init.sql`](supabase/migrations/0001_init.sql), then click **Run**.
3. Under **Authentication → URL Configuration**, set Site URL to `https://ai.sala.site`. Add these redirect URLs: `https://ai.sala.site/**` and `https://jamalxcode.github.io/**`.
4. Under **Authentication → Providers → Email**, keep Email on (sign-in links).
5. Copy from **Project Settings → API**: the Project URL, the **anon/publishable** key and the **service_role/secret** key.
6. Optional: expire old credits daily. Enable the `pg_cron` extension, then run:
   `select cron.schedule('expire-credits', '0 1 * * *', $$select expire_lots()$$);`

### 2. Render (API)
1. **New → Blueprint**, then pick this repo. Render reads `render.yaml`.
2. Enter the secret values: `SUPABASE_URL`, `SUPABASE_ANON_KEY`, `SUPABASE_SERVICE_KEY` and `TAP_SECRET_KEY` (start with `sk_test_…`).
3. Until `api.sala.site` points at Render, set `API_URL` to the `https://….onrender.com` address. Tap's webhook goes to `API_URL/webhooks/tap`.
4. Check that `https://<render-url>/healthz` returns `{"ok":true}`.

### 3. GitHub Pages (site)
1. **Settings → Pages → Source: GitHub Actions.**
2. **Settings → Secrets and variables → Actions → Variables**: add `SUPABASE_URL`, `SUPABASE_ANON_KEY` and `API_BASE` (the Render URL, or `https://api.sala.site` once DNS is set). None of these are secret.
3. Re-run **Deploy site to GitHub Pages** from the Actions tab.

### 4. Domains
- `ai` CNAME → `jamalxcode.github.io`. Then add `ai.sala.site` as the custom domain under Settings → Pages and tick Enforce HTTPS.
- `api` CNAME → your Render host. Then add `api.sala.site` under Render → Settings → Custom Domains.
- After both work, set `API_URL=https://api.sala.site` in Render and `API_BASE=https://api.sala.site` in GitHub.

### 5. Test a payment
Buy the Starter pack with a [Tap test card](https://developers.tap.company/docs/test-cards). Credits should appear only once Tap reports `CAPTURED`. Check Render's logs for `order … paid` and for any `hashstring mismatch` warnings.

## Prices
Packs live in the `packs` table (prices in fils, 1 KWD = 1000 fils). Change them in Supabase's table editor; the site reads them from the API. The 50,000-credit cap is set in `max_balance()` in the SQL and in `maxBalance` in `backend/handlers.go`.

## Before going live
- [ ] Written confirmation from a Kuwaiti lawyer or the CBK that single-entity, non-cashable packs need no e-money licence or NOC
- [ ] Tap told what you sell; live keys issued
- [ ] Legal pages finished: replace the `[placeholders]` and remove the draft notices
- [ ] Ministry of Commerce licence covers e-commerce / IT services

## Next (Phase 2)
AI Gateway: an OpenAI-compatible `/v1/chat/completions` that reserves credits, calls OpenRouter (Vercel AI Gateway as the backup), then charges the real token cost plus markup.
