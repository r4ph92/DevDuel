-- A player is in at most one unfinished match at a time.
--
-- This is enforced here rather than in Go for the same reason as the
-- invariants in 0001_init.sql: creating a lobby and joining one race, and a
-- check followed by an insert lets two of them through. A unique index on the
-- player, restricted to unfinished memberships, is what lets exactly one win.
--
-- A partial index cannot look at another table, so match_players carries a
-- copy of "is my match unfinished" in its own column. The column is derived,
-- never written by a caller: the triggers below compute it on the way in and
-- recompute it whenever the match changes state.
--
-- Forward only, like the migration runner. The backfill runs under the ALTER
-- TABLE lock, and an existing player already in two unfinished matches aborts
-- the migration rather than having one of them picked silently. Undoing this
-- is a later migration that drops the triggers, the index and the column.

alter table match_players add column unfinished boolean not null default false;

update match_players p set unfinished = m.state in ('lobby', 'active', 'judging')
    from matches m where m.id = p.match_id;

create unique index match_players_one_unfinished on match_players (user_id) where unfinished;

-- Runs on insert, and on an update that names match_id or unfinished, which
-- is how a caller setting unfinished by hand gets overruled. It deliberately
-- does not run on every update: recording a submission or a score must not
-- lock the match row, or a submit racing the deadline would take the player
-- row and then the match while the finalizer takes them the other way round.
--
-- The match row is locked so that an insert cannot read a state the match is
-- in the middle of leaving. Code that changes both rows locks the match first.
create function derive_unfinished_membership() returns trigger language plpgsql as $$
begin
    select state in ('lobby', 'active', 'judging') into new.unfinished
        from matches where id = new.match_id for update;
    return new;
end;
$$;

create trigger match_players_derive_unfinished
    before insert or update of match_id, unfinished on match_players
    for each row execute function derive_unfinished_membership();

-- Every state transition releases or reclaims its players' memberships, so
-- no transition written later has to remember to. The unique index also
-- refuses to bring a finished match back while one of its players has since
-- entered another.
create function sync_unfinished_memberships() returns trigger language plpgsql as $$
begin
    update match_players set unfinished = new.state in ('lobby', 'active', 'judging')
        where match_id = new.id;
    return new;
end;
$$;

create trigger matches_sync_unfinished after update of state on matches
    for each row when (old.state is distinct from new.state)
    execute function sync_unfinished_memberships();
