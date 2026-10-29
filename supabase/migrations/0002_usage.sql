-- Sala AI Credits: Phase 2 schema (AI usage).
-- Run after 0001_init.sql: Dashboard -> SQL Editor -> paste -> Run.
--
-- A request first holds the most it could cost (reserve_credits), then pays
-- the real cost and hands the rest back (settle_hold). Credits come out of
-- the oldest unexpired lot first. 1 credit = 1 fils (0.001 KWD) of price.

-- ---------------------------------------------------------------------------
-- Tables
-- ---------------------------------------------------------------------------

create table if not exists models (
  id                   text primary key,          -- id the apps send, e.g. anthropic/claude-sonnet-5.5
  label                text not null,
  tier                 text not null check (tier in ('fast', 'best')),
  openrouter_id        text not null,
  vercel_id            text,                      -- null = no fallback for this model
  input_usd_per_mtok   numeric(12, 4) not null,   -- our cost, synced from OpenRouter hourly
  output_usd_per_mtok  numeric(12, 4) not null,
  markup_pct           numeric(6, 2) not null default 40,
  max_output_tokens    integer not null default 4096 check (max_output_tokens > 0),
  active               boolean not null default true,
  sort                 integer not null default 0,
  prices_synced_at     timestamptz
);

create table if not exists usage_holds (
  id          uuid primary key default gen_random_uuid(),
  user_id     uuid not null references auth.users(id) on delete restrict,
  credits     integer not null check (credits > 0),
  charged     integer,
  status      text not null default 'held' check (status in ('held', 'settled', 'released')),
  app         text,
  model_id    text,
  created_at  timestamptz not null default now(),
  settled_at  timestamptz
);
create index if not exists usage_holds_open_idx on usage_holds (created_at) where status = 'held';

-- Which lots each hold drew from, so unused credits go back where they came from.
create table if not exists hold_allocations (
  id       bigserial primary key,
  hold_id  uuid not null references usage_holds(id),
  lot_id   uuid not null references credit_lots(id),
  credits  integer not null check (credits > 0)
);
create index if not exists hold_allocations_hold_idx on hold_allocations (hold_id);

create table if not exists usage_events (
  id                 bigserial primary key,
  user_id            uuid not null references auth.users(id) on delete restrict,
  hold_id            uuid unique references usage_holds(id),
  app                text,
  model_id           text,
  provider           text,
  prompt_tokens      integer,
  completion_tokens  integer,
  cost_usd           numeric(14, 8),
  credits            integer not null,
  created_at         timestamptz not null default now()
);
create index if not exists usage_events_user_idx on usage_events (user_id, created_at desc);

alter table models           enable row level security;
alter table usage_holds      enable row level security;
alter table hold_allocations enable row level security;
alter table usage_events     enable row level security;

-- ---------------------------------------------------------------------------
-- Functions
-- ---------------------------------------------------------------------------

-- Holds p_credits from the user's oldest lots. Raises insufficient_credits
-- (and changes nothing) if the user doesn't have enough.
create or replace function reserve_credits(p_user uuid, p_credits integer, p_app text, p_model text)
returns uuid
language plpgsql as $$
declare
  v_hold uuid;
  v_need integer := p_credits;
  v_lot credit_lots;
  v_take integer;
begin
  if p_credits <= 0 then
    raise exception 'bad_amount' using errcode = 'P0001';
  end if;

  insert into usage_holds (user_id, credits, app, model_id)
  values (p_user, p_credits, p_app, p_model)
  returning id into v_hold;

  for v_lot in
    select * from credit_lots
    where user_id = p_user and expires_at > now() and remaining > 0
    order by expires_at, created_at
    for update
  loop
    exit when v_need = 0;
    v_take := least(v_need, v_lot.remaining);
    update credit_lots set remaining = remaining - v_take where id = v_lot.id;
    insert into hold_allocations (hold_id, lot_id, credits) values (v_hold, v_lot.id, v_take);
    v_need := v_need - v_take;
  end loop;

  if v_need > 0 then
    raise exception 'insufficient_credits' using errcode = 'P0001';
  end if;
  return v_hold;
end $$;

-- Returns p_amount credits of a hold to the lots it came from, newest
-- allocation first.
create or replace function return_hold_credits(p_hold uuid, p_amount integer)
returns void
language plpgsql as $$
declare
  v_left integer := p_amount;
  v_alloc hold_allocations;
  v_back integer;
begin
  for v_alloc in select * from hold_allocations where hold_id = p_hold order by id desc loop
    exit when v_left <= 0;
    v_back := least(v_left, v_alloc.credits);
    update credit_lots set remaining = remaining + v_back where id = v_alloc.lot_id;
    v_left := v_left - v_back;
  end loop;
end $$;

-- Charges the real cost of a request (never more than was held), returns the
-- rest and records the usage. Safe to call twice: the second call does nothing.
create or replace function settle_hold(
  p_hold uuid, p_credits integer, p_provider text,
  p_prompt_tokens integer, p_completion_tokens integer, p_cost_usd numeric
) returns integer
language plpgsql as $$
declare
  v_hold usage_holds;
  v_charge integer;
begin
  select * into v_hold from usage_holds where id = p_hold for update;
  if not found then
    raise exception 'unknown_hold' using errcode = 'P0001';
  end if;
  if v_hold.status <> 'held' then
    return coalesce(v_hold.charged, 0);
  end if;

  v_charge := greatest(0, least(p_credits, v_hold.credits));
  perform return_hold_credits(p_hold, v_hold.credits - v_charge);

  update usage_holds
  set status = 'settled', charged = v_charge, settled_at = now()
  where id = p_hold;

  if v_charge > 0 then
    insert into ledger_entries (user_id, amount, kind, note)
    values (v_hold.user_id, -v_charge, 'usage', v_hold.model_id);
  end if;

  insert into usage_events (user_id, hold_id, app, model_id, provider, prompt_tokens, completion_tokens, cost_usd, credits)
  values (v_hold.user_id, p_hold, v_hold.app, v_hold.model_id, p_provider, p_prompt_tokens, p_completion_tokens, p_cost_usd, v_charge);

  return v_charge;
end $$;

-- Gives a whole hold back, e.g. when the AI provider rejected the request.
create or replace function release_hold(p_hold uuid)
returns void
language plpgsql as $$
declare
  v_hold usage_holds;
begin
  select * into v_hold from usage_holds where id = p_hold for update;
  if not found or v_hold.status <> 'held' then
    return;
  end if;
  perform return_hold_credits(p_hold, v_hold.credits);
  update usage_holds set status = 'released', charged = 0, settled_at = now() where id = p_hold;
end $$;

-- Releases holds left open by a crash or restart. The backend runs this
-- every few minutes.
create or replace function release_stale_holds() returns integer
language plpgsql as $$
declare
  v_id uuid;
  v_count integer := 0;
begin
  for v_id in
    select id from usage_holds
    where status = 'held' and created_at < now() - interval '15 minutes'
  loop
    perform release_hold(v_id);
    v_count := v_count + 1;
  end loop;
  return v_count;
end $$;

-- ---------------------------------------------------------------------------
-- Lock everything down to service_role
-- ---------------------------------------------------------------------------

revoke all on models, usage_holds, hold_allocations, usage_events from anon, authenticated;
revoke all on sequence hold_allocations_id_seq, usage_events_id_seq from anon, authenticated;

revoke execute on function reserve_credits(uuid, integer, text, text)                          from public, anon, authenticated;
revoke execute on function return_hold_credits(uuid, integer)                                  from public, anon, authenticated;
revoke execute on function settle_hold(uuid, integer, text, integer, integer, numeric)         from public, anon, authenticated;
revoke execute on function release_hold(uuid)                                                  from public, anon, authenticated;
revoke execute on function release_stale_holds()                                               from public, anon, authenticated;

grant execute on function reserve_credits(uuid, integer, text, text)                          to service_role;
grant execute on function return_hold_credits(uuid, integer)                                  to service_role;
grant execute on function settle_hold(uuid, integer, text, integer, integer, numeric)         to service_role;
grant execute on function release_hold(uuid)                                                  to service_role;
grant execute on function release_stale_holds()                                               to service_role;

-- ---------------------------------------------------------------------------
-- Launch models. Prices are refreshed from OpenRouter every hour by the
-- backend; markup_pct, max_output_tokens and active are yours to edit.
-- ---------------------------------------------------------------------------

insert into models (id, label, tier, openrouter_id, vercel_id, input_usd_per_mtok, output_usd_per_mtok, sort) values
  ('anthropic/claude-haiku-5.5',    'Claude Haiku 5.5',   'fast', 'anthropic/claude-haiku-5.5',    'anthropic/claude-haiku-5.5',   0.10,  0.50, 1),
  ('openai/gpt-6-luna',             'GPT-6 Luna',         'fast', 'openai/gpt-6-luna',             'openai/gpt-6-luna',            0.10,  0.50, 2),
  ('deepseek/deepseek-v4.1-flash',  'DeepSeek V4.1 Flash','fast', 'deepseek/deepseek-v4.1-flash',  null,                           0.045, 0.50, 3),
  ('google/gemini-3.8-flash',       'Gemini 3.8 Flash',   'fast', 'google/gemini-3.8-flash',       'google/gemini-3.8-flash',      0.825, 4.125, 4),
  ('anthropic/claude-sonnet-5.5',   'Claude Sonnet 5.5',  'best', 'anthropic/claude-sonnet-5.5',   'anthropic/claude-sonnet-5.5',  2.00, 10.00, 10),
  ('openai/gpt-6.1-sol',            'GPT-6.1 Sol',        'best', 'openai/gpt-6.1-sol',            'openai/gpt-6.1-sol',           2.00, 10.00, 11),
  ('google/gemini-4-argon',         'Gemini 4 Argon',     'best', 'google/gemini-4-argon',         'google/gemini-4-argon',        2.00, 10.00, 12),
  ('anthropic/claude-opus-5.5',     'Claude Opus 5.5',    'best', 'anthropic/claude-opus-5.5',     'anthropic/claude-opus-5.5',    4.00, 20.00, 13)
on conflict (id) do nothing;
