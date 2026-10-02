-- 0020_board_cards_feature_unique.sql
--
-- board_cards had no uniqueness on (project_id, feature_id), so placing the same
-- feature twice created a second row and the board grew a duplicate card.
--
-- This is what makes placement idempotent. PlaceFeature upserts on this index,
-- so moving the same feature again updates the row the first move created
-- instead of accumulating rows -- which is what has to hold, because the whole
-- point of moving a derived card is that the move may be repeated.
--
-- NOT a partial index, which was the first attempt and failed: SQLite rejects a
-- partial index as an ON CONFLICT target ("ON CONFLICT clause does not match any
-- PRIMARY KEY or UNIQUE constraint"). The index is therefore unconditional.
--
-- That is safe because feature_id is nullable and NULLs are distinct in a SQLite
-- unique index, so several complaint cards with no feature are unaffected: only
-- rows carrying a feature id are constrained against each other.
--
-- Existing duplicates are collapsed first, keeping the earliest placement per
-- (project_id, feature_id), so the migration is safe against a table that already
-- accumulated them.

DELETE FROM board_cards
WHERE feature_id IS NOT NULL
  AND id NOT IN (
    SELECT MIN(id) FROM board_cards
    WHERE feature_id IS NOT NULL
    GROUP BY project_id, feature_id
  );

CREATE UNIQUE INDEX IF NOT EXISTS board_cards_project_feature_uniq
  ON board_cards (project_id, feature_id);