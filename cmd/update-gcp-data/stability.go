package main

import (
	"bytes"
	"regexp"

	"spotinfo/internal/snapshot"
)

// Volatile markup this digest removes before comparing two reads of the same
// page. Script and style bodies go first, because the per-response request id
// lives inside one; then comments; then every remaining tag, which is where the
// per-response CSP nonce lives as an attribute.
var (
	scriptOrStyle = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</(script|style)>`)
	htmlComment   = regexp.MustCompile(`(?s)<!--.*?-->`)
	anyTag        = regexp.MustCompile(`(?s)<[^>]*>`)
	whitespaceRun = regexp.MustCompile(`\s+`)
)

// stabilityDigest hashes the rendered text of a page: script and style bodies,
// comments, tags and attributes removed, whitespace collapsed. Two reads of the
// same page agree exactly when the text a reader would see is the same.
//
// Hashing the raw body could not do that job. On 2026-08-12 three consecutive
// reads of https://cloud.google.com/spot-vms/pricing — same URL, same
// User-Agent, seconds apart — returned three different SHA-256 sums while every
// price on the page was identical: each response carries a fresh CSP
// `nonce="…"` attribute and a fresh `FdrFJe` request id inside a script body.
// A raw-body comparison therefore reported ErrSourceUnstable on every run, so
// `make update-gcp-data` could not write a snapshot at all, and the committed
// catalogue drifted away from the source it claims to publish — n2-standard-4
// sat at $0.101336 against the $0.111472 the page served, 9% low, and nothing
// downstream could see it.
//
// The digest covers the whole page's text, not only its tables. Over-refusing
// is the safe direction — a run that waits costs a week of freshness, a run
// that mixes two price generations publishes a wrong savings figure — and the
// text was measured identical across five reads in one window and four reads
// spread over three minutes, while the raw hash changed on every single read.
//
// The manifest is unaffected: it keeps recording the raw body hash, which is
// the provenance of the exact bytes this run read.
func stabilityDigest(body []byte) string {
	text := scriptOrStyle.ReplaceAll(body, []byte(" "))
	text = htmlComment.ReplaceAll(text, []byte(" "))
	text = anyTag.ReplaceAll(text, []byte(" "))
	text = whitespaceRun.ReplaceAll(text, []byte(" "))

	return snapshot.SHA256Hex(bytes.TrimSpace(text))
}
