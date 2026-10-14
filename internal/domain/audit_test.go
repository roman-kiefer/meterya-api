package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditDetailsWhitelistAndMeaningfulActions(t *testing.T) {
	lookup := func(id, kind string) AuditReference { return AuditReference{ID: id, Name: "Name " + id, Kind: kind} }
	tests := []struct {
		kind    string
		payload map[string]any
		action  string
		want    string
	}{
		{"ReadingCorrected", map[string]any{"id": "r", "reason": "Tippfehler", "reading": map[string]any{"date": "2026-10-01", "value": "42.000000", "register_id": "import"}}, "ReadingCorrected", "42.000000"},
		{"TariffScheduleSaved", map[string]any{"unit_prices": []map[string]any{{"valid_from": "2026-01-01", "direction": "IMPORT", "cents": 35900}}}, "TariffScheduleSaved", "35.900"},
		{"WorkspaceMemberActivityChanged", map[string]any{"subject": "member", "active": false}, "MemberDeactivated", "false"},
		{"MeterTransitionRecorded", map[string]any{"old_point": "old", "new_points": []string{"next"}, "date": "2026-06-01"}, "MeterTransitionRecorded", "Name next"},
		{"VirtualPointSaved", map[string]any{"id": "v", "name": "Haus", "type": "SEQUENCE", "root_point": "old"}, "VirtualPointCreated", "Haus"},
		{"PowerToHeatConfigured", map[string]any{"id": "h", "name": "WP", "net_point": "net", "house_point": "house"}, "PowerToHeatConfigured", "Name house"},
		{"UnexpectedFutureEvent", map[string]any{"password": "secret"}, "UnknownEvent", ""},
		{"MemberInvited", map[string]any{"email": "invite@example.test", "role": "USER", "expires_at": "2026-10-20T10:00:00Z"}, "MemberInvited", "invite@example.test"},
		{"InvitationRetried", map[string]any{"email": "invite@example.test", "status": "PENDING"}, "InvitationRetried", "InvitationRetried"},
		{"InvitationRevoked", map[string]any{"email": "invite@example.test", "status": "REVOKED"}, "InvitationRevoked", "REVOKED"},
		{"InvitationDeleted", map[string]any{"email": "invite@example.test", "status": "REVOKED"}, "InvitationDeleted", "invite@example.test"},
		{"InvitationAccepted", map[string]any{"email": "invite@example.test", "subject": "accepted", "role": "USER"}, "InvitationAccepted", "invite@example.test"},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			test.payload["secret"] = "must-not-be-returned"
			test.payload["token_hash"] = "private-invitation-hash"
			raw, _ := json.Marshal(test.payload)
			entry := AuditEntry{ID: "event", Type: test.kind, Object: AuditReference{ID: "point"}}
			AuditDetails(&entry, raw, 1, lookup)
			result, _ := json.Marshal(entry)
			if entry.Action != test.action || !strings.Contains(string(result), test.want) || strings.Contains(string(result), "must-not-be-returned") || strings.Contains(string(result), "secret") || strings.Contains(string(result), "private-invitation-hash") {
				t.Fatal(string(result))
			}
		})
	}
}

func TestAuditMissingObjectsExplainOnlyKnownReasons(t *testing.T) {
	for _, status := range []string{"UNRESOLVED", "DELETED", "OUTSIDE_SCOPE"} {
		t.Run(status, func(t *testing.T) {
			e := AuditEntry{ID: "event", Type: "ReadingRecorded", Object: AuditReference{ID: "source", Kind: "point", Status: status}}
			AuditDetails(&e, []byte("{}"), 1, func(id, kind string) AuditReference { return AuditReference{ID: id, Kind: kind} })
			found := false
			for _, d := range e.Details {
				if d.Field == "object_status" && d.Value == status && d.Format == "explanation" {
					found = true
				}
			}
			if !found {
				t.Fatal(e)
			}
		})
	}
}
