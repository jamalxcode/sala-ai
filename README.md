# Tip Sala

A one-page tip jar for Sala's free AI apps, in English and Arabic. Visitors pick **0.250, 1, 5 or 10 KWD** and pay on **Tap's** hosted payment page. There's no backend, database or account system, and Sala takes on no obligations for a tip.

- Live: https://jamalxcode.github.io/sala-ai/ (later https://ai.sala.site)
- Hosting: GitHub Pages, built with Hugo by `.github/workflows/pages.yml` on every push to `main`

## Connecting the buttons to Tap

1. In the **Tap dashboard**, create a **payment link** for each amount: 0.250, 1, 5 and 10 KWD. Use a description like "Tip for Sala". If Tap asks for a redirect URL after payment, use `https://ai.sala.site/thanks/` (or `https://jamalxcode.github.io/sala-ai/thanks/` until the domain is set up).
2. Paste each link into [`site/data/tips.toml`](site/data/tips.toml) as the `link` for that amount, then commit. The site redeploys itself in about a minute.

Until a button has a link, it shows "Coming soon" and can't be clicked. To change amounts, edit the same file.

## Pages

| Path | What it is |
|---|---|
| `/` and `/ar/` | Tip buttons |
| `/thanks/` | Where Tap can send people after they pay |
| `/terms/` | Tip terms: voluntary, nothing in return, refunds only for mistakes |
| `/privacy/` | What's collected (by Tap) and kept |

Replace the `[placeholders]` in `site/content/terms.*.md` and `privacy.*.md` with your company name and support email.

## Custom domain
- At your DNS provider, add a CNAME: `ai` → `jamalxcode.github.io`.
- Under GitHub **Settings → Pages**, set the custom domain to `ai.sala.site` and tick **Enforce HTTPS**.

## History
The earlier credits system (Go API, Supabase ledger, AI gateway) was removed on 2026-10-30. It's still in git history: see commit `ec596e1`.
