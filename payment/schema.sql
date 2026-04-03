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

-- Function to deduct credits
create or replace function deduct_credit(user_api_key text)
returns boolean as $$
declare
    current_credits int;
begin
    select credits into current_credits from users where api_key = user_api_key;
    if current_credits is null or current_credits < 1 then
        return false;
    end if;
    update users set credits = credits - 1 where api_key = user_api_key;
    return true;
end;
$$ language plpgsql security definer;

-- Index for fast lookups
create index if not exists idx_users_api_key on users(api_key);
create index if not exists idx_users_email on users(email);
