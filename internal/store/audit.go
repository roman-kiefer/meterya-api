package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"meterya.local/api/internal/domain"
)

type auditCursor struct {
	Scope  string `json:"scope"`
	Type   string `json:"type"`
	Before int64  `json:"before"`
}

// Audit uses a consistent snapshot; pagination never reuses offsets.
func (s *Store) Audit(ctx context.Context, subject, ws, eventType, cursor string) (domain.AuditPage, error) {
	out := domain.AuditPage{Entries: []domain.AuditEntry{}, Types: []string{}}
	if len(cursor) > 2048 || len(eventType) > 120 {
		return out, domain.Fail("VALIDATION_FAILED")
	}
	var scope any
	if ws != "" {
		scope = ws
	}
	var before int64
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		var c auditCursor
		if err != nil || json.Unmarshal(raw, &c) != nil || c.Scope != ws || c.Type != eventType || c.Before <= 0 {
			return out, domain.Fail("VALIDATION_FAILED")
		}
		before = c.Before
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var global bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM members WHERE subject=$1 AND role='ADMIN' AND active)", subject).Scan(&global); err != nil {
		return out, err
	}
	if ws == "" && !global {
		return out, domain.Fail("PERMISSION_DENIED")
	}
	if ws != "" {
		var role string
		if err = tx.QueryRow(ctx, roleQuery, ws, subject).Scan(&role); err != nil {
			return out, mapError(err)
		}
		if !CanManage(role) {
			return out, domain.Fail("PERMISSION_DENIED")
		}
	}
	// Resolvers are scoped too: moved objects and foreign workspace names are not leaked.
	names := map[string]domain.AuditReference{}
	pointStatus := map[string]string{}
	statusRows, err := tx.Query(ctx, "SELECT id::text FROM points WHERE $1::uuid IS NOT NULL AND workspace_id<>$1", scope)
	if err != nil {
		return out, err
	}
	for statusRows.Next() {
		var id string
		if err = statusRows.Scan(&id); err != nil {
			break
		}
		pointStatus[id] = "OUTSIDE_SCOPE"
	}
	if err == nil {
		err = statusRows.Err()
	}
	statusRows.Close()
	if err != nil {
		return out, err
	}
	statusRows, err = tx.Query(ctx, "SELECT aggregate_id::text FROM events WHERE event_type='PointPurged' AND ($1::uuid IS NULL OR workspace_id=$1)", scope)
	if err != nil {
		return out, err
	}
	for statusRows.Next() {
		var id string
		if err = statusRows.Scan(&id); err != nil {
			break
		}
		pointStatus[id] = "DELETED"
	}
	if err == nil {
		err = statusRows.Err()
	}
	statusRows.Close()
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,name FROM workspaces WHERE $1::uuid IS NULL OR id=$1 OR
 EXISTS(SELECT 1 FROM members WHERE workspace_id=workspaces.id AND subject=$2 AND active AND role='MODERATOR') AND $3`, scope, subject, !global)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			break
		}
		names["workspace:"+id] = domain.AuditReference{ID: id, Name: name, Kind: "workspace"}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	// Current names are resolved only inside the requested scope.
	rows, err = tx.Query(ctx, "SELECT id::text,state->>'name' FROM points WHERE $1::uuid IS NULL OR workspace_id=$1 UNION ALL SELECT id::text,state->>'name' FROM virtual_points WHERE ($1::uuid IS NULL OR workspace_id=$1) AND NOT deleted", scope)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			break
		}
		names["point:"+id] = domain.AuditReference{ID: id, Name: name, Kind: "point"}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, "SELECT r->>'id',r->>'direction',r->>'unit' FROM points CROSS JOIN LATERAL jsonb_array_elements(state->'meter'->'registers') r WHERE $1::uuid IS NULL OR workspace_id=$1", scope)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id, direction, unit string
		if err = rows.Scan(&id, &direction, &unit); err != nil {
			break
		}
		names["register:"+id] = domain.AuditReference{ID: id, Name: direction + "|" + unit, Kind: "register"}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, "SELECT subject,min(email) FROM members WHERE $1::uuid IS NULL OR workspace_id=$1 OR role='ADMIN' GROUP BY subject", scope)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			break
		}
		names["user:"+id] = domain.AuditReference{ID: id, Name: name, Kind: "user"}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, "SELECT DISTINCT ON (payload->>'subject') payload->>'subject',payload->>'email' FROM events WHERE event_type='WorkspaceMemberAdded' AND payload->>'email' IS NOT NULL AND ($1::uuid IS NULL OR workspace_id=$1) ORDER BY payload->>'subject',sequence DESC", scope)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			break
		}
		if _, ok := names["user:"+id]; !ok {
			names["user:"+id] = domain.AuditReference{ID: id, Name: name, Kind: "user"}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, "SELECT DISTINCT ON (payload->>'id') payload->>'id',payload->>'name' FROM events WHERE event_type IN ('PowerToHeatConfigured','PowerToHeatSourceChoicesSaved') AND payload->>'id' IS NOT NULL AND payload->>'name' IS NOT NULL AND ($1::uuid IS NULL OR workspace_id=$1) ORDER BY payload->>'id',sequence DESC", scope)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			break
		}
		names["heat:"+id] = domain.AuditReference{ID: id, Name: name, Kind: "heat"}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	lookup := func(id, kind string) domain.AuditReference {
		if r, ok := names[kind+":"+id]; ok {
			return r
		}
		status := ""
		if kind == "point" {
			status = pointStatus[id]
		}
		return domain.AuditReference{ID: id, Kind: kind, Status: status}
	}
	rows, err = tx.Query(ctx, "SELECT DISTINCT event_type FROM events WHERE $1::uuid IS NULL OR workspace_id=$1 ORDER BY event_type", scope)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var kind string
		if err = rows.Scan(&kind); err != nil {
			break
		}
		out.Types = append(out.Types, kind)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `SELECT e.id,e.sequence,e.occurred_at,e.event_type,e.aggregate_id,e.aggregate_version,e.workspace_id,w.name,e.actor,e.payload
 FROM events e JOIN workspaces w ON w.id=e.workspace_id
 WHERE ($1::uuid IS NULL OR e.workspace_id=$1) AND ($2='' OR e.event_type=$2) AND ($3::bigint=0 OR e.sequence<$3)
 ORDER BY e.sequence DESC LIMIT 51`, scope, eventType, before)
	if err != nil {
		return out, err
	}
	var last int64
	for rows.Next() {
		if len(out.Entries) == 50 {
			raw, _ := json.Marshal(auditCursor{ws, eventType, last})
			out.Next = base64.RawURLEncoding.EncodeToString(raw)
			break
		}
		var e domain.AuditEntry
		var aggregate, actor string
		var version int64
		var payload []byte
		if err = rows.Scan(&e.ID, &last, &e.At, &e.Type, &aggregate, &version, &e.WorkspaceID, &e.WorkspaceName, &actor, &payload); err != nil {
			break
		}
		e.Actor = lookup(actor, "user")
		if actor == "operator" {
			e.Actor.Kind = "operator"
			e.Actor.Name = ""
		}
		e.Object = lookup(aggregate, "point")
		// For a global page the resolver may see every workspace; for a workspace page it must not.
		domain.AuditDetails(&e, payload, version, lookup)
		out.Entries = append(out.Entries, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
