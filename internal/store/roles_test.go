package store

import (
	"context"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"testing"
)

func TestRoleBoundariesAndGlobalAccess(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Home", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateWorkspace(ctx, Command{Subject: "admin", Key: uuid.NewString(), Fingerprint: "new"}, "Other", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	join(t, s, ws, "moderator", "MODERATOR")
	join(t, s, ws, "guest", "GUEST")
	join(t, s, ws, "user", "USER")
	for _, sub := range []string{"moderator", "user", "guest"} {
		if _, err = s.Role(ctx, other.ID, sub); domain.Code(err) != "RESOURCE_NOT_FOUND" {
			t.Fatalf("cross-workspace access for %s: %v", sub, err)
		}
		visible, e := s.Workspaces(ctx, sub)
		if e != nil || len(visible) != 1 {
			t.Fatal(sub, visible, e)
		}
	}
	// Prove access to a workspace where the admin has no local membership.
	if _, err = s.Pool.Exec(ctx, "DELETE FROM members WHERE workspace_id=$1 AND subject='admin'", other.ID); err != nil {
		t.Fatal(err)
	}
	if role, e := s.Role(ctx, other.ID, "admin"); e != nil || role != "ADMIN" {
		t.Fatal(role, e)
	}
	if all, e := s.Workspaces(ctx, "admin"); e != nil || len(all) != 2 {
		t.Fatal(all, e)
	}
	memberships, e := s.WorkspaceMembers(ctx, ws, "moderator")
	if e != nil {
		t.Fatal(e)
	}
	command := func(subject string) Command {
		return Command{Workspace: ws, Subject: subject, Expected: memberships.Version, Key: uuid.NewString(), Fingerprint: uuid.NewString()}
	}
	if err = s.ManageMember(ctx, command("moderator"), "user", "ADMIN", false); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	if err = s.ManageMember(ctx, command("moderator"), "admin", "GUEST", false); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	if err = s.ManageMember(ctx, command("moderator"), "user", "GUEST", false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkspace(ctx, Command{Subject: "moderator", Key: uuid.NewString(), Fingerprint: "denied"}, "Forbidden", "moderator@example.test"); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	if _, err = s.Execute(ctx, Command{Workspace: ws, Subject: "guest", Key: uuid.NewString(), Fingerprint: "denied"}); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
}
