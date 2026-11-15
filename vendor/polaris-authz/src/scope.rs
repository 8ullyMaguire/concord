//! How far a permission reaches. `Own` is the object you made, `Curated` the objects you look after,
//! `Site` everything on this instance, `Estate` everything on every instance. A grant's scope is a
//! ceiling; the call states what it needs; the wider must cover the narrower.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Scope {
    Own,
    Curated,
    Site,
    Estate,
}

impl Scope {
    pub fn covers(self, needed: Scope) -> bool {
        self >= needed
    }

    pub fn as_str(self) -> &'static str {
        match self {
            Scope::Own => "own",
            Scope::Curated => "curated",
            Scope::Site => "site",
            Scope::Estate => "estate",
        }
    }

    pub fn parse(raw: &str) -> Option<Scope> {
        match raw {
            "own" => Some(Scope::Own),
            "curated" => Some(Scope::Curated),
            "site" => Some(Scope::Site),
            "estate" => Some(Scope::Estate),
            _ => None,
        }
    }
}
