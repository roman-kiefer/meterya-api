package store

import (
	"context"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"testing"
)

func TestMemberActivityRevokesAccessAndReplays(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Home", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateWorkspace(ctx, Command{Subject: "admin", Key: uuid.NewString(), Fingerprint: "other"}, "Other", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	join(t, s, other.ID, "other-mod", "MODERATOR")
	join(t, s, ws, "mod", "MODERATOR")
	join(t, s, ws, "user", "USER")
	change := func(workspace, actor, target string, active bool) error {
		t.Helper()
		m, err := s.WorkspaceMembers(ctx, workspace, actor)
		if err != nil {
			return err
		}
		return s.SetMemberActive(ctx, Command{Workspace: workspace, Subject: actor, Expected: m.Version, Key: uuid.NewString(), Fingerprint: uuid.NewString()}, target, active)
	}
	if err := change(other.ID, "mod", "admin", false); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	if err := change(ws, "mod", "admin", false); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	if err := change(ws, "mod", "user", false); err != nil {
		t.Fatal(err)
	}
	checkInactive := func() {
		t.Helper()
		if _, err := s.Role(ctx, ws, "user"); domain.Code(err) != "RESOURCE_NOT_FOUND" {
			t.Fatalf("inactive user has access: %v", err)
		}
		if list, err := s.Workspaces(ctx, "user"); err != nil || len(list) != 0 {
			t.Fatal(list, err)
		}
		if _, err := s.Execute(ctx, Command{Workspace: ws, Subject: "user", Key: uuid.NewString(), Fingerprint: "denied"}); domain.Code(err) != "PERMISSION_DENIED" {
			t.Fatal(err)
		}
		members, err := s.WorkspaceMembers(ctx, ws, "admin")
		if err != nil {
			t.Fatal(err)
		}
		for _, member := range members.Members {
			if member.Subject == "user" && member.Active {
				t.Fatal("inactive state missing")
			}
		}
	}
	checkInactive()
	if err := s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	checkInactive()
	if err := change(ws, "mod", "user", true); err != nil {
		t.Fatal(err)
	}
	if role, err := s.Role(ctx, ws, "user"); err != nil || role != "USER" {
		t.Fatal(role, err)
	}
	// Inactive administrator grants must not count towards the last-admin guard.
	if err := change(other.ID, "admin", "admin", false); err != nil {
		t.Fatal(err)
	}
	if err := change(ws, "admin", "admin", false); domain.Code(err) != "LAST_WORKSPACE_ADMIN" {
		t.Fatal(err)
	}
	if err := change(ws, "admin", "mod", false); err != nil {
		t.Fatal(err)
	}
	if yes, err := s.IsAdmin(ctx, "mod"); err != nil || yes {
		t.Fatal(yes, err)
	}
	if err := change(ws, "admin", "admin", false); domain.Code(err) != "LAST_WORKSPACE_ADMIN" {
		t.Fatal(err)
	}
}
