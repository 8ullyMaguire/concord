//! Generate the asset table.
//!
//! # Why generated
//!
//! `include_bytes!` needs a literal path at compile time, so a table of `/assets/js/*.js` cannot be written once and left alone: a script
//! added to `assets/` and not to the table is a page that loads a 404, which is exactly the class of bug the browser suites exist to
//! catch — and a hand-maintained list is a list somebody forgets. The build walks the directory instead.
//!
//! The output is one static array of `(name, bytes)`, keyed by the path a page asks for (`js/project.js`), sorted so the generated file
//! is stable across builds and does not churn the diff.
//!
//! **Cargo re-runs this only when something in `assets/` changes**, which is what `cargo:rerun-if-changed` below buys: without it a new
//! script would be served by a build that never looked for it.

use std::fs;
use std::path::{Path, PathBuf};

fn walk(directory: &Path, prefix: &str, out: &mut Vec<(String, PathBuf)>) {
    let Ok(entries) = fs::read_dir(directory) else { return };
    for entry in entries.flatten() {
        let path = entry.path();
        let name = entry.file_name().to_string_lossy().to_string();
        let key = if prefix.is_empty() { name.clone() } else { format!("{prefix}/{name}") };
        if path.is_dir() {
            walk(&path, &key, out);
        } else {
            out.push((key, path));
        }
    }
}

fn main() {
    let assets = Path::new("assets");
    println!("cargo:rerun-if-changed=assets");
    let mut files: Vec<(String, PathBuf)> = Vec::new();
    walk(assets, "", &mut files);
    files.sort_by(|left, right| left.0.cmp(&right.0));

    let mut source = String::from("/// Every page asset, by the path a page asks for.\n");
    source.push_str("pub static ASSETS: &[(&str, &[u8])] = &[\n");
    for (key, path) in &files {
        source.push_str(&format!(
            "    ({key:?}, include_bytes!({:?})),\n",
            path.canonicalize().unwrap_or_else(|_| path.clone()).display().to_string()
        ));
    }
    source.push_str("];\n");

    let out_dir = std::env::var("OUT_DIR").expect("OUT_DIR is set by cargo");
    let destination = Path::new(&out_dir).join("asset_table.rs");
    fs::write(&destination, source).expect("write the generated asset table");
    println!("cargo:rerun-if-changed=build.rs");
}
