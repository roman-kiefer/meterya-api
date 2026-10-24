package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"meterya.local/api/internal/auth"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/downloads"
	"meterya.local/api/internal/media"
	"meterya.local/api/internal/store"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type Server struct {
	Version         string
	Store           *store.Store
	Auth            *auth.Auth
	Identity        auth.Keycloak
	OperatorSubject string
	PhotoRecognizer *media.Recognizer
	backupJobs      *archiveJobs
	photoProcessing chan struct{}
}

type pointResponse struct {
	Photos          []domain.Photo         `json:"photos"`
	Virtual         *domain.VirtualInfo    `json:"virtual,omitempty"`
	OverviewVersion int                    `json:"overview_version"`
	Readings        []domain.ReadingView   `json:"readings"`
	History         []domain.HistoryWindow `json:"history"`
	domain.Point
	Consumption    []domain.ConsumptionInterval `json:"consumption"`
	Annual         []domain.AnnualSummary       `json:"annual"`
	MonthlyBilling []domain.MonthlyBill         `json:"monthly_billing"`
}

func pointResponses(points []domain.Point) ([]pointResponse, error) {
	result := make([]pointResponse, 0, len(points))
	for _, point := range points {
		intervals, err := point.Consumption()
		if err != nil {
			return nil, err
		}
		bills, err := point.MonthlyBills()
		if err != nil {
			return nil, err
		}
		readings, err := point.ReadingViews(intervals)
		if err != nil {
			return nil, err
		}
		result = append(result, pointResponse{Point: point, Readings: readings, Consumption: intervals, MonthlyBilling: bills})
	}
	return result, nil
}

// Enrich only the response snapshot; these derived values do not modify events
// or the annual accounting cache.
func (r *pointResponse) withOverview() {
	r.OverviewVersion = domain.OverviewVersion
	r.History = r.Point.Histories(r.Annual)
	for i := range r.Annual {
		annual := &r.Annual[i]
		annual.Overview = r.Point.Overview(annual, 0)
		for j := range annual.Months {
			m := &annual.Months[j]
			m.Overview = r.Point.Overview(&m.AnnualSummary, m.Month)
		}
	}
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, err error) {
	code := domain.Code(err)
	status := 400
	switch code {
	case "AUTHENTICATION_REQUIRED":
		status = 401
	case "PERMISSION_DENIED", "MFA_REQUIRED":
		status = 403
	case "RESOURCE_NOT_FOUND":
		status = 404
	case "EXPORT_LIMIT_REACHED", "PHOTO_DRAFT_LIMIT", "PHOTO_OCR_BUSY", "BACKUP_BUSY":
		status = 429
	case "BACKUP_TOO_LARGE", "PHOTO_TOO_LARGE":
		status = 413
	case "CONFLICT", "READING_ALREADY_EXISTS", "AGGREGATE_VERSION_CONFLICT", "IDEMPOTENCY_CONFLICT", "TARIFF_OVERLAP", "POINT_ARCHIVED", "POINT_LINKED", "VIRTUAL_SOURCE_IN_USE", "VIRTUAL_ALREADY_EXISTS", "VIRTUAL_AMBIGUOUS", "VIRTUAL_INCOMPATIBLE", "VIRTUAL_CYCLE", "VIRTUAL_CONTINUATION_REQUIRED", "VIRTUAL_TRANSITION_REQUIRED", "SOURCE_MISSING", "SERIAL_ALREADY_ACTIVE", "LAST_WORKSPACE_ADMIN":
		status = 409
	case "INTERNAL_ERROR":
		status = 500
		slog.Error("request failed", "error", err)
	}
	write(w, status, map[string]string{"code": code})
}

type handler func(http.ResponseWriter, *http.Request, store.Session) error

func (s *Server) guard(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, err := s.Auth.Session(r)
		if err != nil {
			failure(w, domain.Fail("AUTHENTICATION_REQUIRED"))
			return
		}
		admin, err := s.Store.IsAdmin(r.Context(), session.Subject)
		if err != nil {
			failure(w, err)
			return
		}
		if admin && session.ACR != "2" {
			failure(w, domain.Fail("MFA_REQUIRED"))
			return
		}
		if r.Method != "GET" {
			if r.Header.Get("Origin") != s.Auth.Origin || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRF)) != 1 {
				failure(w, domain.Fail("PERMISSION_DENIED"))
				return
			}
		}
		if ws := r.PathValue("ws"); ws != "" {
			if _, err = uuid.Parse(ws); err != nil {
				failure(w, domain.Fail("RESOURCE_NOT_FOUND"))
				return
			}
			role, roleErr := s.Store.Role(r.Context(), ws, session.Subject)
			if err = roleErr; err != nil {
				failure(w, err)
				return
			}
			if role == "GUEST" && r.Method != "GET" && !strings.Contains(r.URL.Path, "/reports/") {
				failure(w, domain.Fail("PERMISSION_DENIED"))
				return
			}
		}
		for _, name := range []string{"point", "invite", "export", "reading", "photo"} {
			if id := r.PathValue(name); id != "" {
				if _, err = uuid.Parse(id); err != nil {
					failure(w, domain.Fail("RESOURCE_NOT_FOUND"))
					return
				}
			}
		}
		if err = h(w, r, session); err != nil {
			failure(w, err)
		}
	}
}
func decode(r *http.Request, v any) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return nil, domain.Fail("VALIDATION_FAILED")
	}
	d := json.NewDecoder(strings.NewReader(string(body)))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return nil, domain.Fail("VALIDATION_FAILED")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, domain.Fail("VALIDATION_FAILED")
	}
	return body, nil
}
func key(r *http.Request) (string, error) {
	v := r.Header.Get("Idempotency-Key")
	if _, err := uuid.Parse(v); err != nil {
		return "", domain.Fail("IDEMPOTENCY_KEY_REQUIRED")
	}
	return v, nil
}
func (s *Server) Handler() http.Handler {
	if s.photoProcessing == nil {
		s.photoProcessing = make(chan struct{}, 1)
	}
	if s.backupJobs == nil {
		s.backupJobs = newArchiveJobs()
	}
	if s.PhotoRecognizer == nil {
		s.PhotoRecognizer = media.NewRecognizer()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, r *http.Request) {
		version := s.Version
		if version == "" {
			version = "dev"
		}
		write(w, http.StatusOK, map[string]string{"version": version})
	})
	reportFiles := downloads.New()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.Pool.Ping(r.Context()); err != nil {
			write(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		write(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /auth/login", s.Auth.Login)
	mux.HandleFunc("GET /auth/callback", s.Auth.Callback)
	mux.HandleFunc("GET /auth/profile", s.guard(func(w http.ResponseWriter, r *http.Request, _ store.Session) error {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return nil
	}))
	mux.HandleFunc("GET /auth/profile/security", s.guard(func(w http.ResponseWriter, r *http.Request, _ store.Session) error { return s.Auth.Profile(w, r) }))
	mux.HandleFunc("GET /auth/account/{action}", s.guard(func(w http.ResponseWriter, r *http.Request, session store.Session) error {
		return s.Auth.AccountAction(w, r, r.PathValue("action"), session.ACR == "2")
	}))
	mux.HandleFunc("GET /api/v1/profile", s.guard(s.profile))
	mux.HandleFunc("GET /api/v1/audit", s.guard(s.audit))
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/audit", s.guard(s.audit))
	mux.HandleFunc("POST /api/v1/profile", s.guard(s.saveProfile))
	mux.HandleFunc("POST /auth/logout", s.guard(func(w http.ResponseWriter, r *http.Request, _ store.Session) error { return s.Auth.Logout(w, r) }))
	mux.HandleFunc("GET /api/v1/me", s.guard(func(w http.ResponseWriter, r *http.Request, v store.Session) error {
		workspaces, err := s.Store.Workspaces(r.Context(), v.Subject)
		if err != nil {
			return err
		}
		write(w, 200, map[string]any{"email": v.Email, "subject": v.Subject, "csrf_token": v.CSRF, "workspaces": workspaces, "operator": s.operator(v) == nil})
		return nil
	}))
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/points", s.guard(func(w http.ResponseWriter, r *http.Request, session store.Session) error {
		snap, err := s.Store.Snapshot(r.Context(), r.PathValue("ws"))
		if err != nil {
			return err
		}
		response, err := pointResponses(snap.Points)
		if err != nil {
			return err
		}
		for i := range response {
			response[i].Photos = photoViews(snap.Photos[response[i].ID], response[i].Point, r.PathValue("ws"))
			response[i].Annual = snap.Annual[response[i].ID]
			response[i].withOverview()
			response[i].History = response[i].Point.HistoriesThrough(response[i].Annual, snap.CurrentYear)
		}
		virtuals, err := snap.VirtualReports()
		if err != nil {
			return err
		}
		role, e := s.Store.Role(r.Context(), r.PathValue("ws"), session.Subject)
		if e != nil {
			return e
		}
		for _, v := range virtuals {
			info := v.Info
			for _, action := range []string{"edit", "archive", "delete"} {
				info.Actions[action] = store.CanManage(role) && session.ACR == "2"
			}
			response = append(response, pointResponse{Point: v.Point, Virtual: &info, Readings: []domain.ReadingView{}, Consumption: v.Intervals, Annual: v.Annual, MonthlyBilling: []domain.MonthlyBill{}, History: v.Point.HistoriesThrough(v.Annual, snap.CurrentYear), OverviewVersion: domain.OverviewVersion})
		}
		if r.URL.Query().Get("include_archived") != "true" {
			active := []pointResponse{}
			for _, p := range response {
				if !p.Archived {
					active = append(active, p)
				}
			}
			response = active
		}
		sort.SliceStable(response, func(i, j int) bool { return response[i].Kind == "VIRTUAL" && response[j].Kind != "VIRTUAL" })
		write(w, 200, response)
		return nil
	}))
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/virtual-points", s.guard(s.virtualCandidates))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/virtual-points", s.guard(s.saveVirtual))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/virtual-points/{point}/save", s.guard(s.saveVirtual))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/virtual-points/{point}/remove", s.guard(s.removeVirtual))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/points", s.guard(s.createPoint))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/rename", s.guard(s.renameWorkspace))
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/transitions", s.guard(func(w http.ResponseWriter, r *http.Request, _ store.Session) error {
		v, err := s.Store.MeterTransitions(r.Context(), r.PathValue("ws"))
		if err == nil {
			write(w, 200, v)
		}
		return err
	}))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/transitions", s.guard(s.recordTransition))
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/power-to-heat", s.guard(func(w http.ResponseWriter, r *http.Request, _ store.Session) error {
		v, e := s.Store.HeatReports(r.Context(), r.PathValue("ws"))
		if e == nil {
			write(w, 200, v)
		}
		return e
	}))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/power-to-heat", s.guard(s.configureHeat))
	mux.HandleFunc("POST /api/v1/workspaces", s.guard(s.createWorkspace))
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/members", s.guard(s.listMembers))
	for _, action := range []string{"role", "remove", "activity"} {
		mux.HandleFunc("POST /api/v1/workspaces/{ws}/members/"+action, s.guard(func(w http.ResponseWriter, r *http.Request, v store.Session) error {
			r.SetPathValue("action", action)
			return s.manageMember(w, r, v)
		}))
	}
	for _, action := range []string{"rename", "details", "archive", "restore"} {
		mux.HandleFunc("POST /api/v1/workspaces/{ws}/points/{point}/"+action, s.guard(func(w http.ResponseWriter, r *http.Request, v store.Session) error {
			r.SetPathValue("action", action)
			return s.managePoint(w, r, v)
		}))
	}
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/points/{point}/move", s.guard(s.movePoint))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/points/{point}/purge", s.guard(s.purgePoint(reportFiles)))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/points/{point}/readings", s.guard(s.recordReading))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/photos", s.guard(s.photoWork(s.uploadPhoto)))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/photos/{photo}/analyze", s.guard(s.photoWork(s.analyzePhoto)))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/photos/{photo}/working", s.guard(s.photoWork(s.workingPhoto)))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/photos/{photo}/confirm", s.guard(s.photoWork(s.confirmPhoto)))
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/photos/{photo}/edited", s.guard(s.photoFile))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/photos/{photo}/discard", s.guard(s.discardPhoto))
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/photos/{photo}/original", s.guard(s.photoFile))
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/photos/{photo}/preview", s.guard(s.photoFile))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/points/{point}/readings/{reading}/correct", s.guard(s.changeReading))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/points/{point}/readings/{reading}/retract", s.guard(s.changeReading))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/points/{point}/tariffs", s.guard(s.saveTariffs))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/points/{point}/reports/{year}/download", s.guard(s.prepareReport(reportFiles)))
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/points/{point}/reports/{year}/download/{export}", s.guard(func(w http.ResponseWriter, r *http.Request, v store.Session) error {
		if err := s.reportPointExists(r); err != nil {
			return err
		}
		return s.downloadReport(reportFiles)(w, r, v)
	}))
	mux.HandleFunc("GET /api/v1/workspaces/{ws}/invitations", s.guard(s.listInvites))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/invitations", s.guard(s.invite))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/invitations/{invite}/retry", s.guard(s.retryInvite))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/invitations/{invite}/revoke", s.guard(s.revokeInvite))
	mux.HandleFunc("POST /api/v1/workspaces/{ws}/invitations/{invite}/delete", s.guard(s.deleteInvite))
	mux.HandleFunc("POST /api/v1/invitations/accept", s.guard(s.acceptInvite))
	mux.HandleFunc("GET /api/v1/operator/backup", s.guard(s.backupDownload))
	mux.HandleFunc("POST /api/v1/operator/backup/jobs", s.guard(s.prepareArchive))
	mux.HandleFunc("GET /api/v1/operator/backup/jobs/{job}", s.guard(s.archiveStatus))
	mux.HandleFunc("GET /api/v1/operator/backup/jobs/{job}/download", s.guard(s.archiveDownload))
	mux.HandleFunc("POST /api/v1/operator/backup/uploads", s.guard(s.uploadArchive))
	mux.HandleFunc("POST /api/v1/operator/backup/jobs/{job}/restore", s.guard(s.restoreArchive(reportFiles)))
	mux.HandleFunc("POST /api/v1/operator/backup/validate", s.guard(s.restoreBackup(reportFiles, true)))
	mux.HandleFunc("POST /api/v1/operator/backup/restore", s.guard(s.restoreBackup(reportFiles, false)))
	var maintenance sync.RWMutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && (r.URL.Path == "/api/v1/operator/backup/restore" || r.URL.Path == "/api/v1/operator/backup/validate") {
			maintenance.Lock()
			defer maintenance.Unlock()
		} else {
			maintenance.RLock()
			defer maintenance.RUnlock()
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) createPoint(w http.ResponseWriter, r *http.Request, v store.Session) error {
	var input struct {
		Name   string `json:"name"`
		Serial string `json:"serial"`
		Type   string `json:"type"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Serial = strings.TrimSpace(input.Serial)
	if input.Name == "" || input.Serial == "" || len(input.Name) > 200 || len(input.Serial) > 100 {
		return domain.Fail("VALIDATION_FAILED")
	}
	unit := "kWh"
	dirs := []string{"IMPORT"}
	switch input.Type {
	case "WATER":
		unit = "mÂ³"
		dirs = []string{"CONSUMPTION"}
	case "ELECTRICITY":
	case "BIDIRECTIONAL":
		dirs = append(dirs, "EXPORT")
	default:
		return domain.Fail("VALIDATION_FAILED")
	}
	p := domain.Point{ID: uuid.NewString(), WorkspaceID: r.PathValue("ws"), Name: input.Name, Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: input.Serial, Type: input.Type, Registers: []domain.Register{}}, Readings: []domain.Reading{}}
	for _, d := range dirs {
		p.Meter.Registers = append(p.Meter.Registers, domain.Register{ID: uuid.NewString(), Direction: d, Unit: unit})
	}
	result, err := s.Store.Execute(r.Context(), store.Command{Workspace: p.WorkspaceID, Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), NewPoint: &p})
	if err == nil {
		write(w, 201, result)
	}
	return err
}
func (s *Server) recordReading(w http.ResponseWriter, r *http.Request, v store.Session) error {
	var input struct {
		RegisterID string `json:"register_id"`
		Date       string `json:"date"`
		Value      string `json:"value"`
		Expected   int64  `json:"expected_version"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	value, err := domain.Decimal(input.Value)
	if err != nil {
		return err
	}
	if _, err = uuid.Parse(r.PathValue("point")); err != nil {
		return domain.Fail("RESOURCE_NOT_FOUND")
	}
	reading := domain.Reading{ID: uuid.NewString(), RegisterID: input.RegisterID, Date: input.Date, Value: value, Actor: v.Subject, RecordedAt: time.Now().UTC()}
	var zone string
	if err = s.Store.Pool.QueryRow(r.Context(), "SELECT timezone FROM workspaces WHERE id=$1", r.PathValue("ws")).Scan(&zone); err != nil {
		return err
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return err
	}
	p, err := s.Store.Execute(r.Context(), store.Command{Workspace: r.PathValue("ws"), Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), Expected: input.Expected, PointID: r.PathValue("point"), Reading: &reading, Today: time.Now().In(location).Format("2006-01-02")})
	if err == nil {
		write(w, 201, p)
	}
	return err
}
