package domain

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"time"
)

// AuditEntry deliberately contains a presentation read model, never an event payload.
type AuditEntry struct {
	ID            string         `json:"id"`
	At            time.Time      `json:"occurred_at"`
	Type          string         `json:"event_type"`
	Action        string         `json:"action"`
	WorkspaceID   string         `json:"workspace_id"`
	WorkspaceName string         `json:"workspace_name"`
	Actor         AuditReference `json:"actor"`
	Object        AuditReference `json:"object"`
	Details       []AuditDetail  `json:"details"`
}
type AuditReference struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind"`
	Status string `json:"status,omitempty"`
}
type AuditDetail struct {
	Field  string `json:"field"`
	Value  string `json:"value"`
	Format string `json:"format"`
}
type AuditPage struct {
	Entries []AuditEntry `json:"entries"`
	Types   []string     `json:"event_types"`
	Next    string       `json:"next_cursor,omitempty"`
}

// AuditDetails extracts only known business fields. New event kinds must opt in.
func AuditDetails(e *AuditEntry, payload []byte, version int64, lookup func(string, string) AuditReference) {
	e.Action = e.Type
	e.Details = []AuditDetail{}
	var p map[string]json.RawMessage
	if json.Unmarshal(payload, &p) != nil {
		return
	}
	text := func(m map[string]json.RawMessage, key string) string {
		var value string
		if json.Unmarshal(m[key], &value) == nil {
			return value
		}
		return ""
	}
	add := func(field, value, format string) {
		if value != "" {
			e.Details = append(e.Details, AuditDetail{field, value, format})
		}
	}
	plain := func(key string) { add(key, text(p, key), "text") }
	reference := func(field, id, kind string) {
		if id == "" {
			return
		}
		r := lookup(id, kind)
		if r.Name != "" {
			format := "text"
			if kind == "register" {
				format = "register"
			}
			add(field, r.Name, format)
		} else {
			add(field, r.ID, "reference")
		}
	}
	reading := func(m map[string]json.RawMessage) {
		add("date", text(m, "date"), "date")
		add("value", text(m, "value"), "decimal")
		reference("register", text(m, "register_id"), "register")
	}
	switch e.Type {
	case "PointCreated":
		e.Object = AuditReference{ID: e.Object.ID, Name: text(p, "name"), Kind: "point"}
		plain("name")
		var meter map[string]json.RawMessage
		_ = json.Unmarshal(p["meter"], &meter)
		add("serial", text(meter, "serial"), "text")
		add("meter_type", text(meter, "type"), "meter_type")
	case "PointDetailsSaved", "PointRenamed":
		plain("name")
		plain("serial")
		if name := text(p, "name"); name != "" {
			e.Object.Name = name
		}
	case "PointArchived", "PointRestored", "PointPurged":
	case "ReadingRecorded":
		reading(p)
	case "ReadingPhotoAttached":
		add("date", text(p, "confirmed_date"), "date")
		add("value", text(p, "confirmed_value"), "decimal")
		add("reading", text(p, "reading_id"), "reference")
	case "ReadingCorrected", "ReadingRetracted":
		plain("reason")
		add("reading", text(p, "id"), "reference")
		var r map[string]json.RawMessage
		_ = json.Unmarshal(p["reading"], &r)
		reading(r)
	case "WorkspaceCreated", "WorkspaceRenamed":
		e.Object = lookup(e.Object.ID, "workspace")
		if name := text(p, "name"); name != "" {
			e.Object.Name = name
		}
		plain("name")
	case "WorkspaceMemberAdded", "WorkspaceMemberRoleChanged", "WorkspaceMemberActivityChanged", "WorkspaceMemberRemoved":
		e.Object = lookup(text(p, "subject"), "user")
		plain("email")
		add("role", text(p, "role"), "role")
		if value, ok := p["active"]; ok {
			var active bool
			if json.Unmarshal(value, &active) == nil {
				e.Action = "MemberDeactivated"
				if active {
					e.Action = "MemberActivated"
				}
				add("active", fmt.Sprint(active), "boolean")
			}
		}
	case "MeterTransitionRecorded":
		e.Object = lookup(text(p, "old_point"), "point")
		add("date", text(p, "date"), "date")
		reference("old_point", text(p, "old_point"), "point")
		var next []string
		_ = json.Unmarshal(p["new_points"], &next)
		for _, id := range next {
			reference("new_point", id, "point")
		}
		reference("continuation", text(p, "continuation_point"), "point")
	case "PowerToHeatConfigured", "PowerToHeatSourceChoicesSaved", "PowerToHeatTariffsSaved":
		e.Object = AuditReference{ID: text(p, "id"), Name: text(p, "name"), Kind: "heat"}
		add("valid_from", text(p, "valid_from"), "date")
		reference("net_point", text(p, "net_point"), "point")
		reference("house_point", text(p, "house_point"), "point")
		plain("name")
		if e.Type == "PowerToHeatSourceChoicesSaved" {
			auditChoices(p["choices"], reference)
		}
	case "VirtualPointSaved", "VirtualPointRemoved":
		e.Object = AuditReference{ID: text(p, "id"), Name: text(p, "name"), Kind: "virtual"}
		if e.Type == "VirtualPointSaved" {
			e.Action = "VirtualPointUpdated"
			if version == 1 {
				e.Action = "VirtualPointCreated"
			}
			if string(p["archived"]) == "true" {
				e.Action = "VirtualPointArchived"
			}
		}
		plain("name")
		add("type", text(p, "type"), "virtual_type")
		reference("root_point", text(p, "root_point"), "point")
		reference("heat", text(p, "heat_id"), "heat")
		auditChoices(p["choices"], reference)
	case "PointWorkspaceMoved":
		reference("from_workspace", text(p, "from_workspace_id"), "workspace")
		reference("to_workspace", text(p, "to_workspace_id"), "workspace")
	case "TariffScheduleSaved":
		var schedule struct {
			Unit []UnitPrice `json:"unit_prices"`
			Base []BasePrice `json:"base_prices"`
		}
		_ = json.Unmarshal(payload, &schedule)
		for _, price := range schedule.Unit {
			add("direction", price.Direction, "direction")
			add("valid_from", price.From, "date")
			add("valid_until", price.Until, "date")
			add("unit_price", new(big.Rat).SetFrac(big.NewInt(price.Cents), big.NewInt(1000)).FloatString(3), "ct")
		}
		for _, price := range schedule.Base {
			add("valid_from", price.From, "date")
			add("valid_until", price.Until, "date")
			add("base_price", fmt.Sprint(price.Cents), "cents")
			add("period", price.Period, "period")
		}
	case "MemberInvited", "InvitationRetried", "InvitationRevoked", "InvitationDeleted", "InvitationAccepted":
		e.Object = AuditReference{ID: e.Object.ID, Name: text(p, "email"), Kind: "invitation"}
		plain("email")
		add("role", text(p, "role"), "role")
		add("expires_at", text(p, "expires_at"), "timestamp")
		add("invitation_status", text(p, "status"), "invitation_status")
		add("context", e.Type, "explanation")
	case "MonthlyStatementsImported":
		// Full imported statements are intentionally not exposed.
	default:
		e.Action = "UnknownEvent"
		e.Object.Kind = "event"
		e.Object.Status = ""
		add("event_type", e.Type, "text")
		add("context", "UNKNOWN_EVENT", "explanation")
	}
	if e.Object.ID == "" {
		e.Object.ID = e.ID
	}
	if e.Object.Name == "" {
		if e.Type == "PointPurged" {
			e.Object.Status = "DELETED"
		}
		if e.Object.Status == "" {
			e.Object.Status = "UNRESOLVED"
		}
		add("object_status", e.Object.Status, "explanation")
		add("object_reference", e.Object.ID, "reference")
	}
}

func auditChoices(raw json.RawMessage, ref func(string, string, string)) {
	var choices map[string]string
	_ = json.Unmarshal(raw, &choices)
	// Sort map keys to make details and tests deterministic.
	keys := make([]string, 0, len(choices))
	for id := range choices {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		ref("old_point", id, "point")
		ref("continuation", choices[id], "point")
	}
}
