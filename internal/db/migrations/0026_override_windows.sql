-- §6.6's override is "a SECOND WINDOW where >= 80% of non-abstain participants
-- vote to override". A second window is a second ballot: its own voters, its own
-- window, its own verdict. Until now the only representation of an override was
-- the parent call's `result` column, written by a first-window close -- which
-- made the second window unnecessary and left nowhere for the window's own
-- record to live.
--
-- WHY ITS OWN TABLES AND NOT COLUMNS ON consensus_calls / positions.
--
-- `positions` is keyed (call_id, user_id), so a second ballot on the same call
-- would have to overwrite the first ballot's votes -- the record of who consented
-- to the proposal would be destroyed by the vote on whether to override a block
-- against it. A nullable `window_id` would work in principle and fail in
-- practice: the existing PRIMARY KEY would then allow one row per (NULL, user),
-- and every reader of `positions` in the tree (GetPositions, TallyConsensus, the
-- page) would have to learn a filter it can forget. A separate table makes the
-- forgetting impossible: nothing that reads the first window can see these rows.
--
-- WHY `opened_by` AND NOT A NULLABLE MAKER. The window has exactly one opener and
-- they are the act's author; §6.8 puts "call and close consensus" in the
-- maintainer's column, so this is a maintainer's name on a governance act.
--
-- WHY UNIQUE(call_id). A second window is A window, not a series. Two open
-- override windows on one call would be two mutually unaware ballots deciding the
-- same question, and whichever closed last would win. The UNIQUE constraint is
-- what makes "already has an override window" a refusal the store can produce
-- rather than a race it loses.
--
-- WHY THE CHECK ON result. 'upheld' is a real outcome and the more common one:
-- the block stood. Storing only "overridden or NULL" would make a closed window
-- that did not carry indistinguishable from one still running.
--
-- Positions reuse the four-value vocabulary deliberately (see the CHECK copied
-- from 0001's positions): in an override window consent = override the block and
-- block = uphold it, so one vocabulary covers both ballots and the ratios are
-- computed by one function.

CREATE TABLE override_windows (
    id         INTEGER PRIMARY KEY,
    call_id    INTEGER NOT NULL REFERENCES consensus_calls(id) ON DELETE CASCADE,
    opened_by  INTEGER NOT NULL REFERENCES users(id),
    opened_at  REAL NOT NULL,
    closes_at  REAL NOT NULL,
    status     TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed')),
    closed_at  REAL,
    result     TEXT CHECK (result IN ('overridden','upheld')),
    note       TEXT DEFAULT '',
    UNIQUE (call_id)
);

CREATE INDEX idx_override_windows_call ON override_windows(call_id);

CREATE TABLE override_positions (
    window_id  INTEGER NOT NULL REFERENCES override_windows(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    position   TEXT NOT NULL CHECK (position IN
        ('consent','abstain','stand_aside','block')),
    reason     TEXT DEFAULT '',
    updated_at REAL NOT NULL,
    PRIMARY KEY (window_id, user_id)
);
