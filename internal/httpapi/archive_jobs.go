package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/google/uuid"
	"io"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/downloads"
	"meterya.local/api/internal/store"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type archiveJob struct {
	ID                           string            `json:"id"`
	Status                       string            `json:"status"`
	Completed                    int               `json:"completed"`
	Total                        int               `json:"total"`
	Error                        string            `json:"error,omitempty"`
	Info                         *store.BackupInfo `json:"info,omitempty"`
	DownloadURL                  string            `json:"download_url,omitempty"`
	owner, path, key, mode, hash string
	expires                      time.Time
}
type archiveJobs struct {
	sync.Mutex
	jobs map[string]*archiveJob
	dir  string
	work chan struct{}
}

func newArchiveJobs() *archiveJobs {
	return &archiveJobs{jobs: map[string]*archiveJob{}, work: make(chan struct{}, 1)}
}
func (j *archiveJobs) create(subject, key, mode string) (*archiveJob, bool, error) {
	j.Lock()
	defer j.Unlock()
	count := 0
	for _, job := range j.jobs {
		if job.owner == subject {
			count++
			if job.key == key {
				if job.mode != mode {
					return nil, false, domain.Fail("IDEMPOTENCY_CONFLICT")
				}
				return job, true, nil
			}
		}
	}
	if count >= 10 || len(j.jobs) >= 40 {
		return nil, false, domain.Fail("BACKUP_BUSY")
	}
	if j.dir == "" {
		dir, err := os.MkdirTemp("", "meterya-backup-")
		if err != nil {
			return nil, false, err
		}
		j.dir = dir
	}
	id := uuid.NewString()
	job := &archiveJob{ID: id, Status: "PREPARING", owner: subject, key: key, mode: mode, path: filepath.Join(j.dir, id), expires: time.Now().Add(24 * time.Hour)}
	j.jobs[id] = job
	// Only server-generated UUID files in our own temporary directory are removed.
	time.AfterFunc(24*time.Hour, func() {
		j.Lock()
		defer j.Unlock()
		os.Remove(job.path)
		delete(j.jobs, id)
		if len(j.jobs) == 0 {
			os.Remove(j.dir)
			j.dir = ""
		}
	})
	return job, false, nil
}
func (j *archiveJobs) finish(job *archiveJob, info *store.BackupInfo, err error) {
	j.Lock()
	defer j.Unlock()
	job.Info = info
	if err != nil {
		job.Status = "FAILED"
		job.Error = domain.Code(err)
		if job.Error == "" {
			job.Error = "INTERNAL_ERROR"
		}
		os.Remove(job.path)
		return
	}
	job.Status = "READY"
	if job.mode == "export" {
		job.DownloadURL = "/api/v1/operator/backup/jobs/" + job.ID + "/download"
	}
}
func (s *Server) archiveSnapshot(job *archiveJob) archiveJob {
	s.backupJobs.Lock()
	defer s.backupJobs.Unlock()
	return *job
}
func (s *Server) prepareArchive(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if err := s.operator(v); err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	job, repeated, err := s.backupJobs.create(v.Subject, k, "export")
	if err != nil {
		return err
	}
	if !repeated {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			select {
			case s.backupJobs.work <- struct{}{}:
				defer func() { <-s.backupJobs.work }()
			case <-ctx.Done():
				s.backupJobs.finish(job, nil, domain.Fail("BACKUP_BUSY"))
				return
			}
			err := s.Store.BackupFile(ctx, job.path, func(done, total int) {
				s.backupJobs.Lock()
				job.Completed, job.Total = done, total
				s.backupJobs.Unlock()
			})
			s.backupJobs.finish(job, nil, err)
		}()
	}
	write(w, 202, s.archiveSnapshot(job))
	return nil
}
func (s *Server) ownArchive(r *http.Request, v store.Session) (*archiveJob, error) {
	if err := s.operator(v); err != nil {
		return nil, err
	}
	s.backupJobs.Lock()
	defer s.backupJobs.Unlock()
	job := s.backupJobs.jobs[r.PathValue("job")]
	if job == nil || job.owner != v.Subject || time.Now().After(job.expires) {
		return nil, domain.Fail("RESOURCE_NOT_FOUND")
	}
	return job, nil
}
func (s *Server) archiveStatus(w http.ResponseWriter, r *http.Request, v store.Session) error {
	job, err := s.ownArchive(r, v)
	if err != nil {
		return err
	}
	write(w, 200, s.archiveSnapshot(job))
	return nil
}
func (s *Server) archiveDownload(w http.ResponseWriter, r *http.Request, v store.Session) error {
	job, err := s.ownArchive(r, v)
	if err != nil {
		return err
	}
	copy := s.archiveSnapshot(job)
	if copy.Status != "READY" || copy.mode != "export" {
		return domain.Fail("BACKUP_NOT_READY")
	}
	f, err := os.Open(copy.path)
	if err != nil {
		return domain.Fail("RESOURCE_NOT_FOUND")
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(20 * time.Minute))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\"meterya-backup.zip\"")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "meterya-backup.zip", stat.ModTime(), f)
	return nil
}
func (s *Server) uploadArchive(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if err := s.operator(v); err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	job, repeated, err := s.backupJobs.create(v.Subject, k, "import")
	if err != nil {
		return err
	}
	if repeated {
		copy := s.archiveSnapshot(job)
		if copy.hash == "" {
			return domain.Fail("BACKUP_BUSY")
		}
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(20 * time.Minute))
		hash := sha256.New()
		size, e := io.Copy(hash, io.LimitReader(r.Body, store.MaxArchiveBytes+1))
		if e != nil || size > store.MaxArchiveBytes {
			return domain.Fail("BACKUP_INVALID")
		}
		if hex.EncodeToString(hash.Sum(nil)) != copy.hash {
			return domain.Fail("IDEMPOTENCY_CONFLICT")
		}
		write(w, 202, s.archiveSnapshot(job))
		return nil
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(20 * time.Minute))
	f, err := os.OpenFile(job.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		s.backupJobs.finish(job, nil, err)
		return err
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(r.Body, store.MaxArchiveBytes+1))
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if size > store.MaxArchiveBytes {
		err = domain.Fail("BACKUP_TOO_LARGE")
	}
	if err != nil {
		s.backupJobs.finish(job, nil, err)
		return err
	}
	s.backupJobs.Lock()
	job.hash = hex.EncodeToString(hash.Sum(nil))
	s.backupJobs.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		select {
		case s.backupJobs.work <- struct{}{}:
			defer func() { <-s.backupJobs.work }()
		case <-ctx.Done():
			s.backupJobs.finish(job, nil, domain.Fail("BACKUP_BUSY"))
			return
		}
		info, err := s.Store.RestoreBackupFile(ctx, job.path, true)
		s.backupJobs.finish(job, &info, err)
	}()
	write(w, 202, s.archiveSnapshot(job))
	return nil
}
func (s *Server) restoreArchive(files *downloads.Cache) handler {
	return func(w http.ResponseWriter, r *http.Request, v store.Session) error {
		job, err := s.ownArchive(r, v)
		if err != nil {
			return err
		}
		copy := s.archiveSnapshot(job)
		if copy.Status != "READY" || copy.mode != "import" {
			return domain.Fail("BACKUP_NOT_READY")
		}
		if r.Header.Get("X-Backup-Confirm") != "RESTORE" || r.Header.Get("X-Backup-SHA256") != copy.hash {
			return domain.Fail("BACKUP_CONFIRMATION_REQUIRED")
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(20 * time.Minute))
		info, err := s.Store.RestoreBackupFile(r.Context(), copy.path, false)
		if err != nil {
			return err
		}
		files.Close()
		http.SetCookie(w, &http.Cookie{Name: "meterya_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.Auth.Secure, SameSite: http.SameSiteLaxMode})
		write(w, 200, info)
		return nil
	}
}
