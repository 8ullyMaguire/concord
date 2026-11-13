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

/// A project with `both` intake, a validated complaint and two draft features, one blocking the other.
async fn writable(pool: &PgPool) -> (i64, i64, i64, i64, i64) {
    use sqlx::Row;
    let (user, _) = account(pool).await;
    let project = concord_db::projects::create_project(
        pool, &unique("w"), "Writable", "", "collective", "", user,
    )
    .await
    .expect("create");
    sqlx::query("update projects set intake_mode = 'both' where id = $1")
        .bind(project.id)
        .execute(pool)
        .await
        .unwrap();
    let complaint: i64 = sqlx::query_scalar(
        "insert into complaints (project_id, author_id, title, body, severity, frequency, \
                                 strategic_multiplier, status, created_at, updated_at, public_id) \
         values ($1, $2, 'evidence', '', 2, 1.0, 1.0, 'validated', now(), now(), $3) returning id",
    )
    .bind(project.id)
    .bind(user)
    .bind(ulid::Ulid::new().to_string())
    .fetch_one(pool)
    .await
    .expect("complaint");
    let blocker = concord_db::feature_writes::create_feature(
        pool, project.id, user, "blocker", "", "", None, None, &[],
    )
    .await
    .expect("blocker");
    let blocked = concord_db::feature_writes::create_feature(
        pool, project.id, user, "blocked", "", "", None, None, &[],
    )
    .await
    .expect("blocked");
    sqlx::query("insert into feature_deps (blocked_id, blocker_id) values ($1, $2)")
        .bind(blocked.id)
        .bind(blocker.id)
        .execute(pool)
        .await
        .unwrap();
    let row = sqlx::query("select 1").fetch_one(pool).await.unwrap();
    let _ = row.get::<i32, _>(0);
    (user, project.id, complaint, blocker.id, blocked.id)
}

/// The evidence gate: `ready` needs evidence, and the refusal says what would count.
#[tokio::test]
async fn a_feature_reaches_ready_only_with_evidence() {
    let pool = pool().await;
    let (user, project, complaint, _, _) = writable(&pool).await;
    let feature = concord_db::feature_writes::create_feature(
        &pool, project, user, "an idea", "", "", None, None, &[],
    )
    .await
    .expect("file");

    match concord_db::feature_writes::update_feature_status(&pool, feature.id, "ready").await {
        Err(concord_db::feature_writes::WriteError::Invalid(message)) => {
            assert!(
                message.contains("evidence"),
                "the refusal must name the evidence, got: {message}"
            );
        }
        other => panic!("expected Invalid, got {other:?}"),
    }

    // The complaint is evidence, and it must be validated to be linked at all.
    concord_db::feature_writes::link_complaint(&pool, feature.id, complaint)
        .await
        .expect("link");
    concord_db::feature_writes::update_feature_status(&pool, feature.id, "ready")
        .await
        .expect("ready with evidence");
    let status: String = sqlx::query_scalar("select status from features where id = $1")
        .bind(feature.id)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(status, "ready");
}

/// §46's hole: evidence removed after `ready` must still stop `in_progress`.
#[tokio::test]
async fn starting_work_rechecks_the_evidence_that_ready_accepted() {
    let pool = pool().await;
    let (user, project, complaint, _, _) = writable(&pool).await;
    let feature = concord_db::feature_writes::create_feature(
        &pool, project, user, "backed", "", "", None, None, &[complaint],
    )
    .await
    .expect("file with evidence");
    concord_db::feature_writes::update_feature_status(&pool, feature.id, "ready")
        .await
        .expect("ready");

    // Unlink does not un-ready — that policy is deliberate, so an edit cannot silently undo a
    // decision — which is exactly why the start of work has to look again.
    concord_db::feature_writes::unlink_complaint(&pool, feature.id, complaint)
        .await
        .expect("unlink");
    match concord_db::feature_writes::update_feature_status(&pool, feature.id, "in_progress").await {
        Err(concord_db::feature_writes::WriteError::Invalid(message)) => {
            assert!(
                message.contains("evidence"),
                "the refusal must name the evidence, got: {message}"
            );
        }
        other => panic!("expected Invalid after the evidence was removed, got {other:?}"),
    }
}

/// The dependency refusal, with the blocker named rather than a bare "invalid".
#[tokio::test]
async fn work_does_not_start_while_a_blocker_is_open() {
    let pool = pool().await;
    let (_, project, complaint, blocker, blocked) = writable(&pool).await;
    for feature in [blocker, blocked] {
        concord_db::feature_writes::link_complaint(&pool, feature, complaint)
            .await
            .expect("link");
        concord_db::feature_writes::update_feature_status(&pool, feature, "ready")
            .await
            .expect("ready");
    }
    match concord_db::feature_writes::update_feature_status(&pool, blocked, "in_progress").await {
        Err(concord_db::feature_writes::WriteError::Invalid(message)) => {
            assert!(message.contains("blocked by"), "got: {message}");
            assert!(
                message.contains(&format!("#{blocker}")),
                "the refusal must name the blocker, got: {message}"
            );
        }
        other => panic!("expected Invalid with a blocker, got {other:?}"),
    }
    // Ship the blocker and the way opens — the rule is about open blockers, not about dependencies
    // existing at all.
    for status in ["in_progress", "shipped"] {
        concord_db::feature_writes::update_feature_status(&pool, blocker, status)
            .await
            .expect("move the blocker along");
    }
    // The dependency is still in the table; the blocker's status is what the rule reads: only
    // `shipped` is closed, so `review` would still block — which is the Go query's own definition.
    let _ = project;
    match concord_db::feature_writes::update_feature_status(&pool, blocked, "in_progress").await {
        Ok(()) => {}
        Err(error) => panic!("a shipped blocker must open the way, got {error:?}"),
    }
}

/// Filing: a `complaint_first` project refuses a feature with no complaint, and an unvalidated
/// complaint is not evidence.
#[tokio::test]
async fn filing_obeys_the_projects_intake_mode_and_the_validated_rule() {
    let pool = pool().await;
    let (user, project, _, _, _) = writable(&pool).await;
    sqlx::query("update projects set intake_mode = 'complaint_first' where id = $1")
        .bind(project)
        .execute(&pool)
        .await
        .unwrap();
    match concord_db::feature_writes::create_feature(
        &pool, project, user, "unbacked", "", "", None, None, &[],
    )
    .await
    {
        Err(concord_db::feature_writes::WriteError::Invalid(message)) => {
            assert!(message.contains("complaint_first"), "got: {message}")
        }
        other => panic!("expected Invalid, got {other:?}"),
    }
    // A draft complaint is not evidence.
    let draft: i64 = sqlx::query_scalar(
        "insert into complaints (project_id, author_id, title, body, severity, frequency, \
                                 strategic_multiplier, status, created_at, updated_at, public_id) \
         values ($1, $2, 'draft', '', 1, 1.0, 1.0, 'draft', now(), now(), $3) returning id",
    )
    .bind(project)
    .bind(user)
    .bind(ulid::Ulid::new().to_string())
    .fetch_one(&pool)
    .await
    .unwrap();
    match concord_db::feature_writes::create_feature(
        &pool, project, user, "backed by a draft", "", "", None, None, &[draft],
    )
    .await
    {
        Err(concord_db::feature_writes::WriteError::Invalid(message)) => {
            assert!(message.contains("validated"), "got: {message}")
        }
        other => panic!("expected Invalid for an unvalidated complaint, got {other:?}"),
    }
    // A bad effort size is refused before anything is written.
    sqlx::query("update projects set intake_mode = 'both' where id = $1")
        .bind(project)
        .execute(&pool)
        .await
        .unwrap();
    match concord_db::feature_writes::create_feature(
        &pool, project, user, "huge", "", "XXL", None, None, &[],
    )
    .await
    {
        Err(concord_db::feature_writes::WriteError::Invalid(message)) => {
            assert!(message.contains("effort"), "got: {message}")
        }
        other => panic!("expected Invalid for a bad effort, got {other:?}"),
    }
}

/// The ranking reads the charter, the pain of the complaints it is linked to, and the themes its tags
/// match — and the theme half is the input that had no caller and no writer before this pass.
#[tokio::test]
async fn priorities_sum_pain_and_theme_weight_and_order_by_the_score() {
    use sqlx::Row;
    let pool = pool().await;
    let (user, project, complaint, _, _) = writable(&pool).await;
    // Two features: one with a validated complaint and a theme-weighted tag, one bare.
    let tagged: i64 = sqlx::query_scalar(
        "insert into tags (project_id, name, namespace) values ($1, 'security', 'topic') returning id")
        .bind(project)
        .fetch_one(&pool)
        .await
        .expect("tag");
    let plain = concord_db::feature_writes::create_feature(
        &pool, project, user, "plain", "", "", None, None, &[],
    )
    .await
    .expect("plain");
    let weighed = concord_db::feature_writes::create_feature(
        &pool, project, user, "weighed", "", "", None, None, &[complaint],
    )
    .await
    .expect("weighed");
    sqlx::query("insert into feature_tags (feature_id, tag_id) values ($1, $2)")
        .bind(weighed.id)
        .bind(tagged)
        .execute(&pool)
        .await
        .expect("feature_tags");
    sqlx::query(
        "insert into strategic_themes (project_id, tag, namespace, weight, rationale, set_by, \
                                       created_at, expires_at) \
         values ($1, 'security', 'topic', 2.0, 'hardening', $2, now(), now() + interval '30 days')",
    )
    .bind(project)
    .bind(user)
    .execute(&pool)
    .await
    .expect("theme");

    let ranked = concord_db::priorities::feature_priorities(&pool, project)
        .await
        .expect("priorities");
    let first = ranked.first().expect("at least one");
    assert_eq!(first.id, weighed.id, "the complaint-and-theme feature must rank first");
    assert!(
        (first.theme_weight - 2.0).abs() < 1e-12,
        "theme_weight = {}, want 2.0",
        first.theme_weight
    );
    assert!(first.pain_score > 0.0, "a validated complaint must carry pain");
    let bare = ranked.iter().find(|row| row.id == plain.id).expect("plain row");
    assert_eq!(bare.theme_weight, 0.0);
    assert_eq!(bare.pain_score, 0.0, "no complaint means no pain, not a default");
    assert!(
        first.priority_score > bare.priority_score,
        "the score that orders the list must say so: {} vs {}",
        first.priority_score,
        bare.priority_score
    );
    // An expired theme is not live: the Go query filters `expires_at > now`, and a theme that kept
    // steering the ranking after it expired would make the roadmap's promises permanent.
    sqlx::query("update strategic_themes set expires_at = now() - interval '1 day' where project_id = $1")
        .bind(project)
        .execute(&pool)
        .await
        .unwrap();
    let after = concord_db::priorities::feature_priorities(&pool, project)
        .await
        .expect("priorities again");
    let weighed_after = after.iter().find(|row| row.id == weighed.id).expect("row");
    assert_eq!(weighed_after.theme_weight, 0.0, "an expired theme must stop counting");

    // And the weights come from the charter, not from a constant: a project with mu = 0 ignores the
    // weight term entirely.
    sqlx::query("update charters set mu = 0 where project_id = $1")
        .bind(project)
        .execute(&pool)
        .await
        .unwrap();
    let zeroed = concord_db::priorities::feature_priorities(&pool, project)
        .await
        .expect("priorities with mu = 0");
    let row = zeroed.iter().find(|row| row.id == weighed.id).unwrap();
    let expected = row.elo_r - 2.0 * row.elo_rd + 20.0 * (1.0 + row.pain_score).ln();
    assert!(
        (row.priority_score - expected).abs() < 1e-9,
        "with mu = 0 the weight term must vanish: {} vs {}",
        row.priority_score,
        expected
    );
    let _ = sqlx::query("select 1").fetch_one(&pool).await.unwrap().get::<i32, _>(0);
}

/// The audit reader's two clamps and its search escaping — the three rules that produce a plausible
/// page when they are wrong.
#[tokio::test]
async fn the_audit_page_clamps_its_limit_and_escapes_its_search() {
    let pool = pool().await;
    let (user, project, _, _, _) = writable(&pool).await;
    let base = concord_db::audit::Filter { project_id: project, ..Default::default() };
    // The fixture's own writes (create_project, create_feature) put rows in this project's log, so
    // every total below is read as a delta from what is already there rather than as an absolute —
    // read BEFORE the 62 rows this test adds.
    let before = concord_db::audit::list(&pool, &base).await.expect("baseline page").total;
    // 60 rows, so the default page (50) and the ceiling (200) can be told apart.
    for index in 0..60 {
        concord_db::feature_writes::add_audit(
            &pool, project, user, "fixture.action", "feature", index,
            "plain detail",
        )
        .await
        .expect("audit row");
    }
    // Two rows whose detail carries LIKE metacharacters: a search for "50%" must find exactly one.
    concord_db::feature_writes::add_audit(
        &pool, project, user, "fixture.action", "feature", 900, "the 50% case",
    )
    .await
    .expect("percent row");
    concord_db::feature_writes::add_audit(
        &pool, project, user, "fixture.action", "feature", 901, "the 500 case",
    )
    .await
    .expect("plain row");

    let base = concord_db::audit::Filter { project_id: project, ..Default::default() };

    // Absent limit is the default.
    let page = concord_db::audit::list(&pool, &base).await.expect("page");
    assert_eq!(page.limit, 50, "an absent limit means the default");
    assert_eq!(page.entries.len(), 50);
    assert_eq!(page.total, before + 62, "the total counts the whole log, not the page");

    // A limit above the ceiling is the ceiling, NOT the default. Folding the two clamps answers 50
    // here and the only symptom is a page that looks like it lost rows.
    let capped = concord_db::audit::list(
        &pool,
        &concord_db::audit::Filter { limit: 5_000, ..base.clone() },
    )
    .await
    .expect("capped");
    assert_eq!(capped.limit, 200, "a limit above the ceiling is the ceiling");

    // An explicit small limit is honoured.
    let small = concord_db::audit::list(
        &pool,
        &concord_db::audit::Filter { limit: 5, offset: 3, ..base.clone() },
    )
    .await
    .expect("small page");
    assert_eq!((small.limit, small.offset, small.entries.len()), (5, 3, 5));

    // Newest first.
    assert!(
        small.entries[0].id > small.entries[4].id,
        "the log is newest first"
    );

    // The wildcard is a literal: "50%" finds the one row that says so, not every row with a 50.
    let wildcard = concord_db::audit::list(
        &pool,
        &concord_db::audit::Filter { text: "50%".into(), ..base.clone() },
    )
    .await
    .expect("search");
    assert_eq!(
        wildcard.total, 1,
        "a search for 50%% must not also match 'the 500 case' -- got {:?}",
        wildcard.entries.iter().map(|entry| &entry.detail).collect::<Vec<_>>()
    );
    assert_eq!(wildcard.entries[0].detail, "the 50% case");

    // An empty search emits no LIKE at all: the unfiltered total comes back.
    let empty = concord_db::audit::list(&pool, &concord_db::audit::Filter { text: String::new(), ..base.clone() })
        .await
        .expect("empty search");
    assert_eq!(empty.total, before + 62, "an empty search must not narrow anything");

    // The display name is resolved and the email never is: `users` has an email column and this log
    // is meant to be read by anyone.
    assert!(
        page.entries.iter().all(|entry| entry.actor_display_name.is_some()),
        "every fixture row has an actor, so every row must carry a display name"
    );
    let actions = concord_db::audit::actions(&pool, project).await.expect("actions");
    assert!(actions.contains(&"fixture.action".to_string()));
    assert_eq!(actions, {
        let mut sorted = actions.clone();
        sorted.sort();
        sorted
    }, "the filter vocabulary is alphabetical");
    assert_eq!(concord_db::audit::kind_actions("nonsense"), None, "an unknown kind is an error");
}

/// §6.6's hidden tally, and the eligible population that makes quorum mean something.
#[tokio::test]
async fn an_open_call_hides_its_tally_and_an_unset_eligible_would_make_quorum_vacuous() {
    let pool = pool().await;
    let (user, project, _, _, _) = writable(&pool).await;
    use sqlx::Row;
    // `feature_id` is NULL, not 0: a §6.6 process decision is not about a feature, and PostgreSQL
    // enforces `fk_consensus_calls_feature_id` where SQLite never did (`PRAGMA foreign_keys=OFF`). The
    // fixture writes what the application writes — `internal/store/arenas.go` and the consensus
    // writer both use `NULLIF(?, 0)` — rather than a sentinel the port is right to refuse.
    let call: i64 = sqlx::query_scalar(
        "insert into consensus_calls (project_id, feature_id, opened_by, opens_at, closes_at, \
                                      status, question, public_id) \
         values ($1, null, $2, now(), now() + interval '7 days', 'open', 'ship it?', $3) returning id",
    )
    .bind(project)
    .bind(user)
    .bind(ulid::Ulid::new().to_string())
    .fetch_one(&pool)
    .await
    .expect("call");
    for stance in ["consent", "consent", "block"] {
        sqlx::query(
            "insert into positions (call_id, user_id, position, updated_at) \
             values ($1, (select id from users order by id limit 1), $2, now()) \
             on conflict (call_id, user_id) do update set position = excluded.position",
        )
        .bind(call)
        .bind(stance)
        .execute(&pool)
        .await
        .expect("position");
    }

    let counts = concord_db::consensus::tally(&pool, project, call)
        .await
        .expect("tally");
    assert_eq!(counts.participants, 1, "three rows, one distinct voter");
    let summary = concord_db::consensus::thresholds_summary(
        &counts,
        &concord_governance::Charter::default_for(
            concord_governance::GovernanceModel::Collective,
        ),
    );
    assert!(summary["consent"].as_i64().is_some(), "a closed call's tally carries counts");

    let hidden = concord_db::consensus::hidden_tally(summary);
    assert_eq!(hidden["consent"], serde_json::Value::Null, "an open call hides the counts");
    assert_eq!(hidden["block"], serde_json::Value::Null);
    assert_eq!(hidden["decisive_ratio"], serde_json::Value::Null);
    // §6.6 preserves participation progress and the quorum bar.
    assert!(hidden["participants"].as_i64().is_some(), "participation stays visible");
    assert!(hidden["eligible"].as_i64().is_some(), "eligibility stays visible");
    assert!(hidden["quorum_required"].as_i64().is_some(), "the quorum bar stays visible");

    // The caller's own position is returned and nobody else's.
    let positions = concord_db::consensus::positions(&pool, call).await.expect("positions");
    let own = concord_db::consensus::own_positions_only(positions.clone(), positions[0].user_id);
    assert_eq!(own.len(), 1);
    assert!(concord_db::consensus::own_positions_only(positions, 0).is_empty(), "an anonymous reader owns no position");

    // Eligible is computed from §8.2, not assumed: this fixture's project has a member with no
    // qualifying contribution, so the count is what it is rather than "everyone".
    let eligible = concord_db::consensus::eligible_collaborators(&pool, project, 365.0)
        .await
        .expect("eligible");
    assert_eq!(
        counts.eligible,
        eligible.len() as i64,
        "tally.eligible must be the same number the threshold is computed from"
    );
    let _ = sqlx::query("select 1").fetch_one(&pool).await.unwrap().get::<i32, _>(0);
}
