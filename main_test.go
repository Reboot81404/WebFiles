package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
		bad   bool
	}{
		{"", ".", false},
		{"/", ".", false},
		{"/Documents/notes.txt", "Documents/notes.txt", false},
		{"folder/./notes.txt", "folder/notes.txt", false},
		{"../etc/passwd", "", true},
		{"/../../etc/passwd", "", true},
		{"folder\\notes.txt", "", true},
		{"//server/share", "", true},
	}
	for _, tt := range tests {
		got, err := cleanPath(tt.input)
		if tt.bad && err == nil {
			t.Errorf("cleanPath(%q) expected error", tt.input)
		}
		if !tt.bad && (err != nil || got != tt.want) {
			t.Errorf("cleanPath(%q) = %q, %v; want %q", tt.input, got, err, tt.want)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a/b", `a\\b`, "bad\x00name"} {
		if validName(name) {
			t.Errorf("validName(%q) = true, want false", name)
		}
	}
	for _, name := range []string{"notes.txt", "a file.txt", ".config"} {
		if !validName(name) {
			t.Errorf("validName(%q) = false, want true", name)
		}
	}
}

func TestCreateWriteReadAndDelete(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := &app{root: root}

	createBody := `{"path":"/","name":"notes.txt"}`
	createReq := httptest.NewRequest("POST", "/api/create-file", strings.NewReader(createBody))
	createReq.Header.Set("X-WebFiles-Request", "1")
	createRes := httptest.NewRecorder()
	a.handleCreateFile(createRes, createReq)
	if createRes.Code != 201 {
		t.Fatalf("create status = %d, body = %s", createRes.Code, createRes.Body.String())
	}

	writeBody := `{"path":"/notes.txt","content":"hello from test"}`
	writeReq := httptest.NewRequest("PUT", "/api/write", strings.NewReader(writeBody))
	writeReq.Header.Set("X-WebFiles-Request", "1")
	writeRes := httptest.NewRecorder()
	a.handleWrite(writeRes, writeReq)
	if writeRes.Code != 200 {
		t.Fatalf("write status = %d, body = %s", writeRes.Code, writeRes.Body.String())
	}

	readReq := httptest.NewRequest("GET", "/api/read?path=%2Fnotes.txt", nil)
	readRes := httptest.NewRecorder()
	a.handleRead(readRes, readReq)
	if readRes.Code != 200 {
		t.Fatalf("read status = %d, body = %s", readRes.Code, readRes.Body.String())
	}
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(readRes.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Content != "hello from test" {
		t.Fatalf("read content = %q", payload.Content)
	}

	deleteReq := httptest.NewRequest("POST", "/api/delete", strings.NewReader(`{"path":"/notes.txt"}`))
	deleteReq.Header.Set("X-WebFiles-Request", "1")
	deleteRes := httptest.NewRecorder()
	a.handleDelete(deleteRes, deleteReq)
	if deleteRes.Code != 204 {
		t.Fatalf("delete status = %d, body = %s", deleteRes.Code, deleteRes.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still exists or stat failed: %v", err)
	}
}

func TestCopyFileAndDirectory(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "source", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "backup"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "source", "hello.txt"), []byte("hello"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "source", "nested", "data.txt"), []byte("nested data"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := &app{root: root}

	req := httptest.NewRequest("POST", "/api/copy", strings.NewReader(`{"source":"/source","destination":"/backup/source"}`))
	req.Header.Set("X-WebFiles-Request", "1")
	res := httptest.NewRecorder()
	a.handleCopy(res, req)
	if res.Code != 201 {
		t.Fatalf("copy status = %d, body = %s", res.Code, res.Body.String())
	}
	for rel, want := range map[string]string{
		"backup/source/hello.txt":       "hello",
		"backup/source/nested/data.txt": "nested data",
	} {
		got, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("copied %s = %q, want %q", rel, got, want)
		}
	}
}

func TestCopyRejectsSymlinksAndExistingDestinations(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "source.txt"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "existing.txt"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(base, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Mkdir(filepath.Join(base, "real-dir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "real-dir"), filepath.Join(base, "linked-dir")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := &app{root: root}

	for _, body := range []string{
		`{"source":"/link.txt","destination":"/copy.txt"}`,
		`{"source":"/source.txt","destination":"/existing.txt"}`,
		`{"source":"/linked-dir/child.txt","destination":"/copy-child.txt"}`,
	} {
		req := httptest.NewRequest("POST", "/api/copy", strings.NewReader(body))
		req.Header.Set("X-WebFiles-Request", "1")
		res := httptest.NewRecorder()
		a.handleCopy(res, req)
		if res.Code == 201 {
			t.Fatalf("copy unexpectedly succeeded: %s", body)
		}
	}
	if _, err := os.Lstat(filepath.Join(base, "copy.txt")); !os.IsNotExist(err) {
		t.Fatalf("unexpected copy target or lstat error: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(base, "existing.txt")); err != nil || string(got) != "existing" {
		t.Fatalf("existing destination was modified: %q, %v", got, err)
	}
}

func TestCannotDeleteRoot(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := &app{root: root}
	req := httptest.NewRequest("POST", "/api/delete", strings.NewReader(`{"path":"/"}`))
	req.Header.Set("X-WebFiles-Request", "1")
	res := httptest.NewRecorder()
	a.handleDelete(res, req)
	if res.Code != 400 {
		t.Fatalf("delete root status = %d, want 400", res.Code)
	}
}

func TestAtomicWritePreservesMode(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(dir, "private.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &app{root: root}
	if _, err := a.atomicWrite("private.txt", strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "private.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("permissions after atomic save = %04o, want 0600", got)
	}
}

func TestRootRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := root.Open("escape/secret.txt"); err == nil {
		t.Fatal("os.Root unexpectedly allowed opening a file outside its root")
	}
}
