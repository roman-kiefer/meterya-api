package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"strings"
	"testing"
)

func TestAuditPagingTypesRolesAndScopedResolution(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Home", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Bootstrap(ctx, "Private", "other", "other@example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'mod','mod@example.test','MODERATOR'),($1,'user','user@example.test','USER'),($1,'guest','guest@example.test','GUEST'),($2,'foreign','foreign@example.test','MODERATOR')", ws, other)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(workspace, kind, actor string, payload map[string]any) string {
		t.Helper()
		id := uuid.NewString()
		raw, _ := json.Marshal(payload)
		_, e := s.Pool.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,1,$3,$4,$5,$6)", id, uuid.NewString(), workspace, kind, actor, raw)
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	insert(other, "ForeignOnly", "foreign", map[string]any{"secret": "must-not-leak"})
	for i := 0; i < 55; i++ {
		insert(ws, "ReadingRecorded", "mod", map[string]any{"date": "2026-01-01", "value": "10"})
	}
	first, err := s.Audit(ctx, "mod", ws, "", "")
	if err != nil || len(first.Entries) != 50 || first.Next == "" {
		t.Fatal(first, err)
	}
	seen := map[string]bool{}
	for _, e := range first.Entries {
		seen[e.ID] = true
		if e.WorkspaceID != ws || e.Actor.Name != "mod@example.test" {
			t.Fatal(e)
		}
	}
	fresh := insert(ws, "ReadingRecorded", "mod", map[string]any{"value": "11"})
	second, err := s.Audit(ctx, "mod", ws, "", first.Next)
	if err != nil || len(second.Entries) != 6 || second.Next != "" {
		t.Fatal(second, err)
	}
	for _, e := range second.Entries {
		if seen[e.ID] || e.ID == fresh {
			t.Fatal("unstable page", e)
		}
	}
	filtered, err := s.Audit(ctx, "mod", ws, "WorkspaceCreated", "")
	if err != nil || len(filtered.Entries) != 1 || len(filtered.Types) != 2 || filtered.Entries[0].Actor.Kind != "operator" {
		t.Fatal(filtered, err)
	}
	for _, subject := range []string{"user", "guest", "foreign"} {
		if _, err = s.Audit(ctx, subject, ws, "", ""); err == nil {
			t.Fatal("unauthorized", subject)
		}
	}
	if _, err = s.Audit(ctx, "mod", "", "", ""); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	if _, err = s.Audit(ctx, "mod", other, "", ""); err == nil {
		t.Fatal("foreign access")
	}
	if _, err = s.Audit(ctx, "mod", ws, "ReadingRecorded", first.Next); domain.Code(err) != "VALIDATION_FAILED" {
		t.Fatal("cursor scope", err)
	}
	all, err := s.Audit(ctx, "admin", "", "ForeignOnly", "")
	if err != nil || len(all.Entries) != 1 || all.Entries[0].WorkspaceName != "Private" {
		t.Fatal(all, err)
	}
	moved := insert(ws, "PointWorkspaceMoved", "mod", map[string]any{"from_workspace_id": other, "to_workspace_id": ws})
	page, err := s.Audit(ctx, "mod", ws, "PointWorkspaceMoved", "")
	raw, _ := json.Marshal(page)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].ID != moved || strings.Contains(string(raw), "Private") || strings.Contains(string(raw), "foreign@example.test") {
		t.Fatal(string(raw), err)
	}
	removed := insert(ws, "ReadingRecorded", "missing-subject", map[string]any{"value": "12"})
	// An old event remains in its original workspace after the physical point moves.
	// Its current name/register and the actor's foreign membership must stay private.
	foreignPoint := uuid.NewString()
	foreignEvent := uuid.NewString()
	foreignState, _ := json.Marshal(map[string]any{"name": "Private meter", "meter": map[string]any{"serial": "foreign-meter", "registers": []any{}}})
	_, err = s.Pool.Exec(ctx, "INSERT INTO points(id,workspace_id,serial,version,state) VALUES($1,$2,'foreign-meter',1,$3)", foreignPoint, other, foreignState)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,1,$3,'ReadingRecorded','foreign','{}')", foreignEvent, foreignPoint, ws)
	if err != nil {
		t.Fatal(err)
	}
	privatePage, err := s.Audit(ctx, "mod", ws, "ReadingRecorded", "")
	privateJSON, _ := json.Marshal(privatePage)
	if err == nil && privatePage.Entries[0].Object.Status != "OUTSIDE_SCOPE" {
		t.Fatal("moved source must explain its scope", privatePage.Entries[0])
	}
	if err != nil || strings.Contains(string(privateJSON), "Private meter") || strings.Contains(string(privateJSON), "foreign@example.test") {
		t.Fatal("foreign reference leaked", string(privateJSON), err)
	}
	page, err = s.Audit(ctx, "mod", ws, "ReadingRecorded", "")
	if err != nil || page.Entries[1].ID != removed || page.Entries[1].Actor.Name != "" || page.Entries[1].Object.Name != "" {
		t.Fatal(page, err)
	}
}
