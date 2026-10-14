package downloads

import "testing"

func TestRemovePointInvalidatesAllItsReportsOnly(t *testing.T) {
	c := New()
	t.Cleanup(c.Close)
	first := Scope{Workspace: "ws", Point: "point", Year: "2026", Subject: "admin"}
	second := first
	second.Subject, second.Year = "user", "2025"
	unrelated := first
	unrelated.Point = "other"
	id1, _ := c.Put(first, File{Content: "first"})
	id2, _ := c.Put(second, File{Content: "second"})
	id3, _ := c.Put(unrelated, File{Content: "unrelated"})
	c.RemovePoint("ws", "point")
	if _, err := c.Get(first, id1); err == nil {
		t.Fatal("admin download survived")
	}
	if _, err := c.Get(second, id2); err == nil {
		t.Fatal("user download survived")
	}
	if f, err := c.Get(unrelated, id3); err != nil || f.Content != "unrelated" {
		t.Fatal(f, err)
	}
}
