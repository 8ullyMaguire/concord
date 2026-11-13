// Command goldenembed prints the exact vectors the ported embedder must reproduce.
//
// TEMPORARY, generated for the port: it lives in the archived tree only long enough to emit the fixture, then is
// removed. It prints every non-zero bucket, not a summary, because a summary (dot product, a few dimensions) hides
// exactly the class of bug this fixture exists to catch -- a sign, an index or a width that is off by a little.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"git.polarisocial.xyz/concord/concord/internal/embed"
)

var texts = []string{
	"",
	"crash",
	"export fails",
	"the of and is",
	"Application starts with SQLite",
	"Application starts with PostgreSQL",
	"upload does not work",
	"the upload does not work",
	"Uploads do not work!",
	"network drop on resume",
	"resume/restart kills the transfer",
	"naive café naïve",
	"Ünïcode Ärger — grüße",
	"12345",
	"a1b2-c3d4_e5f6",
	"   ",
	"???",
	"The roadmap page shows an empty column for every milestone",
	"milestone column empty on roadmap",
	"Roadmap: empty milestone columns",
	"users cannot reset their password",
	"password reset email never arrives",
	"Password resets are not delivered",
	"sqlite locked under load",
	"postgres connection pool exhausted under load",
	"the same complaint filed twice",
	"same complaint filed twice again",
	"x",
	"xy",
	"xyz",
	"`backticks` and *stars*",
	"tabs\tand\nnewlines",
}

func main() {
	h := embed.NewHashed()
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()

	fmt.Fprintf(w, "META\t%s\t%d\t%d\n", h.ID(), h.Dims(), len(texts))

	vectors := make([][]float32, len(texts))
	for i, t := range texts {
		v := h.Embed(t)
		vectors[i] = v
		fmt.Fprintf(w, "TEXT\t%d\t%s\n", i, strconv.Quote(t))
		for idx, x := range v {
			if x == 0 {
				continue
			}
			fmt.Fprintf(w, "V\t%d\t%d\t%s\n", i, idx, strconv.FormatFloat(float64(x), 'g', -1, 32))
		}
		fmt.Fprintf(w, "HAS\t%d\t%t\n", i, embed.HasContent(v))
		fmt.Fprintf(w, "ENC\t%d\t%s\n", i, hexOf(embed.Encode(v)))
	}

	// Every pair, so a tolerance cannot hide a single wrong bucket behind a correct total.
	for i := range texts {
		for j := range texts {
			fmt.Fprintf(w, "COS\t%d\t%d\t%s\n", i, j, strconv.FormatFloat(embed.Cosine(vectors[i], vectors[j]), 'g', -1, 64))
			// Round-tripping through the BLOB column must not change the number.
			if i == j {
				fmt.Fprintf(w, "RT\t%d\t%s\n", i, strconv.FormatFloat(
					embed.Cosine(embed.Decode(embed.Encode(vectors[i])), vectors[i]), 'g', -1, 64))
			}
		}
	}

	// The thresholds, as decisions rather than numbers, over the pairs above.
	for i := range texts {
		for j := range texts {
			s := embed.Cosine(vectors[i], vectors[j])
			if embed.IsStrongDuplicate(s) {
				fmt.Fprintf(w, "STRONG\t%d\t%d\n", i, j)
			}
			if embed.IsDuplicate(s) {
				fmt.Fprintf(w, "DUP\t%d\t%d\n", i, j)
			}
		}
	}

	// Rank: ties broken by entity id, stable, truncated.
	sims := []embed.Similarity{
		{Kind: "request", EntityID: 7, Score: 0.5},
		{Kind: "request", EntityID: 3, Score: 0.9},
		{Kind: "request", EntityID: 5, Score: 0.9},
		{Kind: "request", EntityID: 1, Score: 0.5},
	}
	for _, s := range embed.Rank(sims, 3) {
		fmt.Fprintf(w, "RANK\t%d\t%s\n", s.EntityID, strconv.FormatFloat(s.Score, 'g', -1, 64))
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	var sb strings.Builder
	for _, x := range b {
		sb.WriteByte(digits[x>>4])
		sb.WriteByte(digits[x&0x0f])
	}
	return sb.String()
}
