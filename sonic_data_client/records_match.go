package client

import (
	"encoding/json"
	"strings"
)

// RecordsMatcher implements the Matcher interface.
// It evaluates a Record against a list of Subscriptions and returns
// whether any subscription matched, which one, and how.
type RecordsMatcher struct {
	subs []Subscription
}

// NewRecordsMatcher creates a matcher for the given set of subscriptions.
func NewRecordsMatcher(subs []Subscription) *RecordsMatcher {
	return &RecordsMatcher{subs: subs}
}

// Match checks a Record against all subscriptions.
// Returns (true, index, how) on the first hit, or (false, -1, "") on miss.
func (m *RecordsMatcher) Match(r *Record) (ok bool, subIndex int, how string) {
	for i := range m.subs {
		if hit, reason := m.matchOne(r, &m.subs[i]); hit {
			// Apply ops filter last
			if !opsFilterPass(r, &m.subs[i]) {
				continue
			}
			return true, i, reason
		}
	}
	return false, -1, ""
}

// matchOne evaluates a single Record against a single Subscription.
func (m *RecordsMatcher) matchOne(r *Record, sub *Subscription) (bool, string) {
	// Step 1: Direct match — same DB, same table, key matches
	if r.DB == sub.DB {
		return m.directMatch(r, sub)
	}

	// Step 2: Correlation — subscriber asked for APPL_DB, record is from sairedis (or vice versa)
	if sub.DB == "APPL_DB" && r.DB == "ASIC_DB" {
		return m.correlateApplToSai(r, sub)
	}
	if sub.DB == "ASIC_DB" && r.DB == "APPL_DB" {
		return m.correlateSaiToAppl(r, sub)
	}

	return false, ""
}

// directMatch handles the case where the record's DB matches the subscription's DB.
func (m *RecordsMatcher) directMatch(r *Record, sub *Subscription) (bool, string) {
	if r.Table != sub.Table {
		return false, ""
	}

	// If subscriber specified no key, it's a table/type prefix match
	if sub.Key == "" {
		return true, "prefix"
	}

	// Exact key match
	if keysEqual(r, sub) {
		return true, "exact"
	}

	return false, ""
}

// keysEqual compares a Record's key against a Subscription's key,
// handling CIDR normalisation and JSON canonical form.
func keysEqual(r *Record, sub *Subscription) bool {
	rKey := r.Key
	sKey := sub.Key

	// For sairedis entry-type keys (JSON), both should already be normalised
	// by the parser. Just compare strings.
	if rKey == sKey {
		return true
	}

	// Try CIDR normalisation on both sides
	rNorm := normaliseCIDR(rKey)
	sNorm := normaliseCIDR(sKey)
	if rNorm == sNorm {
		return true
	}

	// Try IP normalisation
	rNorm = normaliseIP(rKey)
	sNorm = normaliseIP(sKey)
	if rNorm == sNorm {
		return true
	}

	return false
}

// CorrelationRule defines how an APPL_DB table maps to a SAI object type.
// Only entry-type SAI objects (those with JSON keys) can be correlated.
// OID-based objects (PORT, ACL, BUFFER, etc.) cannot be correlated from
// the rec files alone — the subscriber must use separate APPL_DB and ASIC_DB
// subscriptions for those.
type CorrelationRule struct {
	ApplTable  string // e.g. "ROUTE_TABLE"
	SaiType    string // e.g. "SAI_OBJECT_TYPE_ROUTE_ENTRY"
	MatchField string // JSON field in the SAI entry key to compare against the APPL_DB key

	// ApplKeyPos selects which colon-separated segment of the APPL_DB key
	// to use for comparison. -1 means use the entire key (default for ROUTE).
	// For NEIGH_TABLE keys like "Ethernet0:10.0.0.1", ApplKeyPos=1 extracts "10.0.0.1".
	// For FDB_TABLE keys like "Vlan100:00:11:22:33:44:55", ApplKeyPos=-1 and
	// ApplKeySep=":" with ApplKeySkip=1 extracts the MAC portion.
	ApplKeyPos int
}

// correlationRules is the static lookup table for APPL_DB ↔ ASIC_DB correlation.
// Adding a new entry-type correlation is a one-line change here.
var correlationRules = []CorrelationRule{
	{
		ApplTable:  "ROUTE_TABLE",
		SaiType:    "SAI_OBJECT_TYPE_ROUTE_ENTRY",
		MatchField: "dest",
		ApplKeyPos: -1, // full key is the prefix (e.g. "10.1.0.0/24")
	},
	{
		ApplTable:  "NEIGH_TABLE",
		SaiType:    "SAI_OBJECT_TYPE_NEIGHBOR_ENTRY",
		MatchField: "ip_address",
		ApplKeyPos: 1, // key is "Ethernet0:10.0.0.1" → take segment 1
	},
	{
		ApplTable:  "FDB_TABLE",
		SaiType:    "SAI_OBJECT_TYPE_FDB_ENTRY",
		MatchField: "mac_address",
		ApplKeyPos: 1, // key is "Vlan100:AA:BB:CC:DD:EE:FF" → take everything after first ":"
	},
	{
		ApplTable:  "INSEG_TABLE",
		SaiType:    "SAI_OBJECT_TYPE_INSEG_ENTRY",
		MatchField: "label",
		ApplKeyPos: -1,
	},
	{
		ApplTable:  "NAT_TABLE",
		SaiType:    "SAI_OBJECT_TYPE_NAT_ENTRY",
		MatchField: "nat_data",
		ApplKeyPos: -1,
	},
	{
		ApplTable:  "MCAST_FDB_TABLE",
		SaiType:    "SAI_OBJECT_TYPE_MCAST_FDB_ENTRY",
		MatchField: "mac_address",
		ApplKeyPos: 1,
	},
}

// extractApplKeyPart extracts the comparable portion from an APPL_DB key
// based on the rule's ApplKeyPos.
func extractApplKeyPart(key string, rule *CorrelationRule) string {
	if rule.ApplKeyPos < 0 {
		return key // use the whole key
	}
	// Split on ":" and take everything from the given position onward.
	// For NEIGH: "Ethernet0:10.0.0.1" → pos 1 → "10.0.0.1"
	// For FDB: "Vlan100:AA:BB:CC:DD:EE:FF" → pos 1 → "AA:BB:CC:DD:EE:FF"
	idx := -1
	for i := 0; i < rule.ApplKeyPos; i++ {
		next := strings.IndexByte(key[idx+1:], ':')
		if next < 0 {
			return key // not enough segments, use full key
		}
		idx += next + 1
	}
	return key[idx+1:]
}

// correlateApplToSai checks if a sairedis record matches an APPL_DB subscription
// using the correlation rules.
// Example: subscriber wants APPL_DB/ROUTE_TABLE/10.1.0.0/24,
// record is sairedis SAI_OBJECT_TYPE_ROUTE_ENTRY with dest=10.1.0.0/24.
func (m *RecordsMatcher) correlateApplToSai(r *Record, sub *Subscription) (bool, string) {
	for _, rule := range correlationRules {
		if sub.Table != rule.ApplTable {
			continue
		}
		if r.Table != rule.SaiType {
			continue
		}

		// If subscriber has no key, match all records of this SAI type
		if sub.Key == "" {
			return true, "correlation:" + rule.ApplTable
		}

		// Extract the match field from the sairedis JSON key
		val := extractJsonField(r.Key, rule.MatchField)
		if val == "" {
			continue
		}

		// Extract the comparable portion of the APPL_DB key
		applKey := extractApplKeyPart(sub.Key, &rule)

		// Compare the extracted SAI field against the APPL_DB key portion
		if valuesEqual(val, applKey) {
			return true, "correlation:" + rule.ApplTable + "." + rule.MatchField
		}
	}
	return false, ""
}

// correlateSaiToAppl checks if a swss record matches an ASIC_DB subscription
// using the correlation rules (reverse direction).
// Example: subscriber wants ASIC_DB/SAI_OBJECT_TYPE_ROUTE_ENTRY with dest=10.1.0.0/24,
// record is swss ROUTE_TABLE:10.1.0.0/24.
func (m *RecordsMatcher) correlateSaiToAppl(r *Record, sub *Subscription) (bool, string) {
	for _, rule := range correlationRules {
		if sub.Table != rule.SaiType {
			continue
		}
		if r.Table != rule.ApplTable {
			continue
		}

		// If subscriber has no key, match all records of the APPL_DB table
		if sub.Key == "" {
			return true, "reverse-correlation:" + rule.SaiType
		}

		// The subscriber's key is a SAI JSON key. Extract the match field.
		subVal := extractJsonField(sub.Key, rule.MatchField)
		if subVal == "" {
			continue
		}

		// Extract the comparable portion of the APPL_DB record's key
		applKey := extractApplKeyPart(r.Key, &rule)

		// Compare against the extracted SAI field
		if valuesEqual(applKey, subVal) {
			return true, "reverse-correlation:" + rule.SaiType + "." + rule.MatchField
		}
	}
	return false, ""
}

// extractJsonField parses a JSON string and returns the value of a specific field.
// Returns "" if the key is not JSON or the field is not found.
func extractJsonField(jsonKey string, field string) string {
	if len(jsonKey) == 0 || jsonKey[0] != '{' {
		return ""
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(jsonKey), &m); err != nil {
		return ""
	}
	return m[field]
}

// valuesEqual compares two values with normalisation (CIDR, IP).
func valuesEqual(a, b string) bool {
	if a == b {
		return true
	}
	if normaliseCIDR(a) == normaliseCIDR(b) {
		return true
	}
	if normaliseIP(a) == normaliseIP(b) {
		return true
	}
	return false
}

// opsFilterPass checks whether a record's operation passes the subscription's ops filter.
// If the subscription has no ops filter, everything passes.
func opsFilterPass(r *Record, sub *Subscription) bool {
	if len(sub.Ops) == 0 {
		return true // no filter, everything passes
	}

	// Special case: ops=E means "only failures"
	for _, op := range sub.Ops {
		if strings.EqualFold(op, "E") && r.Status != "" {
			return true
		}
		if strings.EqualFold(op, r.Op) {
			return true
		}
	}
	return false
}
