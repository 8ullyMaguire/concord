//! The S1 store, tested where it runs: against PostgreSQL.
//!
//! These are the tests the mutation gate needs. The Python e2e suites and the parity corpus prove the
//! wire, but `cargo mutants -p concord-db` mutates *these* — and before this file the crate had none,
//! so every mutant survived and the gate answered nothing.
//!
//! Isolation is by slug and username, not by transaction: each test writes rows nothing else reads,
//! because a rolled-back test cannot show that a unique index exists, and unique indexes are exactly
//! what the port got wrong once.

use sqlx::PgPool;

/// The database these tests write to. Required: a suite that quietly skips itself reports green and
/// measures nothing, which is the failure mode this port keeps finding.
async fn pool() -> PgPool {
    let dsn = std::env::var("CONCORD_TEST_DSN").expect(
        "CONCORD_TEST_DSN must point at a scratch PostgreSQL database (see scripts/parity/e2e.sh)",
    );
    PgPool::connect(&dsn).await.expect("connect to CONCORD_TEST_DSN")
}

/// A name no other run has used, so the tests can run against a shared database without colliding.
fn unique(prefix: &str) -> String {
    format!("{prefix}-{}", ulid::Ulid::new().to_string().to_lowercase())
}

/// Register an account and return (id, token).
async fn account(pool: &PgPool) -> (i64, String) {
    let name = unique("t");
    let (user, token) = concord_db::auth::register_user(pool, &name, &name, "correct horse")
        .await
        .expect("register");
    (user.id, token)
}

#[tokio::test]
async fn a_registered_token_names_its_user_and_a_forged_one_names_nobody() {
    let pool = pool().await;
    let (id, token) = account(&pool).await;

    // The token the service hands out must authenticate: `base64-raw(sha256(token))` is how it is
    // stored, and a hash written in another encoding authenticates against neither service.
    assert!(token.starts_with("clt_"), "token must carry the prefix: {token}");
    let found = concord_db::auth::user_for_token(&pool, &token).await.unwrap();
    assert_eq!(found.map(|user| user.id), Some(id));

    let forged = format!("{}x", &token[..token.len() - 1]);
    assert!(concord_db::auth::user_for_token(&pool, &forged).await.unwrap().is_none());

    // The user's role is derived, not stored: the Go store sets it to "member" in GetUser and the
    // schema has no `role` column, so a select on it fails and a default would be a different lie.
    let user = concord_db::auth::user_for_token(&pool, &token).await.unwrap().unwrap();
    assert_eq!(user.role, "member");
    assert!(!user.is_guest);
}

#[tokio::test]
async fn registration_refuses_a_duplicate_a_bad_username_and_a_short_password() {
    let pool = pool().await;
    let name = unique("dup");
    concord_db::auth::register_user(&pool, &name, &name, "correct horse")
        .await
        .expect("first registration");
    match concord_db::auth::register_user(&pool, &name, &name, "correct horse").await {
        Err(concord_db::auth::AuthError::Duplicate(_)) => {}
        other => panic!("expected Duplicate, got {other:?}"),
    }
    for bad in ["Has Caps", "with space", "trailing!", ""] {
        match concord_db::auth::register_user(&pool, bad, bad, "correct horse").await {
            Err(concord_db::auth::AuthError::Invalid(_)) => {}
            other => panic!("expected Invalid for {bad:?}, got {other:?}"),
        }
    }
    match concord_db::auth::register_user(&pool, &unique("pw"), "pw", "short").await {
        Err(concord_db::auth::AuthError::Invalid(_)) => {}
        other => panic!("expected Invalid for a short password, got {other:?}"),
    }
}

#[tokio::test]
async fn creating_a_project_writes_its_charter_nine_columns_and_membership() {
    let pool = pool().await;
    let (user, _) = account(&pool).await;
    let slug = unique("p");
    let project = concord_db::projects::create_project(
        &pool, &slug, "A project", "body", "collective", "", user,
    )
    .await
    .expect("create");
    assert_eq!(project.slug, slug);

    let columns: Vec<(String, Option<i64>)> = sqlx::query_as(
        "select phase, wip_limit from board_columns where project_id = $1 order by position",
    )
    .bind(project.id)
    .fetch_all(&pool)
    .await
    .unwrap();
    let phases: Vec<&str> = columns.iter().map(|row| row.0.as_str()).collect();
    assert_eq!(
        phases,
        [
            "inbox", "triaged", "solution_draft", "consensus", "ready", "in_progress", "review",
            "done", "rejected"
        ],
        "the nine phases, in governance.BoardPhases order"
    );
    // The WIP limits are the charter's, not the column's invention: 3 in progress, 4 in review.
    let wip = |phase: &str| {
        columns
            .iter()
            .find(|row| row.0 == phase)
            .and_then(|row| row.1)
    };
    assert_eq!(wip("in_progress"), Some(3));
    assert_eq!(wip("review"), Some(4));
    assert_eq!(wip("inbox"), None);

    let (merge_quorum, wip_in_progress): (bool, i64) = sqlx::query_as(
        "select merge_requires_quorum, wip_in_progress from charters where project_id = $1",
    )
    .bind(project.id)
    .fetch_one(&pool)
    .await
    .expect("a project without a charter has no rules to read");
    assert!(merge_quorum, "collective is the model that merges on quorum");
    assert_eq!(wip_in_progress, 3);

    let role: String =
        sqlx::query_scalar("select role from members where project_id = $1 and user_id = $2")
            .bind(project.id)
            .bind(user)
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(role, "maintainer");

    // A maintainer-led project is the one that does not need a room to merge.
    let led = concord_db::projects::create_project(
        &pool,
        &unique("m"),
        "Led",
        "",
        "maintainer_led",
        "",
        user,
    )
    .await
    .expect("create maintainer_led");
    let merge_quorum: bool =
        sqlx::query_scalar("select merge_requires_quorum from charters where project_id = $1")
            .bind(led.id)
            .fetch_one(&pool)
            .await
            .unwrap();
    assert!(!merge_quorum);
}

#[tokio::test]
async fn creating_a_project_refuses_a_duplicate_slug_and_a_bad_model() {
    let pool = pool().await;
    let (user, _) = account(&pool).await;
    let slug = unique("dup");
    concord_db::projects::create_project(&pool, &slug, "One", "", "collective", "", user)
        .await
        .expect("first");
    match concord_db::projects::create_project(&pool, &slug, "Two", "", "collective", "", user).await
    {
        Err(concord_db::projects::CreateError::Duplicate(_)) => {}
        other => panic!("expected Duplicate, got {other:?}"),
    }
    match concord_db::projects::create_project(&pool, &unique("x"), "X", "", "anarchy", "", user)
        .await
    {
        Err(concord_db::projects::CreateError::Invalid(_)) => {}
        other => panic!("expected Invalid for an unknown model, got {other:?}"),
    }
    match concord_db::projects::create_project(&pool, "Not A Slug", "X", "", "collective", "", user)
        .await
    {
        Err(concord_db::projects::CreateError::Invalid(_)) => {}
        other => panic!("expected Invalid for a bad slug, got {other:?}"),
    }
}

/// A project with one feature, one user, and the nine default columns.
async fn fixture(pool: &PgPool) -> (i64, i64) {
    let (user, _) = account(pool).await;
    let project = concord_db::projects::create_project(
        pool, &unique("f"), "Fixture", "", "collective", "", user,
    )
    .await
    .expect("create");
    let feature: i64 = sqlx::query_scalar(
        "insert into features (project_id, author_id, title, body, status, created_at, updated_at, \
                                public_id) values ($1, $2, 'a feature', '', 'draft', now(), now(), $3) \
         returning id",
    )
    .bind(project.id)
    .bind(user)
    .bind(ulid::Ulid::new().to_string())
    .fetch_one(pool)
    .await
    .expect("feature");
    (project.id, feature)
}

#[tokio::test]
async fn a_feature_with_no_row_is_placed_by_naming_the_feature() {
    let pool = pool().await;
    let (project, feature) = fixture(&pool).await;
    let before: i64 = sqlx::query_scalar("select count(*) from board_cards where project_id = $1")
        .bind(project)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(before, 0, "a fresh feature has no card: the board derives one");

    concord_db::projects::move_card(&pool, project, 0, feature, "inbox")
        .await
        .expect("place");
    let (column, kind): (String, String) = sqlx::query_as(
        "select c.phase, b.kind from board_cards b join board_columns c on c.id = b.column_id \
         where b.project_id = $1 and b.feature_id = $2",
    )
    .bind(project)
    .bind(feature)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!((column.as_str(), kind.as_str()), ("inbox", "feature"));

    // Placing it again moves the row it created rather than adding a second one.
    concord_db::projects::move_card(&pool, project, 0, feature, "ready")
        .await
        .expect("move");
    let rows: i64 = sqlx::query_scalar("select count(*) from board_cards where feature_id = $1")
        .bind(feature)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(rows, 1);
}

#[tokio::test]
async fn a_wip_limit_refuses_the_next_card_and_allows_the_one_already_there() {
    let pool = pool().await;
    let (project, feature) = fixture(&pool).await;
    // `review` starts at 4; tighten it to 1 so the refusal is reachable in a test.
    sqlx::query("update board_columns set wip_limit = 1 where project_id = $1 and phase = 'review'")
        .bind(project)
        .execute(&pool)
        .await
        .unwrap();

    let second: i64 = sqlx::query_scalar(
        "insert into features (project_id, author_id, title, body, status, created_at, updated_at, \
                                public_id) values ($1, 1, 'another', '', 'draft', now(), now(), $2) \
         returning id",
    )
    .bind(project)
    .bind(ulid::Ulid::new().to_string())
    .fetch_one(&pool)
    .await
    .unwrap();

    concord_db::projects::move_card(&pool, project, 0, feature, "review")
        .await
        .expect("first into an empty column");
    match concord_db::projects::move_card(&pool, project, 0, second, "review").await {
        Err(concord_db::projects::MoveError::ColumnFull(_)) => {}
        other => panic!("expected ColumnFull, got {other:?}"),
    }
    // Re-dropping the card that is already there is not a refusal: otherwise every retry of a
    // successful move reports a full column.
    concord_db::projects::move_card(&pool, project, 0, feature, "review")
        .await
        .expect("the card already in the column");
}

#[tokio::test]
async fn a_card_cannot_be_moved_by_a_name_that_is_not_its_own() {
    let pool = pool().await;
    let (project, feature) = fixture(&pool).await;

    // A feature from another project must not be placeable here.
    let (other, other_feature) = fixture(&pool).await;
    match concord_db::projects::move_card(&pool, other, 0, feature, "inbox").await {
        Err(concord_db::projects::MoveError::NotFound(_)) => {}
        other => panic!("expected NotFound for a foreign feature, got {other:?}"),
    }
    // A phase that names no column.
    match concord_db::projects::move_card(&pool, project, 0, other_feature, "nowhere").await {
        Err(concord_db::projects::MoveError::NoSuchColumn(_)) => {}
        other => panic!("expected NoSuchColumn, got {other:?}"),
    }
    // Naming both a card and a feature is a caller error, not a preference.
    match concord_db::projects::move_card(&pool, project, 7, feature, "inbox").await {
        Err(concord_db::projects::MoveError::Invalid(_)) => {}
        other => panic!("expected Invalid for both ids, got {other:?}"),
    }
}

/// A project with two documents, in the two kinds the board's documents tab shows.
async fn with_documents(pool: &PgPool) -> (i64, String) {
    let (user, _) = account(pool).await;
    let slug = unique("d");
    let project =
        concord_db::projects::create_project(pool, &slug, "Docs", "", "collective", "", user)
            .await
            .expect("create");
    for (kind, name) in [("adr", "known-issues"), ("readme", "about")] {
        sqlx::query(
            "insert into project_documents (project_id, slug, kind, title, body, revision, \
                                            author_id, created_at, updated_at, public_id) \
             values ($1, $2, $3, $4, $5, 1, $6, now(), now(), $7)",
        )
        .bind(project.id)
        .bind(name)
        .bind(kind)
        .bind(format!("{name} title"))
        .bind(format!("body of {name}"))
        .bind(user)
        .bind(ulid::Ulid::new().to_string())
        .execute(pool)
        .await
        .expect("document");
    }
    (project.id, slug)
}

#[tokio::test]
async fn documents_are_listed_by_kind_then_slug_and_one_can_be_fetched_by_its_id() {
    let pool = pool().await;
    let (project, _slug) = with_documents(&pool).await;

    let listed = concord_db::projects::list_documents(&pool, project).await.unwrap();
    let order: Vec<&str> = listed.iter().map(|doc| doc.slug.as_str()).collect();
    assert_eq!(order, ["known-issues", "about"], "order by kind, then slug");
    assert_eq!(listed[0].body, "body of known-issues", "the list carries the body");

    // The single-document read was the route nothing asked about, and it did not work: the column
    // list is written for a bare table and the first version prefixed every entry with `d.`, so
    // `extract(epoch from created_at)` became `d.extract(...)` and PostgreSQL answered
    // "syntax error at or near from". The key is the integer id, as in the Go route.
    let one = concord_db::projects::document(&pool, project, listed[0].id)
        .await
        .expect("a document read must not be a syntax error")
        .expect("the document exists");
    assert_eq!(one.title, "known-issues title");
    assert_eq!(one.kind, "adr");
    assert_eq!(one.body, "body of known-issues");

    // Another project's id is not this project's document.
    let (other, _) = with_documents(&pool).await;
    assert!(concord_db::projects::document(&pool, other, listed[0].id)
        .await
        .unwrap()
        .is_none());
}

#[tokio::test]
async fn an_unplaced_feature_appears_on_the_board_in_the_column_its_status_names() {
    let pool = pool().await;
    let (project, feature) = fixture(&pool).await;
    let slug: String = sqlx::query_scalar("select slug from projects where id = $1")
        .bind(project)
        .fetch_one(&pool)
        .await
        .unwrap();

    let board = concord_db::projects::board_by_slug(&pool, &slug)
        .await
        .unwrap()
        .expect("the project exists");
    assert_eq!(board.columns.len(), 9);
    // `draft` maps to inbox and the feature has no card, so the board derives one: the board shows
    // the work the project has, not the work somebody has placed.
    let derived: Vec<&concord_db::projects::BoardCard> =
        board.cards.iter().filter(|card| card.derived).collect();
    assert_eq!(derived.len(), 1);
    assert_eq!(derived[0].feature_id, feature);
    assert_eq!(derived[0].column, "inbox");
    assert_eq!(derived[0].id, 0, "a derived card has no row");
    assert_eq!(derived[0].title.as_deref(), Some("a feature"));

    // Once it is placed, the derived twin must disappear — otherwise the board shows the same work
    // twice, once draggable and once not.
    concord_db::projects::move_card(&pool, project, 0, feature, "ready")
        .await
        .expect("place");
    let board = concord_db::projects::board_by_slug(&pool, &slug).await.unwrap().unwrap();
    assert_eq!(board.cards.iter().filter(|card| card.derived).count(), 0);
    assert_eq!(board.cards.len(), 1);
    assert_eq!(board.cards[0].column, "ready");

    assert!(concord_db::projects::board_by_slug(&pool, &unique("missing"))
        .await
        .unwrap()
        .is_none());
}

#[tokio::test]
async fn a_card_named_by_an_id_that_is_not_there_is_not_a_success() {
    let pool = pool().await;
    let (project, feature) = fixture(&pool).await;
    // A row that does not exist, in a project that does: the update affects nothing, and reporting
    // that as "moved" is a lie the board cannot tell from a real move.
    match concord_db::projects::move_card(&pool, project, 999_999, 0, "inbox").await {
        Err(concord_db::projects::MoveError::NotFound(_)) => {}
        other => panic!("expected NotFound for an unknown card, got {other:?}"),
    }
    // A card id of zero with no feature id names nothing at all.
    match concord_db::projects::move_card(&pool, project, 0, 0, "inbox").await {
        Err(concord_db::projects::MoveError::Invalid(_)) => {}
        other => panic!("expected Invalid for no card, got {other:?}"),
    }
    // And the feature is still unplaced: a failed move must not have written a card.
    let cards: i64 = sqlx::query_scalar("select count(*) from board_cards where project_id = $1")
        .bind(project)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(cards, 0);

    // A card that does exist moves by its own id.
    concord_db::projects::move_card(&pool, project, 0, feature, "inbox").await.unwrap();
    let card: i64 = sqlx::query_scalar("select id from board_cards where feature_id = $1")
        .bind(feature)
        .fetch_one(&pool)
        .await
        .unwrap();
    concord_db::projects::move_card(&pool, project, card, 0, "triaged")
        .await
        .expect("move by id");
    let phase: String = sqlx::query_scalar(
        "select c.phase from board_cards b join board_columns c on c.id = b.column_id \
         where b.id = $1",
    )
    .bind(card)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(phase, "triaged");
}
/// Every status the Go `featureStatusToPhase` map names, and the phase each one derives into.
///
/// A table, because the mutation run's largest surviving cluster was one deleted match arm at a
/// time: seven statuses with no test between them.
#[test]
fn every_status_derives_the_phase_the_go_map_names() {
    use concord_db::projects::status_phase;
    for (status, phase) in [
        ("draft", "inbox"),
        ("discussion", "triaged"),
        ("consensus", "consensus"),
        ("ready", "ready"),
        ("in_progress", "in_progress"),
        ("review", "review"),
        ("shipped", "done"),
        ("rejected", "rejected"),
    ] {
        assert_eq!(status_phase(status), Some(phase), "status {status}");
    }
    // A status nobody mapped derives nothing, rather than landing in a column that means something
    // else. `inbox` would be the tempting default and it would be a lie about the work.
    assert_eq!(status_phase("archived"), None);
}

/// A whole-second timestamp must serialise without a decimal point, because that is what Go's
/// `float64` encoder prints and the corpus compares bodies.
#[test]
fn a_whole_second_serialises_without_a_decimal_point() {
    use concord_db::projects::BoardCard;
    let card = BoardCard {
        id: 1,
        project_id: 2,
        feature_id: 3,
        column: "inbox".into(),
        entered_at: 1_794_563_910.0,
        column_id: 4,
        title: None,
        derived: false,
    };
    let json = serde_json::to_value(&card).unwrap();
    assert_eq!(
        json["entered_at"].to_string(),
        "1794563910",
        "a whole second is an integer, not 1794563910.0"
    );
    let card = BoardCard { entered_at: 1_794_563_910.5, ..card };
    let json = serde_json::to_value(&card).unwrap();
    assert_eq!(json["entered_at"].to_string(), "1794563910.5");
}

#[tokio::test]
async fn a_password_is_stored_as_argon2id_that_verifies_and_the_token_is_not_stored_in_clear() {
    let pool = pool().await;
    let name = unique("pw");
    let (user, token) = concord_db::auth::register_user(&pool, &name, &name, "correct horse")
        .await
        .expect("register");

    let stored: String = sqlx::query_scalar("select password_hash from users where id = $1")
        .bind(user.id)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert!(stored.starts_with("$argon2id$"), "not an argon2id PHC string: {stored}");
    // Verifying is the assertion a hash function cannot fake: `String::new()` or a constant is a
    // stored value that no password matches, and it is what the mutation run substituted.
    use argon2::password_hash::{PasswordHash, PasswordVerifier};
    let parsed = PasswordHash::new(&stored).expect("a parsable PHC string");
    argon2::Argon2::default()
        .verify_password(b"correct horse", &parsed)
        .expect("the password must verify against what was stored");
    assert!(argon2::Argon2::default()
        .verify_password(b"wrong horse", &parsed)
        .is_err());

    // The plaintext token is returned once and only its hash is kept.
    let stored_tokens: Vec<String> =
        sqlx::query_scalar("select token_hash from api_tokens where user_id = $1")
            .bind(user.id)
            .fetch_all(&pool)
            .await
            .unwrap();
    assert_eq!(stored_tokens.len(), 1);
    assert_ne!(stored_tokens[0], token, "the token itself must not be in the database");
    assert!(!stored_tokens[0].starts_with("clt_"));
}

#[tokio::test]
async fn the_password_rule_stops_at_eight_and_at_two_hundred_characters() {
    let pool = pool().await;
    // Eight is enough, seven is not; two hundred is enough, two hundred and one is not. The bounds
    // are inclusive on the inside, which is a comparison a mutant flips silently.
    for (password, accepted) in [
        ("12345678", true),
        ("1234567", false),
        (&"a".repeat(200), true),
        (&"a".repeat(201), false),
    ] {
        let result =
            concord_db::auth::register_user(&pool, &unique("len"), "len", password).await;
        match (result, accepted) {
            (Ok(_), true) => {}
            (Err(concord_db::auth::AuthError::Invalid(_)), false) => {}
            (other, want) => panic!("password of {} chars: expected accepted={want}, got {other:?}",
                                    password.len()),
        }
    }
}

#[tokio::test]
async fn listing_projects_finds_one_that_was_just_created() {
    let pool = pool().await;
    let (user, _) = account(&pool).await;
    let slug = unique("l");
    concord_db::projects::create_project(&pool, &slug, "Listed", "", "collective", "", user)
        .await
        .expect("create");
    let listed = concord_db::projects::list_projects(&pool).await.unwrap();
    assert!(
        listed.iter().any(|project| project.slug == slug),
        "a project that exists must be listed; an empty list is a service that answers 200 and \
         shows nothing"
    );
}

#[tokio::test]
async fn a_database_error_that_is_not_a_duplicate_is_not_reported_as_one() {
    let pool = pool().await;
    // A user id that does not exist makes `members` refuse on the foreign key, which is a database
    // error and not a duplicate. Reporting it as Duplicate would tell a caller "that name is taken"
    // about a failure that has nothing to do with the name.
    match concord_db::projects::create_project(
        &pool,
        &unique("fk"),
        "Orphan",
        "",
        "collective",
        "",
        999_999_999,
    )
    .await
    {
        Err(concord_db::projects::CreateError::Database(_)) => {}
        other => panic!("expected Database, got {other:?}"),
    }
}
/// The user shape's own serializer: same rule as a project's, a different function.
#[test]
fn a_users_created_at_serialises_the_same_way_a_projects_does() {
    let user = concord_db::auth::User {
        id: 1,
        username: "someone".into(),
        display_name: "Someone".into(),
        role: "member".into(),
        created_at: 1_794_563_910.0,
        is_guest: false,
    };
    let json = serde_json::to_value(&user).unwrap();
    assert_eq!(json["created_at"].to_string(), "1794563910");
    let user = concord_db::auth::User { created_at: 1_794_563_910.75, ..user };
    assert_eq!(
        serde_json::to_value(&user).unwrap()["created_at"].to_string(),
        "1794563910.75"
    );
}

/// The parameters are in the string that is stored, and a verifier on the other side reads them: a
/// wrong `m` is a hash that still verifies and is no longer the same work factor.
#[tokio::test]
async fn a_stored_hash_carries_the_go_stores_argon2_parameters() {
    let pool = pool().await;
    let name = unique("par");
    let (user, _) = concord_db::auth::register_user(&pool, &name, &name, "correct horse")
        .await
        .expect("register");
    let stored: String = sqlx::query_scalar("select password_hash from users where id = $1")
        .bind(user.id)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert!(
        stored.contains("m=65536,t=1,p=2"),
        "the PHC string must carry the Go store's parameters: {stored}"
    );
}

#[tokio::test]
async fn placing_a_feature_that_already_sits_in_a_full_column_is_not_a_refusal() {
    let pool = pool().await;
    let (project, first) = fixture(&pool).await;
    sqlx::query("update board_columns set wip_limit = 1 where project_id = $1 and phase = 'review'")
        .bind(project)
        .execute(&pool)
        .await
        .unwrap();
    let second: i64 = sqlx::query_scalar(
        "insert into features (project_id, author_id, title, body, status, created_at, updated_at, \
                                public_id) values ($1, 1, 'second', '', 'draft', now(), now(), $2) \
         returning id",
    )
    .bind(project)
    .bind(ulid::Ulid::new().to_string())
    .fetch_one(&pool)
    .await
    .unwrap();

    // `first` goes into review and fills it; `second` is then refused, which is the branch that
    // needs the card's *column* and not its id — the first version compared a card id to a column
    // id, so the branch was unreachable.
    concord_db::projects::move_card(&pool, project, 0, first, "review").await.unwrap();
    match concord_db::projects::move_card(&pool, project, 0, second, "review").await {
        Err(concord_db::projects::MoveError::ColumnFull(_)) => {}
        other => panic!("expected ColumnFull from a different column, got {other:?}"),
    }
    // Re-placing the feature that is already in review is a no-op, not a refusal.
    concord_db::projects::move_card(&pool, project, 0, first, "review")
        .await
        .expect("already in that column");

    // And a feature in an unlimited column can still move in only after review has room: `second`
    // must not be able to slip in by first being placed somewhere else.
    concord_db::projects::move_card(&pool, project, 0, second, "inbox").await.unwrap();
    match concord_db::projects::move_card(&pool, project, 0, second, "review").await {
        Err(concord_db::projects::MoveError::ColumnFull(_)) => {}
        other => panic!("expected ColumnFull when moving in from another column, got {other:?}"),
    }
}
/// A byte PostgreSQL cannot store is a database error, not a duplicate.
///
/// The duplicate guard turns one specific code (`23505`) into `Duplicate`, and everything else into
/// `Database`. A mutant that makes the guard `true` reports "that name is taken" for a failure that
/// has nothing to do with the name, so this asserts the other side of the guard with an error that
/// is reachable: a NUL byte is `22021 invalid byte sequence`.
#[tokio::test]
async fn a_byte_the_database_refuses_is_not_reported_as_a_duplicate() {
    let pool = pool().await;
    let name = unique("nul");
    match concord_db::auth::register_user(&pool, &name, "bad\u{0}name", "correct horse").await {
        Err(concord_db::auth::AuthError::Duplicate(_)) => {
            panic!("a NUL byte is not a duplicate username")
        }
        Err(concord_db::auth::AuthError::Database(_)) => {}
        other => panic!("expected Database, got {other:?}"),
    }
    let (user, _) = account(&pool).await;
    match concord_db::projects::create_project(
        &pool,
        &unique("nul"),
        "A project",
        "body\u{0}with a NUL",
        "collective",
        "",
        user,
    )
    .await
    {
        Err(concord_db::projects::CreateError::Duplicate(_)) => {
            panic!("a NUL byte is not a duplicate slug")
        }
        Err(concord_db::projects::CreateError::Database(_)) => {}
        other => panic!("expected Database, got {other:?}"),
    }
}

/// The integral guard has a ceiling, and above it the value stays a float.
///
/// `value.abs() < 9.007e15` is the point past which an `f64` no longer has integer precision, so a
/// timestamp that large is written as a float rather than silently rounded into an integer.
#[test]
fn a_timestamp_past_the_integer_ceiling_stays_a_float() {
    use concord_db::projects::BoardCard;
    let card = BoardCard {
        id: 1,
        project_id: 2,
        feature_id: 3,
        column: "inbox".into(),
        entered_at: 9.007_199_254_740_992e15,
        column_id: 4,
        title: None,
        derived: false,
    };
    let json = serde_json::to_value(&card).unwrap();
    assert_eq!(
        json["entered_at"].to_string(),
        "9007199254740992.0",
        "at the ceiling the value keeps its float form; rounding it is the bug this guard prevents"
    );
}

/// A card that is already in a full column can be dropped there again by its own id.
///
/// This is the `already > 0` guard in `check_wip`, which the board reaches by dragging a card that
/// is already at its destination. Without the guard, a retry reports a full column and the board
/// looks broken for the one action that changed nothing.
#[tokio::test]
async fn dropping_a_card_into_the_full_column_it_occupies_is_not_refused() {
    let pool = pool().await;
    let (project, feature) = fixture(&pool).await;
    sqlx::query("update board_columns set wip_limit = 1 where project_id = $1 and phase = 'review'")
        .bind(project)
        .execute(&pool)
        .await
        .unwrap();
    concord_db::projects::move_card(&pool, project, 0, feature, "review")
        .await
        .expect("place into an empty column");
    let card: i64 = sqlx::query_scalar("select id from board_cards where feature_id = $1")
        .bind(feature)
        .fetch_one(&pool)
        .await
        .unwrap();

    concord_db::projects::move_card(&pool, project, card, 0, "review")
        .await
        .expect("a card dropped where it already is");
}
