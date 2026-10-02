-- Strategic weight becomes a ratified parameter instead of a maintainer dial
-- (spec revision 4 §6.2).
--
-- The steering loophole this closes: §6.2's priority formula multiplies by
-- `strategic_weight` via `Mu`, and the handler required only the maintainer
-- role to change it. So a maintainer could pin a feature to the top of the
-- roadmap by hand, which is exactly what §5.3 forbids -- "no role can steer by
-- hand in collective mode".
--
-- §6.2 replaces the per-feature dial with *strategic themes*: tag-based weights
-- ("security hardening: +2") changed only by consensus, with a public
-- changelog, expiring at the next release cycle unless renewed. Nobody can pin
-- one item by hand; steering happens through public inputs.
--
-- This migration adds the proposal machinery. The feature's own
-- strategic_weight column stays, because the priority formula reads it and
-- rewriting the scoring path is a separate change; what changes is that it is
-- now written only by ratifying a proposal, never by a maintainer directly.

-- A proposal to change a feature's strategic weight. Pending until enough
-- eligible collaborators consent.
--
-- weight_before/weight_after are captured at proposal time so the decision
-- record shows what was being changed and not merely that something changed.
-- A proposal whose target has since been altered by another ratified proposal
-- is stale, and RatifyStrategicWeightProposal rejects it rather than applying
-- two decisions on top of each other.
CREATE TABLE strategic_weight_proposals (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id     INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    feature_id     INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
    proposed_by    INTEGER NOT NULL REFERENCES users(id),
    weight_before  REAL NOT NULL,
    weight_after   REAL NOT NULL,
    rationale      TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending','ratified','rejected','withdrawn','stale')),
    created_at     REAL NOT NULL,
    decided_at     REAL
);

-- The proposal history of one feature. A pending proposal is unique so two
-- people cannot open competing proposals for the same change and neither can be
-- ratified by accident; a ratified or rejected proposal is not unique, because
-- the history is the point.
CREATE UNIQUE INDEX idx_stratweight_one_pending
    ON strategic_weight_proposals(feature_id) WHERE status = 'pending';

CREATE INDEX idx_stratweight_project_status
    ON strategic_weight_proposals(project_id, status);

-- One row per collaborator who consents to a weight proposal.
--
-- A dedicated table rather than counting audit_log rows: audit detail is JSON,
-- so counting consents by proposal id would mean a LIKE over encoded text with
-- no index, and the question is always "how many DISTINCT collaborators", which
-- the audit log cannot answer without parsing it. The audit row is still written
-- on every consent -- this table answers the query, the log remains the record.
--
-- ON CONFLICT DO NOTHING rather than an update: a collaborator re-consenting
-- must not reset their original timestamp, which is part of the record.
CREATE TABLE strategic_weight_consents (
    proposal_id INTEGER NOT NULL REFERENCES strategic_weight_proposals(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    consented_at REAL NOT NULL,
    PRIMARY KEY (proposal_id, user_id)
);

-- Themes: tag-scoped strategic weights, the actual steering mechanism (§6.2).
-- A theme is a tag plus a weight and an expiry. expires_at is NOT NULL because
-- a weight that never expires is permanent aristocracy by another name; §6.2
-- says themes "expire at the next release cycle unless renewed".
--
-- namespace is the tag namespace ("topic", "domain", "platform", "lang", ...)
-- so a theme can be scoped the way §4.3 scopes taxonomy authority.
CREATE TABLE strategic_themes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    tag         TEXT NOT NULL,
    namespace   TEXT NOT NULL DEFAULT 'topic',
    weight      REAL NOT NULL,
    rationale   TEXT NOT NULL DEFAULT '',
    -- Set by consensus, so this is the user id that ratified it.
    set_by      INTEGER NOT NULL REFERENCES users(id),
    created_at  REAL NOT NULL,
    expires_at  REAL NOT NULL
);

-- Themes are changed by consensus too, so a theme change needs the same
-- proposal shape as a weight change. Reusing the table keeps one ratification
-- path rather than two.
CREATE TABLE strategic_theme_proposals (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    theme_tag     TEXT NOT NULL,
    theme_namespace TEXT NOT NULL DEFAULT 'topic',
    weight_after  REAL NOT NULL,
    rationale     TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','ratified','rejected','withdrawn','stale')),
    created_at    REAL NOT NULL,
    decided_at    REAL
);

CREATE UNIQUE INDEX idx_theme_one_pending
    ON strategic_theme_proposals(project_id, theme_tag, theme_namespace)
    WHERE status = 'pending';