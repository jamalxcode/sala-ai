// Sala AI Credits storefront. Talks to the Go API (data-api on <body>) and
// uses Supabase only for sign-in. No secrets live in this file.
(() => {
  const cfg = document.body.dataset;
  const T = JSON.parse(document.getElementById("i18n").textContent);
  const API = (cfg.api || "").replace(/\/$/, "");
  const locale = cfg.lang === "ar" ? "ar-KW" : "en-US";
  const sb = cfg.sbUrl && cfg.sbKey && window.supabase
    ? window.supabase.createClient(cfg.sbUrl, cfg.sbKey)
    : null;

  const $ = (id) => document.getElementById(id);
  const num = (n) => Number(n).toLocaleString(locale);
  const kwd = (fils) => (fils / 1000).toFixed(3) + " " + T.currency;

  function message(text, isError) {
    const el = $("message");
    if (!el) return;
    el.textContent = text;
    el.hidden = false;
    el.classList.toggle("error", !!isError);
  }

  async function accessToken() {
    if (!sb) return null;
    const { data } = await sb.auth.getSession();
    return data.session ? data.session.access_token : null;
  }

  async function api(path, options = {}) {
    const headers = { "Content-Type": "application/json" };
    const token = await accessToken();
    if (token) headers.Authorization = "Bearer " + token;
    const res = await fetch(API + path, { ...options, headers });
    let body = {};
    try { body = await res.json(); } catch (_) { /* empty body */ }
    if (!res.ok) {
      const err = new Error(body.error || "http_" + res.status);
      err.code = body.error;
      throw err;
    }
    return body;
  }

  async function checkout(packId) {
    message(T.redirecting);
    try {
      const { url } = await api("/v1/checkout", {
        method: "POST",
        body: JSON.stringify({ pack_id: packId }),
      });
      window.location.href = url;
    } catch (err) {
      message(err.code === "balance_cap" ? T.balance_cap : T.load_error, true);
    }
  }

  // --- Home: list packs -----------------------------------------------------
  async function home() {
    const box = $("packs");
    try {
      const { packs } = await api("/v1/packs");
      box.replaceChildren(...packs.map((p) => {
        const card = document.createElement("div");
        card.className = "pack";

        const name = document.createElement("h2");
        name.textContent = cfg.lang === "ar" ? p.name_ar : p.name_en;

        const credits = document.createElement("p");
        credits.className = "pack-credits";
        credits.textContent = num(p.credits) + " " + T.credits;

        const bonus = p.credits - p.price_fils;
        const extra = document.createElement("p");
        extra.className = "pack-bonus";
        extra.textContent = bonus > 0 ? "+" + num(bonus) : " ";

        const price = document.createElement("p");
        price.className = "pack-price";
        price.textContent = kwd(p.price_fils);

        const btn = document.createElement("button");
        btn.type = "button";
        btn.textContent = T.buy;
        btn.addEventListener("click", async () => {
          if (!(await accessToken())) {
            window.location.href = cfg.accountUrl + "?buy=" + encodeURIComponent(p.id);
            return;
          }
          checkout(p.id);
        });

        card.append(name, credits, extra, price, btn);
        return card;
      }));
    } catch (_) {
      box.replaceChildren();
      message(T.load_error, true);
    }
    models();
  }

  // --- Home: model price list ----------------------------------------------
  async function models() {
    const box = $("models");
    if (!box) return;
    let data;
    try { ({ data } = await api("/v1/models")); } catch (_) { return; }
    const price = (n) => n.toLocaleString(locale, { maximumFractionDigits: 2 });
    for (const tier of ["fast", "best"]) {
      const rows = data.filter((m) => m.tier === tier);
      if (!rows.length) continue;
      const h = document.createElement("h3");
      h.textContent = T["tier_" + tier];
      const table = document.createElement("table");
      table.className = "ledger";
      const head = table.createTHead().insertRow();
      for (const label of [T.col_model, T.col_input, T.col_output]) {
        const th = document.createElement("th");
        th.textContent = label;
        head.appendChild(th);
      }
      const body = table.createTBody();
      for (const m of rows) {
        const row = body.insertRow();
        row.insertCell().textContent = m.label;
        row.insertCell().textContent = price(m.credits_per_1k_input);
        row.insertCell().textContent = price(m.credits_per_1k_output);
      }
      box.append(h, table);
    }
  }

  // --- Account: sign in, credits, history -----------------------------------
  async function account() {
    if (!sb) { message(T.load_error, true); return; }

    $("login-form").addEventListener("submit", async (e) => {
      e.preventDefault();
      const email = $("email").value.trim();
      const redirect = window.location.origin + window.location.pathname + window.location.search;
      const { error } = await sb.auth.signInWithOtp({ email, options: { emailRedirectTo: redirect } });
      message(error ? T.load_error : T.link_sent, !!error);
    });

    $("sign-out").addEventListener("click", async () => {
      await sb.auth.signOut();
      window.location.reload();
    });

    let rendered = false;
    const render = async (session) => {
      $("login").hidden = !!session;
      $("account").hidden = !session;
      if (!session || rendered) return;
      rendered = true;

      const buy = new URLSearchParams(window.location.search).get("buy");
      if (buy) {
        history.replaceState(null, "", window.location.pathname);
        checkout(buy);
        return;
      }
      try {
        const [me, ledger] = await Promise.all([api("/v1/me"), api("/v1/me/ledger")]);
        $("who").textContent = me.email;
        $("balance").textContent = num(me.balance);
        renderLedger(ledger.entries);
      } catch (_) {
        message(T.load_error, true);
      }
    };

    sb.auth.onAuthStateChange((_event, session) => { render(session); });
    const { data } = await sb.auth.getSession();
    render(data.session);
  }

  function renderLedger(entries) {
    const body = $("ledger");
    if (!entries.length) {
      const row = body.insertRow();
      const cell = row.insertCell();
      cell.colSpan = 3;
      cell.className = "muted";
      cell.textContent = T.history_empty;
      return;
    }
    for (const e of entries) {
      const row = body.insertRow();
      row.insertCell().textContent = new Date(e.created_at).toLocaleDateString(locale);
      const kind = row.insertCell();
      kind.textContent = T["kind_" + e.kind] || e.kind;
      if (e.kind === "usage" && e.note) {
        const model = document.createElement("small");
        model.className = "muted";
        model.textContent = " · " + e.note;
        kind.appendChild(model);
      }
      const amt = row.insertCell();
      amt.className = e.amount > 0 ? "plus" : "minus";
      amt.textContent = (e.amount > 0 ? "+" : "") + num(e.amount);
    }
  }

  // --- Return from Tap: wait for the payment to be confirmed ----------------
  async function paymentReturn() {
    const orderId = new URLSearchParams(window.location.search).get("order");
    const title = $("return-title");
    const done = (text) => { $("spinner").hidden = true; title.textContent = text; };
    if (!orderId || !(await accessToken())) { done(T.return_slow); return; }

    for (let i = 0; i < 30; i++) {
      try {
        const { order, balance } = await api("/v1/orders/" + encodeURIComponent(orderId));
        if (order.status === "paid") {
          done(T.return_paid);
          $("balance").textContent = num(balance);
          $("return-balance").hidden = false;
          return;
        }
        if (order.status === "failed") { done(T.return_failed); return; }
      } catch (_) { /* keep trying */ }
      await new Promise((r) => setTimeout(r, 2000));
    }
    done(T.return_slow);
  }

  const pages = { home, account, return: paymentReturn };
  (pages[cfg.page] || (() => {}))();
})();
