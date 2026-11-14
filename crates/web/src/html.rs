//! HTML with the escaping decided here, and not at each call site.
//!
//! The reader surface is server-rendered (ADR 0005 decision 2, ADR 0011 §7): the document carries its title, canonical and `og:*` in the
//! first paint, and the SvelteKit bundle hydrates into it. A page built by string concatenation is a page whose escaping is a decision
//! nobody made.
//!
//! # The rule
//!
//! **Everything is escaped unless it is inserted with [`Html::raw`].** There is no other way to emit markup, so the trust decision is
//! visible at the call site rather than distributed across the file.
//!
//! This is the same builder darkhold ships (`crates/app/src/html.rs`), adopted rather than re-invented: its contract is the one the estate
//! already relies on, and a second escaping rule in a second product is the same class of mistake as two numbering schemes in one
//! statement.

use std::fmt::Write as _;

/// Escape a string for HTML text or an attribute value.
///
/// `&` first, so the ampersands this function introduces are not escaped again. The apostrophe becomes `&#39;` rather than `&apos;`,
/// which HTML 4 does not define.
#[must_use]
pub fn escape(input: &str) -> String {
    let mut out = String::with_capacity(input.len());
    for ch in input.chars() {
        match ch {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&quot;"),
            '\'' => out.push_str("&#39;"),
            _ => out.push(ch),
        }
    }
    out
}

/// A document under construction.
#[derive(Debug, Default)]
pub struct Html {
    out: String,
}

impl Html {
    /// An empty document.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Open an element with escaped attribute pairs.
    pub fn open(&mut self, tag: &str, attributes: &[(&str, &str)]) -> &mut Self {
        let _ = write!(self.out, "<{tag}");
        for (name, value) in attributes {
            let _ = write!(self.out, " {name}=\"{}\"", escape(value));
        }
        self.out.push('>');
        self
    }

    /// Close an element.
    pub fn close(&mut self, tag: &str) -> &mut Self {
        let _ = write!(self.out, "</{tag}>");
        self
    }

    /// A self-closing element (a `meta`, a `link`, a `br`).
    pub fn void(&mut self, tag: &str, attributes: &[(&str, &str)]) -> &mut Self {
        let _ = write!(self.out, "<{tag}");
        for (name, value) in attributes {
            let _ = write!(self.out, " {name}=\"{}\"", escape(value));
        }
        self.out.push('>');
        self
    }

    /// Escaped text.
    pub fn text(&mut self, text: &str) -> &mut Self {
        self.out.push_str(&escape(text));
        self
    }

    /// Markup inserted without escaping.
    ///
    /// **The only place trust is decided.** Every call site must be able to say why the string is already safe; today that is the
    /// document skeleton this crate produces itself, and nothing a reader supplied.
    pub fn raw(&mut self, markup: &str) -> &mut Self {
        self.out.push_str(markup);
        self
    }

    /// An element with escaped text inside it.
    pub fn element(&mut self, tag: &str, text: &str) -> &mut Self {
        self.open(tag, &[]).text(text).close(tag)
    }

    /// The finished document.
    #[must_use]
    pub fn finish(self) -> String {
        self.out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn text_is_escaped_and_markup_is_not() {
        let mut page = Html::new();
        page.open("p", &[]).text("5 < 6 & \"true\"").close("p");
        assert_eq!(page.finish(), "<p>5 &lt; 6 &amp; &quot;true&quot;</p>");
    }

    #[test]
    fn an_attribute_value_is_escaped_too() {
        let mut page = Html::new();
        page.void("meta", &[("content", "a\"b<c")]);
        assert_eq!(page.finish(), "<meta content=\"a&quot;b&lt;c\">");
    }

    #[test]
    fn an_ampersand_is_escaped_once() {
        assert_eq!(escape("a &amp; b"), "a &amp;amp; b");
    }
}
