-- Supabase schema for Forge payment system

-- Users table
create table if not exists users (
    id uuid primary key,
    email text unique not null,
    stripe_customer_id text,
    credits integer default 5,
    api_key text unique,
    created_at timestamptz default now()
);

-- Enable RLS
alter table users enable row level security;

-- RLS policies
create policy "Users can read own data" on users
    for select using (auth.uid()::text = id);

create policy "Users can update own credits" on users
    for update using (auth.uid()::text = id);

-- Function to add credits (called by Stripe webhook)
create or replace function add_credits(user_email text, amount int)
returns void as $$
begin
    update users
    set credits = credits + amount
    where email = user_email;
end;
$$ language plpgsql security definer;

-- Function to deduct credits (atomic single-UPDATE — no TOCTOU race)
create or replace function deduct_credit(user_api_key text)
returns boolean as $$
declare
    rows_updated int;
begin
    update users set credits = credits - 1
    where api_key = user_api_key and credits >= 1;
    get diagnostics rows_updated = row_count;
    return rows_updated > 0;
end;
$$ language plpgsql security definer;

-- Index for fast lookups
create index if not exists idx_users_api_key on users(api_key);
create index if not exists idx_users_email on users(email);

-- Runs table for per-inference usage tracking
create table if not exists runs (
    id          uuid primary key default gen_random_uuid(),
    user_id     uuid references users(id),
    model       text not null,
    tokens_in   integer default 0,
    tokens_out  integer default 0,
    cost_usd    numeric(12,8) default 0,
    ts          timestamptz default now()
);

create index if not exists idx_runs_user_id on runs(user_id);
create index if not exists idx_runs_ts on runs(ts desc);

-- Stripe event idempotency table (prevents double-credit on webhook retries)
create table if not exists stripe_events (
    event_id     text primary key,
    processed_at timestamptz default now()
);

-- Idempotent credit-add function: inserts event_id first; skips if already seen
create or replace function add_credits_idempotent(
    p_event_id text, p_user_id uuid, p_amount int
) returns boolean as $$
begin
    insert into stripe_events(event_id) values (p_event_id);
    update users set credits = credits + p_amount where id = p_user_id;
    return true;
exception when unique_violation then
    return false;
end;
$$ language plpgsql security definer;
