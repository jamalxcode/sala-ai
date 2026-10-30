# Tip Sala

A one-page tip jar for Sala's free AI apps, in English and Arabic.

Sala's apps, such as how.sala.company, are free. Visitors who like them can leave a **tip** of **0.250, 1, 5 or 10 KWD**. Tap handles the tip securely.

- Live: https://jamalxcode.github.io/sala-ai/ (later https://ai.sala.site)
- Hosting: GitHub Pages, built with Hugo by `.github/workflows/pages.yml` on every push to `main`
- No backend, database, accounts or stored card details

## Tips, not payments

This site takes **tips only**. It doesn't sell anything, so nothing here is a purchase, subscription, top-up or credit balance.

| A tip on this site is… | It is not… |
|---|---|
| A voluntary, one-off thank-you | A payment for a product or service |
| Given with nothing in return | A way to unlock features, support or priority |
| Free to skip: the apps stay free either way | A subscription or recurring charge |
| Refunded only for mistakes (a double charge or wrong amount, within 14 days) | A deposit, wallet or stored credit |
| A gift to Sala the business | A charitable donation |

Sala takes on **no obligation** to anyone who tips. Keep this wording on the site and in Tap. If a page ever starts promising something in return for a tip, it has become a sale, and the terms, refunds and Tap set-up all need to change.

## Connecting the tip buttons to Tap

Tap's dashboard calls these **payment links**. Here, each one is used only to collect a tip of a fixed amount.

1. In the **Tap dashboard**, create one link per tip amount: **0.250**, **1**, **5** and **10 KWD**.
   - **Title / description:** "Tip for Sala" (Arabic: "إكرامية لسالا"). Avoid words like "order", "purchase" or "invoice".
   - **Redirect after paying** (if Tap asks): `https://ai.sala.site/thanks/`, or `https://jamalxcode.github.io/sala-ai/thanks/` until the domain is set up.
2. Paste each link into [`site/data/tips.toml`](site/data/tips.toml) as the `link` for that amount, then commit. The site redeploys itself in about a minute.
3. Tell Tap your account will receive **tips** (voluntary, with nothing in return), so the payments don't look unusual to them.

A tip button with no link shows "Coming soon" and can't be clicked. To change the amounts, edit the same file.

## Pages

| Path | What it is |
|---|---|
| `/` and `/ar/` | The tip buttons |
| `/thanks/` | Thank-you page Tap can send tippers back to |
| `/terms/` | Tip terms: voluntary, nothing in return, refunds only for mistakes |
| `/privacy/` | What Tap collects and what Sala keeps |

Before launch, replace the `[placeholders]` in `site/content/terms.*.md` and `privacy.*.md` with your company name and support email.

## Custom domain
- At your DNS provider, add a CNAME: `ai` → `jamalxcode.github.io`.
- Under GitHub **Settings → Pages**, set the custom domain to `ai.sala.site` and tick **Enforce HTTPS**.

## History
This repo first held a prepaid credits system: a Go API, a Supabase ledger and an AI gateway. It was replaced by this tip page on 2026-10-30 and is still in git history at commit `ec596e1`.
