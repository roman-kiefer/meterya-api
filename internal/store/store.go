package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"meterya.local/api/internal/domain"
	"sort"
	"time"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct{ Pool *pgxpool.Pool }
type Member struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Version int64  `json:"version"`
}

func Open(ctx context.Context, url string) (*Store, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var gooseTable, releaseTable, workspacesTable bool
	if err = db.QueryRowContext(ctx, "SELECT to_regclass('goose_db_version') IS NOT NULL, to_regclass('release_schema') IS NOT NULL, to_regclass('workspaces') IS NOT NULL").Scan(&gooseTable, &releaseTable, &workspacesTable); err != nil {
		return nil, err
	}
	if (gooseTable || workspacesTable) && !releaseTable {
		return nil, fmt.Errorf("unsupported development database: create a fresh database for the first release; existing data is not deleted")
	}
	if releaseTable {
		var version int
		if err = db.QueryRowContext(ctx, "SELECT version FROM release_schema").Scan(&version); err != nil {
			return nil, fmt.Errorf("unsupported release database schema: %w", err)
		}
		if version != 1 {
			return nil, fmt.Errorf("unsupported release database schema version %d", version)
		}
	}
	goose.SetBaseFS(migrations)
	if err = goose.SetDialect("postgres"); err != nil {
		return nil, err
	}
	if err = goose.UpContext(ctx, db, "migrations"); err != nil {
		return nil, err
	}
	p, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, err
	}
	s := &Store{p}
	if err = s.warmAnnualProjections(ctx); err != nil {
		p.Close()
		return nil, err
	}
	return s, nil
}
func Hash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Fail("RESOURCE_NOT_FOUND")
	}
	var e *pgconn.PgError
	if errors.As(err, &e) && e.Code == "23505" {
		return domain.Fail("CONFLICT")
	}
	return err
}
func (s *Store) Workspaces(ctx context.Context, sub string) ([]Member, error) {
	rows, err := s.Pool.Query(ctx, "SELECT w.id,w.name,CASE WHEN EXISTS(SELECT 1 FROM members WHERE active AND subject=$1 AND role='ADMIN') THEN 'ADMIN' ELSE m.role END,w.version FROM workspaces w LEFT JOIN members m ON m.workspace_id=w.id AND m.subject=$1 AND m.active WHERE m.subject IS NOT NULL OR EXISTS(SELECT 1 FROM members WHERE active AND subject=$1 AND role='ADMIN') ORDER BY w.name", sub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Member{}
	for rows.Next() {
		var m Member
		if err = rows.Scan(&m.ID, &m.Name, &m.Role, &m.Version); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
func (s *Store) Role(ctx context.Context, workspace, sub string) (string, error) {
	var role string
	err := s.Pool.QueryRow(ctx, roleQuery, workspace, sub).Scan(&role)
	return role, mapError(err)
}
func (s *Store) IsAdmin(ctx context.Context, sub string) (bool, error) {
	var yes bool
	err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM members WHERE active AND subject=$1 AND role IN ('ADMIN','MODERATOR'))", sub).Scan(&yes)
	return yes, err
}
func (s *Store) Points(ctx context.Context, ws string) ([]domain.Point, error) {
	rows, err := s.Pool.Query(ctx, "SELECT state FROM points WHERE workspace_id=$1 ORDER BY state->>'name'", ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Point{}
	for rows.Next() {
		var b []byte
		var p domain.Point
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		sort.Slice(p.Readings, func(i, j int) bool { return p.Readings[i].Date > p.Readings[j].Date })
		result = append(result, p)
	}
	return result, rows.Err()
}
func (s *Store) Point(ctx context.Context, ws, id string) (domain.Point, error) {
	var p domain.Point
	var b []byte
	err := s.Pool.QueryRow(ctx, "SELECT state FROM points WHERE workspace_id=$1 AND id=$2", ws, id).Scan(&b)
	if err != nil {
		return p, mapError(err)
	}
	err = json.Unmarshal(b, &p)
	return p, err
}

type Command struct {
	PhotoEdits                           []domain.PhotoEdit
	EditedPhoto                          []byte
	PhotoID, AttachReadingID             string
	ReadingChange                        *domain.ReadingChange
	Workspace, Subject, Key, Fingerprint string
	Expected                             int64
	PointID                              string
	NewPoint                             *domain.Point
	Reading                              *domain.Reading
	Tariffs                              *domain.Tariffs
	Name                                 *string
	Serial                               *string
	Archived                             *bool
	Today                                string
	ReplaceImportTariffs                 bool
}

func (s *Store) Execute(ctx context.Context, c Command) (domain.Point, error) {
	var p domain.Point
	if c.PhotoID != "" && (c.Reading == nil && c.AttachReadingID == "" || c.NewPoint != nil || c.Tariffs != nil || c.Name != nil || c.Serial != nil || c.Archived != nil || c.ReadingChange != nil) {
		return p, domain.Fail("VALIDATION_FAILED")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	var role string
	if err = tx.QueryRow(ctx, roleQuery+" FOR SHARE", c.Workspace, c.Subject).Scan(&role); err != nil {
		return p, domain.Fail("PERMISSION_DENIED")
	}
	if (c.NewPoint != nil || c.Tariffs != nil || c.Name != nil || c.Serial != nil || c.Archived != nil) && !CanManage(role) {
		return p, domain.Fail("PERMISSION_DENIED")
	}
	if role == "GUEST" {
		return p, domain.Fail("PERMISSION_DENIED")
	}
	// Serialize repeated requests before looking up the response.
	_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", c.Subject+":"+c.Key)
	if err != nil {
		return p, err
	}
	var fingerprint string
	var saved []byte
	err = tx.QueryRow(ctx, "SELECT fingerprint,response FROM idempotency WHERE subject=$1 AND key=$2", c.Subject, c.Key).Scan(&fingerprint, &saved)
	if err == nil {
		if fingerprint != c.Fingerprint {
			return p, domain.Fail("IDEMPOTENCY_CONFLICT")
		}
		var receipt struct {
			Purged bool `json:"purged"`
		}
		if err = json.Unmarshal(saved, &receipt); err != nil {
			return p, err
		}
		if receipt.Purged {
			return p, domain.Fail("RESOURCE_NOT_FOUND")
		}
		err = json.Unmarshal(saved, &p)
		if err == nil {
			var present bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM points WHERE id=$1 AND workspace_id=$2)", p.ID, c.Workspace).Scan(&present); err != nil {
				return p, err
			}
			if !present {
				return p, domain.Fail("RESOURCE_NOT_FOUND")
			}
		}
		return p, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return p, err
	}
	var payload []byte
	var photo *domain.Photo
	var photoReading domain.Reading
	kind := "PointCreated"
	if c.NewPoint != nil {
		if !CanManage(role) {
			return p, domain.Fail("PERMISSION_DENIED")
		}
		p = *c.NewPoint
		p.Version = 1
		payload, _ = json.Marshal(p)
	} else {
		var state []byte
		if err = tx.QueryRow(ctx, "SELECT state FROM points WHERE id=$1 AND workspace_id=$2 FOR UPDATE", c.PointID, c.Workspace).Scan(&state); err != nil {
			return p, mapError(err)
		}
		if err = json.Unmarshal(state, &p); err != nil {
			return p, err
		}
		if p.Version != c.Expected {
			return p, domain.Fail("AGGREGATE_VERSION_CONFLICT")
		}
		if c.Serial != nil {
			if c.Name == nil || c.Archived != nil {
				return p, domain.Fail("VALIDATION_FAILED")
			}
			if err = p.ChangeDetails(*c.Name, *c.Serial); err != nil {
				return p, err
			}
			kind = "PointDetailsSaved"
			payload, _ = json.Marshal(map[string]string{"name": p.Name, "serial": p.Meter.Serial})
		} else if c.Name != nil || c.Archived != nil {
			if !CanManage(role) {
				return p, domain.Fail("PERMISSION_DENIED")
			}
			kind, err = p.Manage(c.Name, c.Archived)
			if err != nil {
				return p, err
			}
			payload, _ = json.Marshal(map[string]string{"name": p.Name})
		} else if c.ReadingChange != nil {
			if !c.ReadingChange.Retract {
				if err = readingWithinMeterPeriod(ctx, tx, c.Workspace, p.ID, c.ReadingChange.Date); err != nil {
					return p, err
				}
			}
			event, changeErr := p.ChangeReading(*c.ReadingChange, c.Subject, role, c.Today)
			if changeErr != nil {
				return p, changeErr
			}
			kind = "ReadingCorrected"
			if c.ReadingChange.Retract {
				kind = "ReadingRetracted"
			}
			payload, _ = json.Marshal(event)
		} else if c.AttachReadingID != "" {
			if c.PhotoID == "" {
				return p, domain.Fail("VALIDATION_FAILED")
			}
			found := false
			for _, reading := range p.Readings {
				if reading.ID == c.AttachReadingID {
					if c.Reading != nil && (c.Reading.RegisterID != reading.RegisterID || c.Reading.Date != reading.Date || c.Reading.Value != reading.Value) {
						return p, domain.Fail("PHOTO_READING_MISMATCH")
					}
					if !CanManage(role) && reading.Actor != c.Subject {
						return p, domain.Fail("PERMISSION_DENIED")
					}
					photoReading = reading
					found = true
					break
				}
			}
			if !found {
				return p, domain.Fail("RESOURCE_NOT_FOUND")
			}
			p.Version++
			kind = "ReadingPhotoAttached"
		} else if p.Archived {
			return p, domain.Fail("POINT_ARCHIVED")
		} else if c.Tariffs != nil {
			if !CanManage(role) {
				return p, domain.Fail("PERMISSION_DENIED")
			}
			if err = p.ValidateTariffs(*c.Tariffs); err != nil {
				return p, err
			}
			p.Tariffs = *c.Tariffs
			p.Version++
			payload, _ = json.Marshal(c.Tariffs)
			kind = "TariffScheduleSaved"
		} else {
			if c.Reading == nil {
				return p, domain.Fail("VALIDATION_FAILED")
			}
			if err = readingWithinMeterPeriod(ctx, tx, c.Workspace, p.ID, c.Reading.Date); err != nil {
				return p, err
			}
			if err = p.ValidateReading(*c.Reading, c.Today); err != nil {
				return p, err
			}
			p.Version++
			p.Readings = append(p.Readings, *c.Reading)
			payload, _ = json.Marshal(c.Reading)
			kind = "ReadingRecorded"
			photoReading = *c.Reading
		}
	}
	if c.PhotoID != "" {
		linked, e := bindPhoto(ctx, tx, c, photoReading)
		if e != nil {
			return p, e
		}
		photo = &linked
		if c.AttachReadingID != "" {
			payload, _ = json.Marshal(photo)
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$4,$5,$6,$7)", uuid.NewString(), p.ID, p.Version, c.Workspace, kind, c.Subject, payload); err != nil {
		return p, mapError(err)
	}
	if photo != nil && c.AttachReadingID == "" {
		p.Version++
		body, _ := json.Marshal(photo)
		if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$4,'ReadingPhotoAttached',$5,$6)", uuid.NewString(), p.ID, p.Version, c.Workspace, c.Subject, body); err != nil {
			return p, err
		}
	}
	state, _ := json.Marshal(p)
	if c.NewPoint != nil {
		_, err = tx.Exec(ctx, "INSERT INTO points(id,workspace_id,serial,version,state,archived) VALUES($1,$2,$3,$4,$5,$6)", p.ID, c.Workspace, p.Meter.Serial, p.Version, state, p.Archived)
	} else {
		_, err = tx.Exec(ctx, "UPDATE points SET state=$1,version=$2,archived=$4,serial=$5 WHERE id=$3", state, p.Version, p.ID, p.Archived, p.Meter.Serial)
	}
	if err != nil {
		var duplicate *pgconn.PgError
		if errors.As(err, &duplicate) && duplicate.Code == "23505" && duplicate.ConstraintName == "points_active_serial" {
			return p, domain.Fail("SERIAL_ALREADY_ACTIVE")
		}
		return p, mapError(err)
	}
	if c.Reading != nil {
		if err = insertReading(ctx, tx, p.ID, *c.Reading); err != nil {
			return p, mapError(err)
		}
	}
	if c.ReadingChange != nil {
		if _, err = tx.Exec(ctx, "DELETE FROM readings WHERE point_id=$1 AND id=$2", p.ID, c.ReadingChange.ID); err != nil {
			return p, err
		}
		for _, r := range p.Readings {
			if r.ID == c.ReadingChange.ID {
				if err = insertReading(ctx, tx, p.ID, r); err != nil {
					return p, mapError(err)
				}
			}
		}
	}
	if err = replaceAnnualProjections(ctx, tx, p); err != nil {
		return p, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO idempotency(subject,key,fingerprint,response) VALUES($1,$2,$3,$4)", c.Subject, c.Key, c.Fingerprint, state); err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}
func (s *Store) Bootstrap(ctx context.Context, name, sub, email string) (string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	id := uuid.NewString()
	_, err = tx.Exec(ctx, "INSERT INTO workspaces(id,name) VALUES($1,$2)", id, name)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,$2,$3,'ADMIN')", id, sub, email)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"id": id, "name": name, "subject": sub, "email": email})
	_, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,1,$2,'WorkspaceCreated','operator',$3)", uuid.NewString(), id, body)
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}
func (s *Store) Rebuild(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Lock before reading a stable snapshot so concurrent commands cannot be lost.
	if _, err = tx.Exec(ctx, "LOCK TABLE workspaces,members,points,readings,annual_summaries,virtual_points,photos IN EXCLUSIVE MODE"); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT aggregate_id,aggregate_version,event_type,payload FROM events ORDER BY sequence")
	if err != nil {
		return err
	}
	points := map[string]domain.Point{}
	virtuals := map[string]domain.VirtualPoint{}
	photoDocuments := map[string]domain.Photo{}
	type workspaceState struct {
		Name    string
		Version int64
	}
	workspaces := map[string]workspaceState{}
	type membership struct {
		Workspace, Subject, Email, Role string
		Active                          bool
	}
	members := map[string]membership{}
	for rows.Next() {
		var id, kind string
		var version int64
		var body []byte
		if err = rows.Scan(&id, &version, &kind, &body); err != nil {
			rows.Close()
			return err
		}
		switch kind {
		case "WorkspaceCreated":
			var v struct{ ID, Name, Subject, Email string }
			if err = json.Unmarshal(body, &v); err != nil {
				rows.Close()
				return err
			}
			members[v.ID+":"+v.Subject] = membership{v.ID, v.Subject, v.Email, "ADMIN", true}
			workspaces[id] = workspaceState{v.Name, version}
		case "WorkspaceRenamed":
			var v struct{ Name string }
			if err = json.Unmarshal(body, &v); err != nil {
				rows.Close()
				return err
			}
			if _, ok := workspaces[id]; !ok {
				rows.Close()
				return errors.New("rename without workspace")
			}
			workspaces[id] = workspaceState{v.Name, version}
		case "MeterTransitionRecorded", "PowerToHeatConfigured", "PowerToHeatTariffsSaved", "PowerToHeatSourceChoicesSaved":
			ws, ok := workspaces[id]
			if !ok {
				rows.Close()
				return errors.New("transition without workspace")
			}
			ws.Version = version
			workspaces[id] = ws
		case "InvitationAccepted":
			var v struct {
				Workspace            string `json:"workspace_id"`
				Subject, Email, Role string
			}
			if err = json.Unmarshal(body, &v); err != nil {
				rows.Close()
				return err
			}
			members[v.Workspace+":"+v.Subject] = membership{v.Workspace, v.Subject, v.Email, v.Role, true}
		case "WorkspaceMemberAdded", "WorkspaceMemberRoleChanged", "WorkspaceMemberRemoved", "WorkspaceMemberActivityChanged":
			var v struct {
				Workspace            string `json:"workspace_id"`
				Subject, Email, Role string
				Active               bool `json:"active"`
			}
			if err = json.Unmarshal(body, &v); err != nil {
				rows.Close()
				return err
			}
			key := v.Workspace + ":" + v.Subject
			if kind == "WorkspaceMemberRemoved" {
				delete(members, key)
			} else if kind == "WorkspaceMemberAdded" {
				members[key] = membership{v.Workspace, v.Subject, v.Email, v.Role, true}
			} else {
				m, ok := members[key]
				if !ok {
					rows.Close()
					return errors.New("role change without member")
				}
				if kind == "WorkspaceMemberActivityChanged" {
					m.Active = v.Active
				} else {
					m.Role = v.Role
				}
				members[key] = m
			}
			ws, ok := workspaces[v.Workspace]
			if !ok {
				rows.Close()
				return errors.New("membership change without workspace")
			}
			ws.Version = version
			workspaces[v.Workspace] = ws
		case "VirtualPointSaved", "VirtualPointRemoved":
			var v domain.VirtualPoint
			if err = json.Unmarshal(body, &v); err != nil {
				rows.Close()
				return err
			}
			virtuals[id] = v
		case "PointCreated":
			var p domain.Point
			if err = json.Unmarshal(body, &p); err != nil {
				rows.Close()
				return err
			}
			points[id] = p
		case "PointWorkspaceMoved":
			p, ok := points[id]
			if !ok {
				rows.Close()
				return errors.New("move without point")
			}
			var v struct {
				From string `json:"from_workspace_id"`
				To   string `json:"to_workspace_id"`
			}
			if err = json.Unmarshal(body, &v); err != nil {
				rows.Close()
				return err
			}
			if p.WorkspaceID != v.From {
				rows.Close()
				return errors.New("move source does not match point")
			}
			if _, ok := workspaces[v.To]; !ok {
				rows.Close()
				return errors.New("move target does not exist")
			}
			p.WorkspaceID, p.Version = v.To, version
			points[id] = p
		case "ReadingRecorded":
			p, ok := points[id]
			if !ok {
				rows.Close()
				return errors.New("reading without point")
			}
			var r domain.Reading
			if err = json.Unmarshal(body, &r); err != nil {
				rows.Close()
				return err
			}
			p.Readings = append(p.Readings, r)
			p.Version = version
			points[id] = p
		case "ReadingCorrected", "ReadingRetracted":
			p, ok := points[id]
			if !ok {
				rows.Close()
				return errors.New("reading change without point")
			}
			var event domain.ReadingChangeApplied
			if err = json.Unmarshal(body, &event); err != nil {
				rows.Close()
				return err
			}
			if err = p.ApplyReadingChange(event); err != nil {
				rows.Close()
				return err
			}
			p.Version = version
			points[id] = p
		case "ReadingPhotoAttached":
			p, ok := points[id]
			if !ok {
				return errors.New("photo without point")
			}
			var photo domain.Photo
			if err = json.Unmarshal(body, &photo); err != nil {
				return err
			}
			if photo.PointID != id {
				return errors.New("photo point mismatch")
			}
			if photoDocuments[photo.ID].ID != "" {
				return errors.New("photo attached twice")
			}
			photoDocuments[photo.ID] = photo
			p.Version = version
			points[id] = p
		case "PointPurged":
			delete(points, id)
		case "PointRenamed", "PointDetailsSaved", "PointArchived", "PointRestored":
			p, ok := points[id]
			if !ok {
				rows.Close()
				return errors.New("management event without point")
			}
			if kind == "PointRenamed" || kind == "PointDetailsSaved" {
				var v struct{ Name, Serial string }
				if err = json.Unmarshal(body, &v); err != nil {
					rows.Close()
					return err
				}
				p.Name = v.Name
				if kind == "PointDetailsSaved" {
					p.Meter.Serial = v.Serial
				}
			} else {
				p.Archived = kind == "PointArchived"
			}
			p.Version = version
			points[id] = p
		case "TariffScheduleSaved":
			p, ok := points[id]
			if !ok {
				rows.Close()
				return errors.New("tariff without point")
			}
			if err = json.Unmarshal(body, &p.Tariffs); err != nil {
				rows.Close()
				return err
			}
			p.Version = version
			points[id] = p
		case "MonthlyStatementsImported":
			p, ok := points[id]
			if !ok {
				rows.Close()
				return errors.New("monthly statements without point")
			}
			var statements []domain.MonthlyStatement
			if err = json.Unmarshal(body, &statements); err != nil {
				rows.Close()
				return err
			}
			p.MonthlyStatements = append(p.MonthlyStatements, statements...)
			p.Version = version
			points[id] = p
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var photoCount int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM photos WHERE point_id IS NOT NULL").Scan(&photoCount); err != nil {
		return err
	}
	if photoCount != len(photoDocuments) {
		return domain.Fail("PHOTO_MISSING")
	}
	for id, ws := range workspaces {
		if _, err = tx.Exec(ctx, "UPDATE workspaces SET name=$1,version=$2 WHERE id=$3", ws.Name, ws.Version, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM readings"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM members"); err != nil {
		return err
	}
	for _, m := range members {
		if _, err = tx.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role,active) VALUES($1,$2,$3,$4,$5)", m.Workspace, m.Subject, m.Email, m.Role, m.Active); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM annual_summaries"); err != nil {
		return err
	}
	pointIDs := make([]string, 0, len(points))
	for id := range points {
		pointIDs = append(pointIDs, id)
	}
	if _, err = tx.Exec(ctx, "DELETE FROM points WHERE NOT(id=ANY($1::uuid[]))", pointIDs); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM virtual_points"); err != nil {
		return err
	}
	for _, v := range virtuals {
		body, _ := json.Marshal(v)
		if _, err = tx.Exec(ctx, "INSERT INTO virtual_points(id,workspace_id,version,state,archived,deleted) VALUES($1,$2,$3,$4,$5,$6)", v.ID, v.WorkspaceID, v.Version, body, v.Archived, v.Deleted); err != nil {
			return err
		}
	}
	for _, p := range points {
		body, _ := json.Marshal(p)
		if _, err = tx.Exec(ctx, "INSERT INTO points(id,workspace_id,serial,version,state,archived) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET workspace_id=excluded.workspace_id,serial=excluded.serial,version=excluded.version,state=excluded.state,archived=excluded.archived", p.ID, p.WorkspaceID, p.Meter.Serial, p.Version, body, p.Archived); err != nil {
			return err
		}
		for _, r := range p.Readings {
			if err = insertReading(ctx, tx, p.ID, r); err != nil {
				return err
			}
		}
		if err = replaceAnnualProjections(ctx, tx, p); err != nil {
			return err
		}
	}
	for _, photo := range photoDocuments {
		p, ok := points[photo.PointID]
		if !ok {
			continue
		}
		body, _ := json.Marshal(photo)
		tag, e := tx.Exec(ctx, "UPDATE photos SET workspace_id=$1,point_id=$2,reading_id=$3,document=$4,expires_at=NULL WHERE id=$5 AND sha256=$6", p.WorkspaceID, photo.PointID, photo.ReadingID, body, photo.ID, photo.SHA256)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return domain.Fail("PHOTO_MISSING")
		}
	}
	return tx.Commit(ctx)
}

func insertReading(ctx context.Context, tx pgx.Tx, point string, r domain.Reading) error {
	_, err := tx.Exec(ctx, "INSERT INTO readings(id,point_id,register_id,reading_date,value,actor,recorded_at) VALUES($1,$2,$3,$4::text::date,$5::text::numeric(20,6),$6,$7)", r.ID, point, r.RegisterID, r.Date, r.Value, r.Actor, r.RecordedAt)
	return err
}

type Session struct {
	Subject, Email, ACR, CSRF string
	Expires                   time.Time
}

func (s *Store) Session(ctx context.Context, token string) (Session, error) {
	var v Session
	err := s.Pool.QueryRow(ctx, "UPDATE sessions SET last_seen=now() WHERE token_hash=$1 AND expires_at>now() AND last_seen>now()-interval '30 minutes' RETURNING subject,email,acr,csrf,expires_at", Hash(token)).Scan(&v.Subject, &v.Email, &v.ACR, &v.CSRF, &v.Expires)
	return v, err
}
func (s *Store) SaveSession(ctx context.Context, token string, v Session) error {
	_, err := s.Pool.Exec(ctx, "INSERT INTO sessions(token_hash,subject,email,acr,csrf,expires_at) VALUES($1,$2,$3,$4,$5,$6)", Hash(token), v.Subject, v.Email, v.ACR, v.CSRF, v.Expires)
	return err
}
