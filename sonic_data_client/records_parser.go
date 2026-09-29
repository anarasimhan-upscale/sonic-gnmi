package client

import (
	"encoding/json"
	"net"
	"sort"
	"strings"
	"time"

	log "github.com/golang/glog"
)

// RecordsParser implements the Parser interface for swss.rec and sairedis.rec lines.
type RecordsParser struct {
	// lastSairedis holds the most recently parsed sairedis record so that
	// a following E|<status> line can be attached to it.
	lastSairedis *Record

	// loc is the timezone used to parse the localtime timestamps in the rec files.
	// The gnmi container inherits the host timezone.
	loc *time.Location
}

// NewRecordsParser creates a parser using the given timezone for timestamp parsing.
// Pass time.Local if no override is configured.
func NewRecordsParser(loc *time.Location) *RecordsParser {
	return &RecordsParser{loc: loc}
}

// Parse dispatches to the swss or sairedis parser based on RawLine.Source.
func (p *RecordsParser) Parse(l RawLine) (*Record, bool) {
	if len(l.Line) == 0 {
		return nil, false
	}
	switch l.Source {
	case "swss":
		return p.parseSwss(l)
	case "sairedis":
		return p.parseSairedis(l)
	default:
		log.V(4).Infof("records_parser: unknown source %q", l.Source)
		return nil, false
	}
}

// recTimestampLayout is the format orchagent uses for rec file timestamps.
// Example: 2026-09-26.10:15:32.101234
const recTimestampLayout = "2006-01-02.15:04:05.000000"

// parseTimestamp parses the localtime timestamp from a rec file line.
func (p *RecordsParser) parseTimestamp(raw string) (time.Time, bool) {
	// Timestamps are exactly 26 characters: YYYY-MM-DD.HH:MM:SS.uuuuuu
	if len(raw) < 26 {
		return time.Time{}, false
	}
	ts, err := time.ParseInLocation(recTimestampLayout, raw[:26], p.loc)
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

// parseSwss parses a swss.rec line.
// Format: timestamp|TABLE:key|op|f:v|f:v|...
// Returns false for rotation banners, comments, and malformed lines.
func (p *RecordsParser) parseSwss(l RawLine) (*Record, bool) {
	line := l.Line

	// Skip rotation banners and comments
	if isSkippableLine(line) {
		return nil, false
	}

	parts := strings.Split(line, "|")
	if len(parts) < 3 {
		return nil, false
	}

	ts, ok := p.parseTimestamp(parts[0])
	if !ok {
		return nil, false
	}

	// Split TABLE:key on the first ":"
	tableKey := parts[1]
	colonIdx := strings.IndexByte(tableKey, ':')
	if colonIdx < 0 {
		return nil, false
	}
	table := tableKey[:colonIdx]
	key := tableKey[colonIdx+1:]

	op := parts[2]
	if op != "SET" && op != "DEL" {
		// Unrecognised op — still parse it, just pass through
		log.V(6).Infof("records_parser: swss unknown op %q", op)
	}

	// Parse field:value pairs from remaining parts
	fields := make(map[string]string)
	for _, fv := range parts[3:] {
		if fv == "" {
			continue
		}
		idx := strings.IndexByte(fv, ':')
		if idx < 0 {
			// No colon — store with empty value
			fields[fv] = ""
		} else {
			fields[fv[:idx]] = fv[idx+1:]
		}
	}

	r := &Record{
		Seq:    l.Seq,
		TS:     ts,
		Source: "swss",
		DB:     "APPL_DB",
		Table:  table,
		Key:    key,
		Op:     op,
		Fields: fields,
		Status: "",
		Raw:    line,
	}
	return r, true
}

// parseSairedis parses a sairedis.rec line.
// Format: timestamp|opcode|key|attr=val|attr=val|...
// Also handles E|<status> failure lines and # comments.
// Bulk opcodes (upper case C R S G B) may have || separators for multiple keys.
func (p *RecordsParser) parseSairedis(l RawLine) (*Record, bool) {
	line := l.Line

	if isSkippableLine(line) {
		return nil, false
	}

	parts := strings.Split(line, "|")
	if len(parts) < 2 {
		return nil, false
	}

	ts, ok := p.parseTimestamp(parts[0])
	if !ok {
		return nil, false
	}

	opcode := parts[1]

	// Handle E|<status> — failure line attached to the previous operation
	if opcode == "E" {
		return p.handleELine(l, ts, parts)
	}

	if len(parts) < 3 {
		return nil, false
	}

	rawKey := parts[2]

	// Extract SAI object type and the object key
	saiType, objKey := splitSaiKey(rawKey)
	if saiType == "" {
		return nil, false
	}

	// Parse attr=val pairs from remaining parts
	fields := make(map[string]string)

	// Check for bulk operations (upper case opcodes: C R S G B)
	isBulk := len(opcode) == 1 && opcode[0] >= 'A' && opcode[0] <= 'Z' && opcode[0] != 'E'

	if isBulk {
		// Bulk lines may contain || as separator for multiple keys.
		// Re-parse from the original line to capture all keys.
		bulkKeys := parseBulkKeys(line)
		if len(bulkKeys) > 1 {
			fields["_keys"] = strings.Join(bulkKeys, ",")
		}
		// Parse attrs from parts after the key
		for _, av := range parts[3:] {
			if av == "" {
				continue // skip empty parts from || splits
			}
			idx := strings.IndexByte(av, '=')
			if idx < 0 {
				continue
			}
			fields[av[:idx]] = av[idx+1:]
		}
	} else {
		for _, av := range parts[3:] {
			if av == "" {
				continue
			}
			idx := strings.IndexByte(av, '=')
			if idx < 0 {
				continue
			}
			fields[av[:idx]] = av[idx+1:]
		}
	}

	// Normalise the key for entry-type objects (JSON keys)
	normKey := normaliseEntryKey(objKey)

	r := &Record{
		Seq:    l.Seq,
		TS:     ts,
		Source: "sairedis",
		DB:     "ASIC_DB",
		Table:  saiType,
		Key:    normKey,
		Op:     opcode,
		Fields: fields,
		Status: "",
		Raw:    line,
	}

	// Keep a reference so the next E line can attach to it
	p.lastSairedis = r

	return r, true
}

// handleELine processes a sairedis E|<status> failure line.
// It attaches the status to the most recently parsed sairedis record.
func (p *RecordsParser) handleELine(l RawLine, ts time.Time, parts []string) (*Record, bool) {
	status := ""
	if len(parts) >= 3 {
		status = parts[2]
	}

	if p.lastSairedis == nil {
		// No preceding record to attach to — emit a standalone E record
		r := &Record{
			Seq:    l.Seq,
			TS:     ts,
			Source: "sairedis",
			DB:     "ASIC_DB",
			Table:  "",
			Key:    "",
			Op:     "E",
			Fields: map[string]string{"_response": "E"},
			Status: status,
			Raw:    l.Line,
		}
		return r, true
	}

	// Attach status to the previous sairedis record and re-emit it
	attached := *p.lastSairedis // shallow copy
	attached.Status = status
	attached.Seq = l.Seq // use the E line's seq
	if attached.Fields == nil {
		attached.Fields = make(map[string]string)
	}
	// Copy fields to avoid mutating the original
	newFields := make(map[string]string, len(attached.Fields)+1)
	for k, v := range attached.Fields {
		newFields[k] = v
	}
	newFields["_response"] = "E"
	attached.Fields = newFields

	// Clear lastSairedis so a second E line doesn't re-attach
	p.lastSairedis = nil

	return &attached, true
}

// splitSaiKey splits a sairedis key like "SAI_OBJECT_TYPE_ROUTE_ENTRY:{...}" into
// the type ("SAI_OBJECT_TYPE_ROUTE_ENTRY") and the object key ("{...}" or "oid:0x...").
func splitSaiKey(raw string) (saiType string, objKey string) {
	// Entry-type keys: SAI_OBJECT_TYPE_X:{json}
	// OID-type keys: SAI_OBJECT_TYPE_X:oid:0x...
	// Type alone (no key): SAI_OBJECT_TYPE_X

	idx := strings.Index(raw, ":{")
	if idx >= 0 {
		return raw[:idx], raw[idx+1:]
	}

	idx = strings.Index(raw, ":oid:")
	if idx >= 0 {
		return raw[:idx], raw[idx+1:]
	}

	// Might just be the type name with no colon
	if strings.HasPrefix(raw, "SAI_") {
		return raw, ""
	}

	return "", ""
}

// normaliseEntryKey normalises a JSON entry key into a canonical form.
// Fields are sorted alphabetically, whitespace is removed, and route
// prefixes / IPv6 addresses are canonicalised.
func normaliseEntryKey(key string) string {
	if len(key) == 0 || key[0] != '{' {
		return key // not a JSON key (e.g. oid:0x...), return as-is
	}

	var m map[string]string
	if err := json.Unmarshal([]byte(key), &m); err != nil {
		return key // can't parse, return as-is
	}

	// Normalise known fields
	if dest, ok := m["dest"]; ok {
		m["dest"] = normaliseCIDR(dest)
	}
	if ip, ok := m["ip_address"]; ok {
		m["ip_address"] = normaliseIP(ip)
	}

	// Sort keys for canonical ordering
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Rebuild JSON with sorted keys, no extra whitespace
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(m[k])
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.String()
}

// normaliseCIDR canonicalises a CIDR prefix (e.g. "10.1.0.0/24").
func normaliseCIDR(s string) string {
	_, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		return s
	}
	return ipnet.String()
}

// normaliseIP canonicalises an IP address (handles IPv6 shorthand).
func normaliseIP(s string) string {
	ip := net.ParseIP(s)
	if ip == nil {
		return s
	}
	return ip.String()
}

// parseBulkKeys extracts all keys from a bulk sairedis line.
// Bulk lines use || as separator between key groups.
func parseBulkKeys(line string) []string {
	// Split on || to get each key group
	groups := strings.Split(line, "||")
	var keys []string
	for _, g := range groups {
		parts := strings.Split(g, "|")
		// The key is field[2] in the first group, or field[0] in subsequent groups
		// (because || split removes the preceding |)
		for _, part := range parts {
			if strings.HasPrefix(part, "SAI_") {
				keys = append(keys, part)
				break
			}
		}
	}
	return keys
}

// isSkippableLine returns true for lines that should not be parsed:
// comments, empty lines, rotation banners, etc.
func isSkippableLine(line string) bool {
	if len(line) == 0 {
		return true
	}
	if line[0] == '#' {
		return true
	}
	// Rotation banner written by orchagent on SIGHUP
	if strings.HasPrefix(line, "SIGHUP") {
		return true
	}
	// Some versions write a banner like "Reopening log file"
	if strings.Contains(line, "Reopening log file") {
		return true
	}
	return false
}
