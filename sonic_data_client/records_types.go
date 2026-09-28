package client

import "time"

// Record is one orchagent/sairedis recorder line, ready for gNMI encoding.
// See the RECORDS target design: disk is the queue; each matched line becomes
// one Notification with this payload as JSON_IETF TypedValue.
type Record struct {
	Seq       string            `json:"seq"`
	TS        time.Time         `json:"ts"`
	Source    string            `json:"source"` // "swss" | "sairedis"
	DB        string            `json:"db"`     // "APPL_DB" | "ASIC_DB"
	Table     string            `json:"table"`
	Key       string            `json:"key"`
	Op        string            `json:"op"`
	Fields    map[string]string `json:"fields"`
	Status    string            `json:"status"`
	Raw       string            `json:"raw,omitempty"`
	MatchedBy string            `json:"matched_by,omitempty"`
}
