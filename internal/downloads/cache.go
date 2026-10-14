package downloads

import (
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"sync"
	"time"
)

type Scope struct{ Workspace, Point, Year, Subject string }
type File struct {
	Name, MediaType, Content string
	DataVersion              string
}
type entry struct {
	scope   Scope
	file    File
	expires time.Time
	timer   *time.Timer
}
type Cache struct {
	mu       sync.Mutex
	files    map[string]entry
	bytes    int
	maxBytes int
	ttl      time.Duration
	now      func() time.Time
}

// Files are operational, private downloads, not new ledger events. They never
// reach the database or disk and are removed automatically after five minutes.
func New() *Cache {
	return &Cache{files: map[string]entry{}, maxBytes: 8 << 20, ttl: 5 * time.Minute, now: time.Now}
}
func (c *Cache) remove(id string) {
	if e, ok := c.files[id]; ok {
		c.bytes -= len(e.file.Content)
		e.timer.Stop()
		delete(c.files, id)
	}
}
func (c *Cache) purge() {
	for id, e := range c.files {
		if !e.expires.After(c.now()) {
			c.remove(id)
		}
	}
}
func (c *Cache) Put(scope Scope, file File) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purge()
	if len(c.files) >= 128 || len(file.Content) > c.maxBytes-c.bytes {
		return "", domain.Fail("EXPORT_LIMIT_REACHED")
	}
	id := uuid.NewString()
	timer := time.AfterFunc(c.ttl, func() { c.mu.Lock(); defer c.mu.Unlock(); c.remove(id) })
	c.files[id] = entry{scope: scope, file: file, expires: c.now().Add(c.ttl), timer: timer}
	c.bytes += len(file.Content)
	return id, nil
}
func (c *Cache) Get(scope Scope, id string) (File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purge()
	e, ok := c.files[id]
	if !ok || e.scope != scope {
		return File{}, domain.Fail("RESOURCE_NOT_FOUND")
	}
	return e.file, nil
}
func (c *Cache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.files {
		c.remove(id)
	}
}

func (c *Cache) RemovePoint(workspace, point string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, e := range c.files {
		if e.scope.Workspace == workspace && e.scope.Point == point {
			c.remove(id)
		}
	}
}
