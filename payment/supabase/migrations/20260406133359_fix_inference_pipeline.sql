-- Atomic deduct_credit: single UPDATE with WHERE credits >= 1, no TOCTOU race
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

-- Stripe event idempotency table: prevents double-credit on webhook retries
create table if not exists stripe_events (
    event_id     text primary key,
    processed_at timestamptz default now()
);

-- Idempotent credit-add: inserts event_id first; skips silently if already seen
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
