//! The ported embedder against vectors emitted by the Go implementation it replaces.
//!
//! The fixture is `golden.tsv`, produced by running the Go `internal/embed` package over a 32-text corpus and printing
//! **every non-zero bucket**, every pairwise cosine, every encode/decode round trip, the two threshold decisions and one
//! ranked list. `scripts/parity/golden_embed.go` is the generator; the ledger records how to re-run it.
//!
//! Two choices make this a real test rather than a decorative one:
//!
//! * **bit equality on the bucket values**, not a tolerance. `strconv.FormatFloat(x, 'g', -1, 32)` is the shortest
//!   decimal that round-trips to that `float32`, so parsing it back in Rust must give the same bits. A tolerance would
//!   let a wrong sign, a wrong weight or an off-by-one index hide behind a correct total.
//! * **all 1,024 ordered pairs**, not a sample. The scores are what the thresholds act on, and a single wrong bucket
//!   changes a pair that a sample would plausibly miss.

use concord_embed::{cosine, decode, encode, has_content, Hashed};
use std::collections::BTreeMap;

const GOLDEN: &str = include_str!("golden.tsv");

struct Fixture {
    texts: Vec<String>,
    vectors: BTreeMap<usize, Vec<(usize, f32)>>,
    has_content: BTreeMap<usize, bool>,
    encoded: BTreeMap<usize, String>,
    cosines: BTreeMap<(usize, usize), f64>,
    round_trips: BTreeMap<usize, f64>,
    strong: Vec<(usize, usize)>,
    duplicates: Vec<(usize, usize)>,
    ranked: Vec<(i64, f64)>,
    id: String,
    dims: usize,
}

fn parse_golden() -> Fixture {
    let mut fixture = Fixture {
        texts: Vec::new(),
        vectors: BTreeMap::new(),
        has_content: BTreeMap::new(),
        encoded: BTreeMap::new(),
        cosines: BTreeMap::new(),
        round_trips: BTreeMap::new(),
        strong: Vec::new(),
        duplicates: Vec::new(),
        ranked: Vec::new(),
        id: String::new(),
        dims: 0,
    };
    for line in GOLDEN.lines() {
        let fields: Vec<&str> = line.split('\t').collect();
        match fields[0] {
            "META" => {
                fixture.id = fields[1].to_string();
                fixture.dims = fields[2].parse().expect("dims");
            }
            "TEXT" => {
                let index: usize = fields[1].parse().expect("text index");
                // The field is Go's %q, which is a double-quoted string with the escapes this corpus needs (tab,
                // newline, backslash, quote). serde_json parses exactly that subset, and the corpus carries no Go-only
                // escape.
                let text: String = serde_json::from_str(fields[2]).expect("quoted text");
                assert_eq!(index, fixture.texts.len(), "texts arrive in order");
                fixture.texts.push(text);
            }
            "V" => {
                let index: usize = fields[1].parse().expect("vector index");
                let bucket: usize = fields[2].parse().expect("bucket");
                let value: f32 = fields[3].parse().expect("bucket value round-trips to f32");
                fixture.vectors.entry(index).or_default().push((bucket, value));
            }
            "HAS" => {
                let index: usize = fields[1].parse().expect("has index");
                fixture
                    .has_content
                    .insert(index, fields[2] == "true");
            }
            "ENC" => {
                let index: usize = fields[1].parse().expect("enc index");
                fixture.encoded.insert(index, fields[2].to_string());
            }
            "COS" => {
                let a: usize = fields[1].parse().expect("cos a");
                let b: usize = fields[2].parse().expect("cos b");
                let score: f64 = fields[3].parse().expect("cos score");
                fixture.cosines.insert((a, b), score);
            }
            "RT" => {
                let index: usize = fields[1].parse().expect("rt index");
                let score: f64 = fields[2].parse().expect("rt score");
                fixture.round_trips.insert(index, score);
            }
            "STRONG" => fixture.strong.push((
                fields[1].parse().expect("strong a"),
                fields[2].parse().expect("strong b"),
            )),
            "DUP" => fixture.duplicates.push((
                fields[1].parse().expect("dup a"),
                fields[2].parse().expect("dup b"),
            )),
            "RANK" => fixture.ranked.push((
                fields[1].parse().expect("rank id"),
                fields[2].parse().expect("rank score"),
            )),
            other => panic!("unknown fixture line: {other}"),
        }
    }
    fixture
}

fn hex(bytes: &[u8]) -> String {
    use std::fmt::Write as _;
    bytes.iter().fold(String::new(), |mut out, byte| {
        let _ = write!(out, "{byte:02x}");
        out
    })
}

#[test]
fn the_embedder_reproduces_every_bucket_the_reference_emitted() {
    let golden = parse_golden();
    let hashed = Hashed::new();
    assert_eq!(hashed.id(), golden.id, "the model id is load-bearing");
    assert_eq!(hashed.dims(), golden.dims);

    let mut compared = 0_usize;
    for (index, text) in golden.texts.iter().enumerate() {
        let ours = hashed.embed(text);
        let mut our_buckets: Vec<(usize, f32)> = ours
            .iter()
            .enumerate()
            .filter(|(_, value)| **value != 0.0)
            .map(|(bucket, value)| (bucket, *value))
            .collect();
        our_buckets.sort_by_key(|(bucket, _)| *bucket);

        let want = golden.vectors.get(&index).cloned().unwrap_or_default();
        assert_eq!(
            our_buckets.len(),
            want.len(),
            "text {index} ({text:?}): bucket count differs — a different feature set, not a rounding difference"
        );
        for ((our_bucket, our_value), (want_bucket, want_value)) in our_buckets.iter().zip(want.iter()) {
            assert_eq!(our_bucket, want_bucket, "text {index} ({text:?}): bucket index differs");
            assert_eq!(
                our_value.to_bits(),
                want_value.to_bits(),
                "text {index} ({text:?}) bucket {our_bucket}: {our_value} != {want_value} (bits differ)"
            );
            compared += 1;
        }

        assert_eq!(
            has_content(&ours),
            golden.has_content[&index],
            "text {index} ({text:?}): has_content differs"
        );
        assert_eq!(
            hex(&encode(&ours)),
            golden.encoded[&index],
            "text {index} ({text:?}): encoded bytes differ"
        );
    }
    // A fixture that silently stopped carrying buckets would otherwise pass by comparing nothing.
    assert!(compared > 800, "only {compared} buckets compared; the fixture is not what it was");
}

#[test]
fn every_ordered_pair_keeps_its_cosine() {
    let golden = parse_golden();
    let hashed = Hashed::new();
    let vectors: Vec<Vec<f32>> = golden.texts.iter().map(|text| hashed.embed(text)).collect();

    let mut worst = 0.0_f64;
    for (&(a, b), &want) in &golden.cosines {
        let got = cosine(&vectors[a], &vectors[b]);
        assert_eq!(
            got.to_bits(),
            want.to_bits(),
            "cosine({:?}, {:?}) = {got} but the reference said {want}",
            golden.texts[a],
            golden.texts[b]
        );
        worst = worst.max((got - want).abs());
    }
    assert_eq!(golden.cosines.len(), golden.texts.len() * golden.texts.len());
    assert_eq!(worst, 0.0, "every pair is bit-identical, not merely close");
}

#[test]
fn a_vector_survives_the_blob_column() {
    let golden = parse_golden();
    let hashed = Hashed::new();
    for (&index, &want) in &golden.round_trips {
        let vector = hashed.embed(&golden.texts[index]);
        let back = decode(&encode(&vector));
        assert_eq!(back, vector, "text {index}: encode/decode changed the vector");
        assert_eq!(
            cosine(&back, &vector).to_bits(),
            want.to_bits(),
            "text {index}: the round trip is not self-similar"
        );
    }
    assert!(decode(&[0, 1, 2]).is_empty(), "a truncated blob is ignored, not half-read");
    assert!(decode(&[]).is_empty());
}

#[test]
fn the_two_bands_land_on_the_same_pairs() {
    let golden = parse_golden();
    let hashed = Hashed::new();
    let vectors: Vec<Vec<f32>> = golden.texts.iter().map(|text| hashed.embed(text)).collect();

    let mut strong = Vec::new();
    let mut duplicates = Vec::new();
    for a in 0..vectors.len() {
        for b in 0..vectors.len() {
            let score = cosine(&vectors[a], &vectors[b]);
            if concord_embed::is_strong_duplicate(score) {
                strong.push((a, b));
            }
            if concord_embed::is_duplicate(score) {
                duplicates.push((a, b));
            }
        }
    }
    assert_eq!(strong, golden.strong, "the strong band is a different set of pairs");
    assert_eq!(duplicates, golden.duplicates, "the near-match band is a different set of pairs");

    // The bands must do something on this corpus, or the test above proves only that both are empty.
    assert!(!strong.is_empty(), "the corpus contains no strong duplicate, which cannot be right");
    assert!(
        duplicates.len() > strong.len(),
        "the near-match band must be strictly wider than the strong band"
    );

    // And the corpus must contain a literal repeat: "the upload does not work" against "Uploads do not work!" is the
    // morphological case the trigram signal exists for.
    let literal = cosine(&vectors[6], &vectors[7]);
    assert!(
        concord_embed::is_duplicate(literal),
        "a stop-word-only difference must not hide a duplicate: {literal}"
    );
}

#[test]
fn rank_breaks_ties_by_entity_id() {
    let golden = parse_golden();
    let sims = vec![
        concord_embed::Similarity {
            kind: "request".into(),
            entity_id: 7,
            score: 0.5,
            title: String::new(),
            status: "open".into(),
            url: None,
        },
        concord_embed::Similarity {
            kind: "request".into(),
            entity_id: 3,
            score: 0.9,
            title: String::new(),
            status: "open".into(),
            url: None,
        },
        concord_embed::Similarity {
            kind: "request".into(),
            entity_id: 5,
            score: 0.9,
            title: String::new(),
            status: "open".into(),
            url: None,
        },
        concord_embed::Similarity {
            kind: "request".into(),
            entity_id: 1,
            score: 0.5,
            title: String::new(),
            status: "open".into(),
            url: None,
        },
    ];
    let ranked: Vec<(i64, f64)> = concord_embed::rank(sims, 3)
        .into_iter()
        .map(|s| (s.entity_id, s.score))
        .collect();
    assert_eq!(ranked, golden.ranked);
    // Hand-written first, corrected by the oracle: the 0.5 tie is ids 7 and 1, so id order puts **1** third and the
    // limit of three then cuts id 7 entirely. My expectation had said [3, 5, 7] — inserting the tie in the order it was
    // written rather than the order the rule decides.
    assert_eq!(
        ranked.iter().map(|(id, _)| *id).collect::<Vec<_>>(),
        vec![3, 5, 1],
        "equal scores come out in id order, and the list is cut to the limit"
    );
}
