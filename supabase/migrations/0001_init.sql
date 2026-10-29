-- Sala AI Credits: Phase 1 schema.
-- Run once in Supabase: Dashboard -> SQL Editor -> paste this file -> Run.
--
-- Units: money is stored in fils (1 KWD = 1000 fils). Credits are whole numbers.
-- Every table has RLS on with no policies, and every function is executable
-- only by service_role, so the public anon key cannot read or change anything.
-- Only the Go backend (holding the service key) talks to this schema.

create extension if not exists pgcrypto;

-- ---------------------------------------------------------------------------
-- Tables
-- ---------------------------------------------------------------------------

create table if not exists packs (
  id          text primary key,
  name_en     text not null,
  name_ar     text not null,
  price_fils  integer not null check (price_fils >= 100),
  credits     integer not null check (credits > 0),
  active      boolean not null default true,
  sort        integer not null default 0
);

create table if not exists orders (
  id             uuid primary key default gen_random_uuid(),
  user_id        uuid not null references auth.users(id) on delete restrict,
  pack_id        text not null references packs(id),
  price_fils     integer not null,
  credits        integer not null,
  currency       text not null default 'KWD',
  status         text not null default 'pending'
                 check (status in ('pending', 'paid', 'failed', 'refunded')),
  tap_charge_id  text unique,
  tap_status     text,
  created_at     timestamptz not null default now(),
  paid_at        timestamptz
);
create index if not exists orders_user_idx on orders (user_id, created_at desc);

-- One lot per paid order. Spending draws from the oldest unexpired lot first.
create table if not exists credit_lots (
  id          uuid primary key default gen_random_uuid(),
  user_id     uuid not null references auth.users(id) on delete restrict,
  order_id    uuid unique references orders(id),
  credits     integer not null check (credits > 0),
  remaining   integer not null check (remaining >= 0),
  expires_at  timestamptz not null,
  created_at  timestamptz not null default now()
);
create index if not exists credit_lots_user_idx on credit_lots (user_id, expires_at);

-- Append-only: rows are never updated or deleted. Positive = credits in.
create table if not exists ledger_entries (
  id          bigserial primary key,
  user_id     uuid not null references auth.users(id) on delete restrict,
  amount      integer not null check (amount <> 0),
  kind        text not null
              check (kind in ('purchase', 'usage', 'refund', 'expiry', 'adjustment')),
  order_id    uuid references orders(id),
  lot_id      uuid references credit_lots(id),
  note        text,
  created_at  timestamptz not null default now()
);
create index if not exists ledger_user_idx on ledger_entries (user_id, created_at desc);

create or replace function ledger_entries_immutable() returns trigger
language plpgsql as $$
begin
  raise exception 'ledger_entries is append-only';
end $$;

drop trigger if exists ledger_entries_no_change on ledger_entries;
create trigger ledger_entries_no_change
  before update or delete on ledger_entries
  for each row execute function ledger_entries_immutable();

alter table packs          enable row level security;
alter table orders         enable row level security;
alter table credit_lots    enable row level security;
alter table ledger_entries enable row level security;

-- ---------------------------------------------------------------------------
-- Settings
-- ---------------------------------------------------------------------------

-- Most unexpired credits one account may hold (50 KWD worth).
create or replace function max_balance() returns integer
language sql immutable as $$ select 50000 $$;

-- ---------------------------------------------------------------------------
-- Functions (called by the backend through /rest/v1/rpc/<name>)
-- ---------------------------------------------------------------------------

create or replace function get_balance(p_user uuid) returns integer
language sql stable as $$
  select coalesce(sum(remaining), 0)::integer
  from credit_lots
  where user_id = p_user and expires_at > now();
$$;

-- Creates a pending order, refusing it if the pack would push the account
-- over max_balance().
create or replace function create_order(p_user uuid, p_pack text)
returns orders
language plpgsql as $$
declare
  v_pack packs;
  v_order orders;
begin
  select * into v_pack from packs where id = p_pack and active;
  if not found then
    raise exception 'unknown_pack' using errcode = 'P0001';
  end if;

  if get_balance(p_user) + v_pack.credits > max_balance() then
    raise exception 'balance_cap' using errcode = 'P0001';
  end if;

  insert into orders (user_id, pack_id, price_fils, credits)
  values (p_user, v_pack.id, v_pack.price_fils, v_pack.credits)
  returning * into v_order;
  return v_order;
end $$;

create or replace function attach_charge(p_order uuid, p_charge text, p_tap_status text)
returns void
language sql as $$
  update orders
  set tap_charge_id = p_charge, tap_status = p_tap_status
  where id = p_order and status = 'pending';
$$;

-- Marks an order paid and adds its credits. Safe to call many times for the
-- same charge: only the first call adds credits. The caller must pass the
-- amount and currency it read back from Tap's API, and they must match.
create or replace function fulfill_order(p_charge text, p_amount_fils integer, p_currency text)
returns jsonb
language plpgsql as $$
declare
  v_order orders;
  v_lot_id uuid;
begin
  select * into v_order from orders where tap_charge_id = p_charge for update;
  if not found then
    raise exception 'unknown_charge' using errcode = 'P0001';
  end if;

  if v_order.status = 'paid' then
    return jsonb_build_object('order_id', v_order.id, 'status', 'paid', 'already', true);
  end if;
  if v_order.status <> 'pending' then
    raise exception 'order_not_pending' using errcode = 'P0001';
  end if;
  if p_amount_fils <> v_order.price_fils or p_currency <> v_order.currency then
    raise exception 'amount_mismatch' using errcode = 'P0001';
  end if;

  update orders
  set status = 'paid', tap_status = 'CAPTURED', paid_at = now()
  where id = v_order.id;

  insert into credit_lots (user_id, order_id, credits, remaining, expires_at)
  values (v_order.user_id, v_order.id, v_order.credits, v_order.credits, now() + interval '12 months')
  returning id into v_lot_id;

  insert into ledger_entries (user_id, amount, kind, order_id, lot_id, note)
  values (v_order.user_id, v_order.credits, 'purchase', v_order.id, v_lot_id, v_order.pack_id);

  return jsonb_build_object('order_id', v_order.id, 'status', 'paid', 'already', false);
end $$;

create or replace function fail_order(p_charge text, p_tap_status text)
returns void
language sql as $$
  update orders
  set status = 'failed', tap_status = p_tap_status
  where tap_charge_id = p_charge and status = 'pending';
$$;

-- Zeroes expired lots and records each as an 'expiry' ledger entry.
-- Schedule daily with pg_cron (see README) or run by hand.
create or replace function expire_lots() returns integer
language plpgsql as $$
declare
  v_lot credit_lots;
  v_count integer := 0;
begin
  for v_lot in
    select * from credit_lots
    where expires_at <= now() and remaining > 0
    for update
  loop
    insert into ledger_entries (user_id, amount, kind, lot_id, note)
    values (v_lot.user_id, -v_lot.remaining, 'expiry', v_lot.id, 'expired');
    update credit_lots set remaining = 0 where id = v_lot.id;
    v_count := v_count + 1;
  end loop;
  return v_count;
end $$;

-- ---------------------------------------------------------------------------
-- Lock everything down to service_role
-- ---------------------------------------------------------------------------

revoke all on packs, orders, credit_lots, ledger_entries from anon, authenticated;
revoke all on sequence ledger_entries_id_seq from anon, authenticated;

revoke execute on function get_balance(uuid)                   from public, anon, authenticated;
revoke execute on function create_order(uuid, text)            from public, anon, authenticated;
revoke execute on function attach_charge(uuid, text, text)     from public, anon, authenticated;
revoke execute on function fulfill_order(text, integer, text)  from public, anon, authenticated;
revoke execute on function fail_order(text, text)              from public, anon, authenticated;
revoke execute on function expire_lots()                       from public, anon, authenticated;
revoke execute on function max_balance()                       from public, anon, authenticated;

grant execute on function get_balance(uuid)                   to service_role;
grant execute on function create_order(uuid, text)            to service_role;
grant execute on function attach_charge(uuid, text, text)     to service_role;
grant execute on function fulfill_order(text, integer, text)  to service_role;
grant execute on function fail_order(text, text)              to service_role;
grant execute on function expire_lots()                       to service_role;
grant execute on function max_balance()                       to service_role;

-- ---------------------------------------------------------------------------
-- Launch packs (KWD). Edit prices here; the site reads them from the API.
-- ---------------------------------------------------------------------------

insert into packs (id, name_en, name_ar, price_fils, credits, sort) values
  ('starter', 'Starter', 'المبتدئ',   1000,  1000, 1),
  ('basic',   'Basic',   'الأساسي',   3000,  3000, 2),
  ('plus',    'Plus',    'بلس',       5000,  5250, 3),
  ('pro',     'Pro',     'المحترف',  10000, 10750, 4),
  ('max',     'Max',     'الأقصى',   25000, 27500, 5)
on conflict (id) do nothing;
