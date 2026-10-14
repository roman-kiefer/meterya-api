package downloads

import (
	"meterya.local/api/internal/domain"
	"testing"
	"time"
)

func TestDownloadsArePrivateExpireAndReleaseCapacity(t *testing.T) {
	c := New()
	t.Cleanup(c.Close)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.maxBytes = 4
	scope := Scope{"ws", "p", "2026", "owner"}
	file := File{Name: "file.html", MediaType: "text/html", Content: "1234"}
	id, err := c.Put(scope, file)
	if err != nil {
		t.Fatal(err)
	}
	for _, foreign := range []Scope{{"ws", "p", "2026", "other"}, {"other", "p", "2026", "owner"}, {"ws", "other", "2026", "owner"}, {"ws", "p", "2025", "owner"}} {
		if _, err = c.Get(foreign, id); domain.Code(err) != "RESOURCE_NOT_FOUND" {
			t.Fatal("scope leak", err)
		}
	}
	if got, err := c.Get(scope, id); err != nil || got != file {
		t.Fatal(got, err)
	}
	if _, err = c.Put(scope, file); domain.Code(err) != "EXPORT_LIMIT_REACHED" {
		t.Fatal(err)
	}
	now = now.Add(c.ttl)
	if _, err = c.Get(scope, id); domain.Code(err) != "RESOURCE_NOT_FOUND" || c.bytes != 0 {
		t.Fatal(err, c.bytes)
	}
	if _, err = c.Put(scope, file); err != nil {
		t.Fatal(err)
	}
}
