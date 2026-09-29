package client

import (
	"context"
	"time"
)

// Record is the structured representation of one line from swss.rec or sairedis.rec.
// WS3 (parser) produces these from raw text lines. WS1 (client) encodes them as gNMI Notifications.
type Record struct {
	Seq    string            // resumable cursor: "<source>:<inode>:<offset>"
	TS     time.Time         // parsed from the localtime stamp in the file
	Source string            // "swss" | "sairedis"
	DB     string            // "APPL_DB" | "ASIC_DB"
	Table  string            // ROUTE_TABLE, or SAI_OBJECT_TYPE_ROUTE_ENTRY for sairedis
	Key    string            // key as written in the file, after normalisation
	Op     string            // SET/DEL for swss, the sairedis opcode letter otherwise
	Fields map[string]string // field:value or attr=val pairs
	Status string            // filled from a following E| line, "" on success
	Raw    string            // the original line, for debugging
}

// RawLine is what the tailer (WS2) sends to the parser (WS3) through a channel.
// WS3 never opens files or deals with rotation — it just receives these.
type RawLine struct {
	Source string // "swss" | "sairedis" — tells the parser which format to expect
	Seq    string // cursor token filled by the tailer
	Line   string // the raw text line from the file, without trailing newline
}

// Tailer reads rec files from disk and pushes raw lines to a channel.
// WS2 owns the implementation. WS1 creates one per (namespace, source) pair.
type Tailer interface {
	// Run replays from `from` (zero = no replay) across rotated files, then tails
	// live until ctx is cancelled. Blocks on `out` when the consumer is slow.
	Run(ctx context.Context, from time.Time, out chan<- RawLine) error
}

// Parser turns a raw text line into a structured Record.
// WS3 owns the implementation. Returns false if the line should be skipped.
type Parser interface {
	Parse(l RawLine) (*Record, bool)
}

// Matcher checks whether a Record matches any active subscription.
// WS3 owns the implementation. Returns the matched subscription index and how it matched.
type Matcher interface {
	Match(r *Record) (ok bool, subIndex int, how string)
}

// Subscription is a parsed representation of one gNMI subscription path.
// WS1 parses the gNMI path into this struct and passes a slice to the Matcher.
type Subscription struct {
	Namespace string   // "localhost", "asic0", "asic1", ...
	DB        string   // "APPL_DB" | "ASIC_DB"
	Table     string   // e.g. "ROUTE_TABLE" or "SAI_OBJECT_TYPE_ROUTE_ENTRY"
	Key       string   // e.g. "10.1.0.0/24" or "" for whole-table subscription
	Ops       []string // e.g. ["SET","DEL"] or ["c","E"] or nil for all ops
}
