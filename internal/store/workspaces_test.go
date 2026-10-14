package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"sync"
	"testing"
)

// join records the same two events as an accepted invitation, without sending email.
func join(t *testing.T, s *Store, ws, subject, role string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var version int64
	if err = tx.QueryRow(ctx, "SELECT version FROM workspaces WHERE id=$1 FOR UPDATE", ws).Scan(&version); err != nil {
		t.Fatal(err)
	}
	email := subject + "@example.test"
	if _, err = tx.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,$2,$3,$4)", ws, subject, email, role); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"workspace_id": ws, "subject": subject, "email": email, "role": role})
	if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,1,$3,'InvitationAccepted',$4,$5)", uuid.NewString(), uuid.NewString(), ws, subject, body); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$2,'WorkspaceMemberAdded',$4,$5)", uuid.NewString(), ws, version+1, subject, body); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE workspaces SET version=version+1 WHERE id=$1", ws); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceManagementIsIsolatedAndReplayable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Home", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	join(t, s, ws, "user", "USER")
	c := Command{Subject: "user", Key: uuid.NewString(), Fingerprint: "create"}
	if _, err = s.CreateWorkspace(ctx, c, "Other", "user@example.test"); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	c.Subject = "admin"
	created, err := s.CreateWorkspace(ctx, c, " Rental ", "admin@example.test")
	if err != nil || created.Name != "Rental" || created.Role != "ADMIN" {
		t.Fatal(created, err)
	}
	repeat, err := s.CreateWorkspace(ctx, c, "Rental", "admin@example.test")
	if err != nil || repeat.ID != created.ID {
		t.Fatal(repeat, err)
	}
	c.Fingerprint = "different"
	if _, err = s.CreateWorkspace(ctx, c, "Different", "admin@example.test"); domain.Code(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(err)
	}
	if visible, err := s.Workspaces(ctx, "user"); err != nil || len(visible) != 1 || visible[0].ID != ws {
		t.Fatal(visible, err)
	}
	if _, err = s.WorkspaceMembers(ctx, ws, "user"); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	if _, err = s.WorkspaceMembers(ctx, created.ID, "user"); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	command := func(expected int64) Command {
		return Command{Workspace: ws, Subject: "admin", Expected: expected, Key: uuid.NewString(), Fingerprint: uuid.NewString()}
	}
	if err = s.ManageMember(ctx, command(2), "admin", "USER", false); domain.Code(err) != "LAST_WORKSPACE_ADMIN" {
		t.Fatal(err)
	}
	if err = s.ManageMember(ctx, command(2), "admin", "", true); domain.Code(err) != "LAST_WORKSPACE_ADMIN" {
		t.Fatal(err)
	}
	promotion := command(2)
	if err = s.ManageMember(ctx, promotion, "user", "ADMIN", false); err != nil {
		t.Fatal(err)
	}
	if err = s.ManageMember(ctx, promotion, "user", "ADMIN", false); err != nil {
		t.Fatal(err)
	}
	if err = s.ManageMember(ctx, command(2), "user", "USER", false); domain.Code(err) != "AGGREGATE_VERSION_CONFLICT" {
		t.Fatal(err)
	}
	if err = s.ManageMember(ctx, command(3), "user", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Role(ctx, ws, "user"); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal(err)
	}
	before, err := s.WorkspaceMembers(ctx, ws, "admin")
	if err != nil || before.Version != 4 || len(before.Members) != 1 {
		t.Fatal(before, err)
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := s.WorkspaceMembers(ctx, ws, "admin")
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if err != nil || string(a) != string(b) {
		t.Fatal("membership replay differs", before, after, err)
	}
	// Rejoining is a later event and must survive replay even after earlier removal.
	join(t, s, ws, "user", "USER")
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if role, err := s.Role(ctx, ws, "user"); err != nil || role != "USER" {
		t.Fatal(role, err)
	}
	if role, err := s.Role(ctx, created.ID, "admin"); err != nil || role != "ADMIN" {
		t.Fatal(role, err)
	}
}

func TestConcurrentAdminRemovalKeepsOneAdmin(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Home", "a", "a@example.test")
	if err != nil {
		t.Fatal(err)
	}
	join(t, s, ws, "b", "ADMIN")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, actor := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- s.ManageMember(ctx, Command{Workspace: ws, Subject: actor, Expected: 2, Key: uuid.NewString(), Fingerprint: actor}, actor, "USER", false)
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if domain.Code(e) != "AGGREGATE_VERSION_CONFLICT" && domain.Code(e) != "LAST_WORKSPACE_ADMIN" {
			t.Fatal(e)
		}
	}
	var count int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM members WHERE workspace_id=$1 AND role='ADMIN'", ws).Scan(&count); err != nil || success != 1 || count != 1 {
		t.Fatal(success, count, err)
	}
}

func TestHistoricalInvitationAllowsFreshInvite(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Home", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	insert := func(status string) error {
		_, e := s.Pool.Exec(ctx, "INSERT INTO invitations(id,workspace_id,email,role,token_hash,status,expires_at,actor) VALUES($1,$2,'user@example.test','USER',$3,$4,now()+interval '7 days','admin')", uuid.NewString(), ws, uuid.NewString(), status)
		return e
	}
	if err = insert("ACCEPTED"); err != nil {
		t.Fatal(err)
	}
	if err = insert("REVOKED"); err != nil {
		t.Fatal(err)
	}
	if err = insert("PENDING"); err != nil {
		t.Fatal(err)
	}
	if err = insert("SENT"); domain.Code(mapError(err)) != "CONFLICT" {
		t.Fatal(err)
	}
}
