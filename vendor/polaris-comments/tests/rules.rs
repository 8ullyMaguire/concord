use polaris_comments::*;

fn c(id: i64, parent: Option<i64>, depth: u8, created: i64, score: i64) -> Comment {
    Comment {
        id,
        parent_id: parent,
        depth,
        body: format!("body {id}"),
        created_at: created,
        score,
        deleted: false,
    }
}

#[test]
fn a_whitespace_only_comment_is_refused_rather_than_published() {
    assert_eq!(check_body(&Policy::SUKI, "   \n\t "), Err(Refused::Empty));
    assert_eq!(check_body(&Policy::SUKI, "ok"), Ok(()));
}

#[test]
fn an_over_long_comment_names_the_limit_instead_of_being_truncated() {
    let long = "x".repeat(5001);
    match check_body(&Policy::WHITEBOIS, &long) {
        Err(Refused::TooLong { max, got }) => {
            assert_eq!((max, got), (5000, 5001));
        }
        other => panic!("expected TooLong, got {other:?}"),
    }
}

#[test]
fn the_bound_counts_characters_not_bytes() {
    // 2000 emoji are 8_000 bytes and must pass suki's 2000-character bound: a bound counted in bytes would refuse a
    // comment that fits.
    let body = "🌙".repeat(2000);
    assert_eq!(check_body(&Policy::SUKI, &body), Ok(()));
    let one_too_many = "🌙".repeat(2001);
    assert!(matches!(check_body(&Policy::SUKI, &one_too_many), Err(Refused::TooLong { .. })));
}

#[test]
fn a_top_level_comment_sits_at_depth_zero() {
    assert_eq!(place(&Policy::SUKI, None), Ok(0));
    assert_eq!(place(&Policy::WHITEBOIS, None), Ok(0));
}

#[test]
fn suki_lets_a_thread_go_as_deep_as_the_conversation_does() {
    let parent = Parent { id: 7, depth: 40, in_thread: true };
    assert_eq!(place(&Policy::SUKI, Some(parent)), Ok(41));
}

#[test]
fn whitebois_flattens_a_third_level_instead_of_refusing_it() {
    // Measured in api.rs (§7.7): a reply deeper than two keeps its parent and has its depth capped, "so a client with a
    // stale nesting depth can still post". Refusing here would be a policy the site does not hold.
    let at_two = Parent { id: 7, depth: 2, in_thread: true };
    assert_eq!(place(&Policy::WHITEBOIS, Some(at_two)), Ok(2));
    let at_one = Parent { id: 7, depth: 1, in_thread: true };
    assert_eq!(place(&Policy::WHITEBOIS, Some(at_one)), Ok(2), "a genuine reply to depth 1 sits at 2");
}

#[test]
fn a_capped_policy_can_declare_refusal_instead_of_flattening() {
    let refusing = Policy { max_depth: MaxDepth::Capped(2), overflow: Overflow::Refuse, ..Policy::SUKI };
    let at_two = Parent { id: 7, depth: 2, in_thread: true };
    assert_eq!(
        place(&refusing, Some(at_two)),
        Err(Refused::TooDeep { cap: 2, parent_depth: 2 })
    );
}

#[test]
fn a_parent_from_another_thread_is_refused_not_attached() {
    let foreign = Parent { id: 9, depth: 0, in_thread: false };
    assert_eq!(
        place(&Policy::SUKI, Some(foreign)),
        Err(Refused::ParentNotInThread { parent_id: 9 })
    );
}

#[test]
fn oldest_first_is_a_conversation_and_newest_first_is_a_feed() {
    let mut page = vec![c(1, None, 0, 100, 0), c(2, None, 0, 300, 0), c(3, None, 0, 200, 0)];
    order(&Policy::SUKI, &mut page);
    assert_eq!(page.iter().map(|x| x.id).collect::<Vec<_>>(), vec![1, 3, 2]);

    let feed = Policy { ordering: Ordering::Newest, ..Policy::SUKI };
    order(&feed, &mut page);
    assert_eq!(page.iter().map(|x| x.id).collect::<Vec<_>>(), vec![2, 3, 1]);
}

#[test]
fn score_ordering_breaks_ties_by_id_so_two_renders_agree() {
    let mut page = vec![c(9, None, 0, 100, 5), c(4, None, 0, 100, 5), c(8, None, 0, 100, 7)];
    order(&Policy::WHITEBOIS, &mut page);
    assert_eq!(page.iter().map(|x| x.id).collect::<Vec<_>>(), vec![8, 4, 9]);
}

#[test]
fn a_tombstone_keeps_the_reply_attached_and_shows_that_something_was_removed() {
    let mut page = vec![c(1, None, 0, 100, 0), c(2, Some(1), 1, 200, 0)];
    let deleted = after_delete(&Policy::SUKI, page.remove(0)).expect("tombstone keeps the row");
    assert_eq!(deleted.body, "");
    assert!(deleted.deleted);
    assert_eq!(page[0].parent_id, Some(1), "the reply still points at the tombstone");
    assert_eq!(replies_lost(&Policy::SUKI, &page, 1), 0);
}

#[test]
fn a_cascade_delete_removes_the_row_and_counts_what_the_reader_loses() {
    let page = vec![c(1, None, 0, 100, 0), c(2, Some(1), 1, 200, 0), c(3, Some(2), 2, 300, 0)];
    assert!(after_delete(&Policy::WHITEBOIS, page[0].clone()).is_none());
    assert_eq!(replies_lost(&Policy::WHITEBOIS, &page, 1), 2, "both descendants go with it");
    assert_eq!(replies_lost(&Policy::WHITEBOIS, &page, 3), 0);
}

#[test]
fn the_two_declared_policies_are_the_two_measured_sites() {
    // If a site's policy drifts from what its schema enforces, this is where the disagreement becomes visible.
    assert_eq!(Policy::SUKI.max_depth, MaxDepth::Unbounded);
    assert_eq!(Policy::SUKI.body.max, 2000, "suki's MAX_BODY_CHARS, measured");
    assert_eq!(Policy::SUKI.deletion, Deletion::Tombstone);
    assert_eq!(Policy::WHITEBOIS.max_depth, MaxDepth::Capped(2));
    assert_eq!(Policy::WHITEBOIS.deletion, Deletion::Cascade);
    assert_eq!(Policy::WHITEBOIS.overflow, Overflow::Flatten, "api.rs keeps the parent and caps the depth");
    assert_ne!(Policy::SUKI, Policy::WHITEBOIS);
}
