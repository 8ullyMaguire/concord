package httpapi

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Feed and FeedItem are a syndication document in either of two serialisations.
//
// RSS 2.0 and Atom 1.0 differ in element names and in two required fields -- Atom wants
// <updated> and an atom:link rel="self", RSS wants <pubDate> and an optional one -- and in
// nothing else. So there is ONE model and two writers. Two hand-written templates would
// drift, and the drift would be silent, because a feed reader ignores elements it does
// not recognise rather than complaining.
//
// See docs/specs/feeds-spec.md.

const (
	// feedContentTypeXML is what a reader keys on; the charset goes with it because a
	// document declaring text/xml defaults to US-ASCII and non-ASCII titles become
	// mojibake in readers that honour the declaration.
	feedContentTypeXML = "application/xml; charset=utf-8"
	feedContentTypeRSS = "application/rss+xml; charset=utf-8"
	feedContentTypeAto = "application/atom+xml; charset=utf-8"

	// feedItemLimit bounds one document. A feed with 10,000 items is a scrape target,
	// not a reader.
	feedItemLimit = 50
)

// Feed is one syndication document, independent of its serialisation.
type Feed struct {
	Title       string
	SelfURL     string // absolute; atom:link rel="self", and RSS's optional one
	AltURL      string // the HTML page a human should read instead
	Description string
	Updated     time.Time
	Items       []FeedItem
}

// FeedItem is one entry. Author is a display name only -- see DisplayNamesFor.
type FeedItem struct {
	ID         string // absolute, stable, never reused
	Title      string
	Link       string // absolute
	Updated    time.Time
	Summary    string
	Author     string
	Categories []string
}

// rfc822 is RSS's date format. time.RFC3339 is Atom's. A single format in both is a
// common and reader-visible bug: RSS readers that cannot parse RFC3339 silently drop the
// item rather than showing it undated.
func rfc822(t time.Time) string { return t.UTC().Format(time.RFC1123Z) }

// plainText reduces user-supplied markup to something safe to put in a feed summary.
//
// Two separate problems, and neither is solved by the other. First, XML escaping: this
// whole file marshals through encoding/xml, which escapes text nodes correctly, so a
// title containing <script> cannot break out. Second, readability: a complaint body is
// markdown, and raw markdown in a feed is noise to every reader. So summaries are
// stripped to text and length-capped -- a cap because a 50,000-character body would make
// the feed unusable regardless of format.
func plainText(s string, max int) string {
	s = strings.ReplaceAll(s, "```", " ")
	for _, tag := range []string{"**", "__", "*", "_", "`", "#"} {
		s = strings.ReplaceAll(s, tag, "")
	}
	s = strings.Join(strings.Fields(s), " ")
	// Cut on a rune boundary, not a byte: slicing mid-UTF-8 produces invalid XML.
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "\u2026"
	}
	return s
}

// writeFeed serialises f in the requested format.
//
// format is "rss" or "atom". Anything else is an error rather than a silent default: a
// reader asking for ?format=pdf and receiving RSS 2.0 has no way to tell it happened.
func writeFeed(w http.ResponseWriter, format string, f Feed) error {
	switch format {
	case "rss":
		w.Header().Set("Content-Type", feedContentTypeRSS)
		w.WriteHeader(http.StatusOK)
		return writeRSS(w, f)
	case "atom":
		w.Header().Set("Content-Type", feedContentTypeAto)
		w.WriteHeader(http.StatusOK)
		return writeAtom(w, f)
	default:
		return fmt.Errorf("unsupported feed format %q", format)
	}
}

// --- RSS 2.0 ------------------------------------------------------------------

type rssDoc struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel rssChan  `xml:"channel"`
}

type rssChan struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	// atom:link rel=self is optional in RSS 2.0 but is what tells a reader where
	// the canonical copy is, and it is how a reader detects a feed that moved.
	AtomSelf  string    `xml:"http://www.w3.org/2005/Atom link"`
	AtomRel   string    `xml:"rel,attr"`
	AtomHref  string    `xml:"href,attr"`
	AtomTitle string    `xml:"title,attr"`
	Items     []rssItem `xml:"item"`
}

type rssItem struct {
	Title string `xml:"title"`
	Link  string `xml:"link"`
	// guid isPermaLink=false because the link is absolute and already the identity;
	// letting a reader treat it as a fetchable permalink invites a duplicate request.
	GUID        string   `xml:"guid"`
	GUIDIsPerma string   `xml:"isPermaLink,attr"`
	PubDate     string   `xml:"pubDate"`
	Description string   `xml:"description"`
	Author      string   `xml:"author,omitempty"`
	Categories  []string `xml:"category,omitempty"`
}

func writeRSS(w http.ResponseWriter, f Feed) error {
	doc := rssDoc{
		Version: "2.0",
		Channel: rssChan{
			Title:       f.Title,
			Link:        f.AltURL,
			Description: f.Description,
			PubDate:     rfc822(f.Updated),
			AtomSelf:    f.SelfURL,
			AtomRel:     "self",
			AtomHref:    f.SelfURL,
			AtomTitle:   f.Title,
		},
	}
	for _, it := range f.Items {
		doc.Channel.Items = append(doc.Channel.Items, rssItem{
			Title:       it.Title,
			Link:        it.Link,
			GUID:        it.ID,
			GUIDIsPerma: "false",
			PubDate:     rfc822(it.Updated),
			Description: it.Summary,
			Author:      it.Author,
			Categories:  it.Categories,
		})
	}
	return xml.NewEncoder(w).Encode(doc)
}

// --- Atom 1.0 -----------------------------------------------------------------

type atomDoc struct {
	XMLName  xml.Name   `xml:"http://www.w3.org/2005/Atom feed"`
	Title    string     `xml:"title"`
	ID       string     `xml:"id"`
	Updated  string     `xml:"updated"`
	Link     []atomLnk  `xml:"link"`
	Subtitle string     `xml:"subtitle,omitempty"`
	Items    []atomItem `xml:"entry"`
}

type atomLnk struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr,omitempty"`
}

type atomItem struct {
	Title     string         `xml:"title"`
	ID        string         `xml:"id"`
	Updated   string         `xml:"updated"`
	Published string         `xml:"published,omitempty"`
	Link      atomLnk        `xml:"link"`
	Summary   string         `xml:"summary"`
	Author    *atomAuth      `xml:"author,omitempty"`
	Category  []atomCategory `xml:"category,omitempty"`
}

type atomAuth struct {
	Name string `xml:"name"`
}

// atomCategory carries a term attribute, which a plain string cannot express: Atom's
// category is <category term="..."/>, not <category>...</category>. So the item's
// categories are converted rather than assigned.
type atomCategory struct {
	Term string `xml:"term,attr"`
}

func writeAtom(w http.ResponseWriter, f Feed) error {
	doc := atomDoc{
		Title:    f.Title,
		ID:       f.SelfURL,
		Updated:  f.Updated.UTC().Format(time.RFC3339),
		Subtitle: f.Description,
		Link: []atomLnk{
			{Rel: "self", Href: f.SelfURL, Type: feedContentTypeAto},
			{Rel: "alternate", Href: f.AltURL, Type: "text/html"},
		},
	}
	for _, it := range f.Items {
		e := atomItem{
			Title:   it.Title,
			ID:      it.ID,
			Updated: it.Updated.UTC().Format(time.RFC3339),
			Link:    atomLnk{Rel: "alternate", Href: it.Link, Type: "text/html"},
			Summary: it.Summary,
		}
		// An <author> element with an empty <name> is worse than no author element:
		// readers render the name they find, and "" looks like a bug.
		if it.Author != "" {
			e.Author = &atomAuth{Name: it.Author}
		}
		for _, c := range it.Categories {
			e.Category = append(e.Category, atomCategory{Term: c})
		}
		doc.Items = append(doc.Items, e)
	}
	return xml.NewEncoder(w).Encode(doc)
}
