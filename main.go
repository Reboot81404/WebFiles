package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed web/*
var webFiles embed.FS

const (
	maxTextBytes   = 5 << 20
	maxListEntries = 2000
	maxCopyEntries = 10000
)

type app struct {
	root     *os.Root
	user     string
	password string
}

type apiError struct {
	Error string `json:"error"`
}

type listEntry struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	ModTime string `json:"modified"`
}

type listResponse struct {
	Path      string      `json:"path"`
	Entries   []listEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

type pathRequest struct {
	Path string `json:"path"`
}

type writeRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type renameRequest struct {
	OldPath string `json:"oldPath"`
	NewPath string `json:"newPath"`
}

type copyRequest struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8090", "HTTP listen address")
	rootPath := flag.String("root", "./data", "Directory exposed by WebFiles")
	user := flag.String("user", "", "HTTP Basic Auth username (required for non-loopback listen addresses)")
	passwordEnv := flag.String("password-env", "WEBFILES_PASSWORD", "Environment variable containing the Basic Auth password")
	//tlsCert := flag.String("tls-cert", "", "TLS certificate PEM file (required for non-loopback listeners)")
	//tlsKey := flag.String("tls-key", "", "TLS private key PEM file (required for non-loopback listeners)")
	flag.Parse()

	password := os.Getenv(*passwordEnv)
	if *user != "" && password == "" {
		log.Fatalf("-user was set but environment variable %s is empty", *passwordEnv)
	}
	/*if (*tlsCert == "") != (*tlsKey == "") {
		log.Fatal("-tls-cert and -tls-key must be supplied together")
	}*/
	if !isLoopbackAddress(*listen) {
		if *user == "" || len(password) < 4 {
			log.Fatal("non-loopback listeners require -user, a password of at least 4 characters ")
		}
	}
	
	if err := os.MkdirAll(*rootPath, 0750); err != nil {
		log.Fatalf("create root directory: %v", err)
	}
	root, err := os.OpenRoot(*rootPath)
	if err != nil {
		log.Fatalf("open root directory: %v", err)
	}
	defer root.Close()

	a := &app{root: root, user: *user, password: password}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", a.handleIndex)
	mux.HandleFunc("GET /app.js", a.handleAsset)
	mux.HandleFunc("GET /style.css", a.handleAsset)
	mux.HandleFunc("GET /api/list", a.handleList)
	mux.HandleFunc("GET /api/read", a.handleRead)
	mux.HandleFunc("GET /api/preview", a.handlePreview)
	mux.HandleFunc("GET /api/download", a.handleDownload)
	mux.HandleFunc("POST /api/create-file", a.handleCreateFile)
	mux.HandleFunc("POST /api/create-dir", a.handleCreateDir)
	mux.HandleFunc("PUT /api/write", a.handleWrite)
	mux.HandleFunc("POST /api/delete", a.handleDelete)
	mux.HandleFunc("POST /api/rename", a.handleRename)
	mux.HandleFunc("POST /api/copy", a.handleCopy)
	mux.HandleFunc("POST /api/upload", a.handleUpload)

	server := &http.Server{
		Addr:              *listen,
		Handler:           a.securityHeaders(a.auth(mux)),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	/*
	scheme := "http"
	if *tlsCert != "" {
		scheme = "https"
	}
	log.Printf("WebEdit listening on %s://%s (root: %s)", scheme, *listen, *rootPath)
	if *tlsCert != "" {
		log.Fatal(server.ListenAndServeTLS(*tlsCert, *tlsKey))
	}
	log.Fatal(server.ListenAndServe())
	*/
	log.Printf("WebFiles listening on http://%s (root: %s)", *listen, *rootPath)
	log.Fatal(server.ListenAndServe())


}

func isLoopbackAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (a *app) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.user == "" {
			next.ServeHTTP(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(a.user))
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(a.password))
		if !ok || userOK != 1 || passOK != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="WebFiles", charset="UTF-8"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		if r.URL.Path == "/" || r.URL.Path == "/app.js" || r.URL.Path == "/style.css" {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; media-src 'self'; frame-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "frontend unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (a *app) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name != "app.js" && name != "style.css" {
		http.NotFound(w, r)
		return
	}
	data, err := webFiles.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if name == "app.js" {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	w.Write(data)
}

func (a *app) handleList(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}
	dir, err := a.root.Open(rel)
	if err != nil {
		writeFSError(w, err)
		return
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is not a directory")
		return
	}
	entries, err := dir.ReadDir(maxListEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		writeFSError(w, err)
		return
	}
	truncated := len(entries) > maxListEntries
	if truncated {
		entries = entries[:maxListEntries]
	}
	result := make([]listEntry, 0, len(entries))
	for _, entry := range entries {
		fi, statErr := entry.Info()
		if statErr != nil {
			continue
		}
		typ := "file"
		mode := fi.Mode()
		switch {
		case mode.IsDir():
			typ = "directory"
		case mode&fs.ModeSymlink != 0:
			typ = "symlink"
		case !mode.IsRegular():
			typ = "other"
		}
		result = append(result, listEntry{
			Name: entry.Name(), Type: typ, Size: fi.Size(), ModTime: fi.ModTime().Format(time.RFC3339),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Type == "directory" && result[j].Type != "directory" {
			return true
		}
		if result[i].Type != "directory" && result[j].Type == "directory" {
			return false
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	writeJSON(w, http.StatusOK, listResponse{Path: virtualPath(rel), Entries: result, Truncated: truncated})
}

func (a *app) handleRead(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil || rel == "." {
		writeError(w, http.StatusBadRequest, "invalid file path")
		return
	}
	f, err := a.root.Open(rel)
	if err != nil {
		writeFSError(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeFSError(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, "only regular files can be edited")
		return
	}
	if info.Size() > maxTextBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file is larger than the 5 MiB editor limit")
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, maxTextBytes+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read file")
		return
	}
	if int64(len(data)) > maxTextBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file is larger than the 5 MiB editor limit")
		return
	}
	if !utf8.Valid(data) {
		writeError(w, http.StatusUnsupportedMediaType, "file is not valid UTF-8 text; use download instead")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": virtualPath(rel), "content": string(data)})
}

func (a *app) handlePreview(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil || rel == "." {
		writeError(w, http.StatusBadRequest, "invalid file path")
		return
	}
	ext := strings.ToLower(path.Ext(rel))
	allowed := map[string]string{
		".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif",
		".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp",
		".mp4": "video/mp4", ".webm": "video/webm", ".ogv": "video/ogg",
		".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".m4a": "audio/mp4",
		".pdf": "application/pdf",
	}
	contentType, ok := allowed[ext]
	if !ok {
		writeError(w, http.StatusUnsupportedMediaType, "preview is not supported for this file type")
		return
	}
	a.serveFile(w, r, rel, contentType, false)
}

func (a *app) handleDownload(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil || rel == "." {
		writeError(w, http.StatusBadRequest, "invalid file path")
		return
	}
	a.serveFile(w, r, rel, "", true)
}

func (a *app) serveFile(w http.ResponseWriter, r *http.Request, rel, forcedType string, attachment bool) {
	f, err := a.root.Open(rel)
	if err != nil {
		writeFSError(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeFSError(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, "path is not a regular file")
		return
	}
	contentType := forcedType
	if contentType == "" {
		contentType = mime.TypeByExtension(strings.ToLower(path.Ext(rel)))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if attachment {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(rel)}))
	} else {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": path.Base(rel)}))
		if contentType == "application/pdf" {
			w.Header().Set("Content-Security-Policy", "sandbox")
		}
	}
	http.ServeContent(w, r, path.Base(rel), info.ModTime(), f)
}

func (a *app) handleCreateFile(w http.ResponseWriter, r *http.Request) {
	if !a.validMutation(w, r) {
		return
	}
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req, 1<<20) {
		return
	}
	parent, err := cleanPath(req.Path)
	if err != nil || !validName(req.Name) {
		writeError(w, http.StatusBadRequest, "invalid path or filename")
		return
	}
	rel := joinPath(parent, req.Name)
	f, err := a.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		writeFSError(w, err)
		return
	}
	if err := f.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not close new file")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"path": virtualPath(rel)})
}

func (a *app) handleCreateDir(w http.ResponseWriter, r *http.Request) {
	if !a.validMutation(w, r) {
		return
	}
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req, 1<<20) {
		return
	}
	parent, err := cleanPath(req.Path)
	if err != nil || !validName(req.Name) {
		writeError(w, http.StatusBadRequest, "invalid path or directory name")
		return
	}
	rel := joinPath(parent, req.Name)
	if err := a.root.Mkdir(rel, 0755); err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"path": virtualPath(rel)})
}

func (a *app) handleWrite(w http.ResponseWriter, r *http.Request) {
	if !a.validMutation(w, r) {
		return
	}
	var req writeRequest
	if !decodeJSON(w, r, &req, 6*maxTextBytes+(1<<20)) {
		return
	}
	rel, err := cleanPath(req.Path)
	if err != nil || rel == "." {
		writeError(w, http.StatusBadRequest, "invalid file path")
		return
	}
	if int64(len(req.Content)) > maxTextBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "text files are limited to 5 MiB")
		return
	}
	if !utf8.ValidString(req.Content) {
		writeError(w, http.StatusBadRequest, "content must be valid UTF-8")
		return
	}
	if _, err := a.atomicWrite(rel, strings.NewReader(req.Content)); err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": virtualPath(rel), "bytes": len(req.Content)})
}

func (a *app) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !a.validMutation(w, r) {
		return
	}
	var req pathRequest
	if !decodeJSON(w, r, &req, 1<<20) {
		return
	}
	rel, err := cleanPath(req.Path)
	if err != nil || rel == "." {
		writeError(w, http.StatusBadRequest, "refusing to delete the root directory")
		return
	}
	if err := a.root.RemoveAll(rel); err != nil {
		writeFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) handleRename(w http.ResponseWriter, r *http.Request) {
	if !a.validMutation(w, r) {
		return
	}
	var req renameRequest
	if !decodeJSON(w, r, &req, 1<<20) {
		return
	}
	oldPath, oldErr := cleanPath(req.OldPath)
	newPath, newErr := cleanPath(req.NewPath)
	if oldErr != nil || newErr != nil || oldPath == "." || newPath == "." || oldPath == newPath {
		writeError(w, http.StatusBadRequest, "invalid source or destination path")
		return
	}
	if _, err := a.root.Lstat(oldPath); err != nil {
		writeFSError(w, err)
		return
	}
	if _, err := a.root.Lstat(newPath); err == nil {
		writeError(w, http.StatusConflict, "destination already exists")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		writeFSError(w, err)
		return
	}
	if err := a.root.Rename(oldPath, newPath); err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": virtualPath(newPath)})
}

func (a *app) handleCopy(w http.ResponseWriter, r *http.Request) {
	if !a.validMutation(w, r) {
		return
	}
	var req copyRequest
	if !decodeJSON(w, r, &req, 1<<20) {
		return
	}
	source, sourceErr := cleanPath(req.Source)
	destination, destinationErr := cleanPath(req.Destination)
	if sourceErr != nil || destinationErr != nil || source == "." || destination == "." || source == destination {
		writeError(w, http.StatusBadRequest, "invalid source or destination path")
		return
	}
	if strings.HasPrefix(destination, source+"/") {
		writeError(w, http.StatusBadRequest, "a directory cannot be copied into itself")
		return
	}
	if err := a.rejectSymlinkComponents(source); err != nil {
		writeFSError(w, err)
		return
	}
	if err := a.rejectSymlinkComponents(path.Dir(destination)); err != nil {
		writeFSError(w, err)
		return
	}
	parent, err := a.root.Open(path.Dir(destination))
	if err != nil {
		writeFSError(w, err)
		return
	}
	parentInfo, statErr := parent.Stat()
	parent.Close()
	if statErr != nil || !parentInfo.IsDir() {
		writeError(w, http.StatusBadRequest, "destination parent is not a directory")
		return
	}
	if _, err := a.root.Lstat(destination); err == nil {
		writeError(w, http.StatusConflict, "destination already exists")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		writeFSError(w, err)
		return
	}

	count := 0
	if err := a.copyEntry(source, destination, &count); err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"path": virtualPath(destination)})
}

// rejectSymlinkComponents ensures that no existing path component is a
// symbolic link. Checking each component prevents an intermediate link from
// redirecting a copy operation to another location inside the root.
func (a *app) rejectSymlinkComponents(rel string) error {
	if rel == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(rel, "/") {
		current = joinPath(currentOrDot(current), part)
		info, err := a.root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return errors.New("symbolic link path components cannot be copied through")
		}
	}
	return nil
}

func currentOrDot(value string) string {
	if value == "" {
		return "."
	}
	return value
}

// copyEntry copies only regular files and directories. Symbolic links and
// special files are rejected rather than followed or recreated.
func (a *app) copyEntry(source, destination string, count *int) error {
	if err := a.rejectSymlinkComponents(source); err != nil {
		return err
	}
	(*count)++
	if *count > maxCopyEntries {
		return errors.New("copy contains too many filesystem entries")
	}
	info, err := a.root.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return errors.New("symbolic links and special files cannot be copied")
	}
	if info.IsDir() {
		if err := a.root.Mkdir(destination, 0755); err != nil {
			return err
		}
		cleanup := func() { _ = a.root.RemoveAll(destination) }
		dir, err := a.root.Open(source)
		if err != nil {
			cleanup()
			return err
		}
		entries, readErr := dir.ReadDir(-1)
		closeErr := dir.Close()
		if readErr != nil {
			cleanup()
			return readErr
		}
		if closeErr != nil {
			cleanup()
			return closeErr
		}
		for _, entry := range entries {
			if err := a.copyEntry(path.Join(source, entry.Name()), path.Join(destination, entry.Name()), count); err != nil {
				cleanup()
				return err
			}
		}
		return nil
	}

	input, err := a.root.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	openedInfo, err := input.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() {
		return errors.New("source is not a regular file")
	}
	output, err := a.root.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()&0777)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil {
		_ = a.root.Remove(destination)
		return copyErr
	}
	if closeErr != nil {
		_ = a.root.Remove(destination)
		return closeErr
	}
	return nil
}
/*
func (a *app) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !a.validMutation(w, r) {
		return
	}
	dir, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid destination directory")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload or upload exceeds 100 MiB")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	files := r.MultipartForm.File["file"]
	if len(files) != 1 {
		writeError(w, http.StatusBadRequest, "upload exactly one file per request")
		return
	}
	filename := path.Base(strings.ReplaceAll(files[0].Filename, "\\", "/"))
	if !validName(filename) {
		writeError(w, http.StatusBadRequest, "invalid uploaded filename")
		return
	}
	if files[0].Size > maxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "uploads are limited to 100 MiB")
		return
	}
	rel := joinPath(dir, filename)
	if _, err := a.root.Lstat(rel); err == nil {
		writeError(w, http.StatusConflict, "destination already exists; rename or delete it before uploading")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		writeFSError(w, err)
		return
	}
	file, err := files[0].Open()
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not open uploaded file")
		return
	}
	defer file.Close()
	n, err := a.atomicWrite(rel, io.LimitReader(file, maxUploadBytes))
	if err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"path": virtualPath(rel), "bytes": n})
}
*/
func (a *app) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !a.validMutation(w, r) {
		return
	}
	dir, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid destination directory")
		return
	}

	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart request")
		return
	}

	part, err := mr.NextPart()
	if err != nil || part.FormName() != "file" {
		writeError(w, http.StatusBadRequest, "upload exactly one file per request")
		return
	}
	defer part.Close()

	filename := path.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
	if !validName(filename) {
		writeError(w, http.StatusBadRequest, "invalid uploaded filename")
		return
	}

	rel := joinPath(dir, filename)
	if _, err := a.root.Lstat(rel); err == nil {
		writeError(w, http.StatusConflict, "destination already exists; rename or delete it before uploading")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		writeFSError(w, err)
		return
	}

	n, err := a.atomicWrite(rel, part)
	if err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"path": virtualPath(rel), "bytes": n})
}




func (a *app) atomicWrite(rel string, src io.Reader) (int64, error) {
	parent := path.Dir(rel)
	base := path.Base(rel)
	mode := os.FileMode(0644)
	if info, err := a.root.Lstat(rel); err == nil {
		switch {
		case info.Mode().IsRegular():
			// Preserve existing permissions when replacing a regular file.
			mode = info.Mode().Perm()
		case info.Mode()&fs.ModeSymlink != 0:
			// Replace the link itself, never follow it; use private permissions.
			mode = 0600
		default:
			return 0, errors.New("destination is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return 0, err
	}
	tmp := path.Join(parent, ".webfiles-tmp-"+hex.EncodeToString(random[:]))
	f, err := a.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return 0, err
	}
	cleanup := func() {
		f.Close()
		a.root.Remove(tmp)
	}
	n, copyErr := io.Copy(f, src)
	if copyErr != nil {
		cleanup()
		return n, copyErr
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return n, err
	}
	if err := f.Close(); err != nil {
		a.root.Remove(tmp)
		return n, err
	}
	if err := a.root.Rename(tmp, path.Join(parent, base)); err != nil {
		a.root.Remove(tmp)
		return n, err
	}
	return n, nil
}

func (a *app) validMutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-WebFiles-Request") != "1" {
		writeError(w, http.StatusForbidden, "missing request header")
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			writeError(w, http.StatusForbidden, "origin rejected")
			return false
		}
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid JSON request")
		}
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request must contain one JSON object")
		return false
	}
	return true
}

func cleanPath(input string) (string, error) {
	if strings.ContainsRune(input, '\x00') || strings.Contains(input, "\\") {
		return "", errors.New("invalid path")
	}
	if input == "" || input == "/" {
		return ".", nil
	}
	if strings.HasPrefix(input, "//") {
		return "", errors.New("invalid absolute path")
	}
	input = strings.TrimPrefix(input, "/")
	for _, part := range strings.Split(input, "/") {
		if part == ".." {
			return "", errors.New("parent traversal rejected")
		}
	}
	clean := path.Clean(input)
	if clean == "." || clean == "" {
		return ".", nil
	}
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || !fs.ValidPath(clean) {
		return "", errors.New("invalid relative path")
	}
	return clean, nil
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00") && utf8.ValidString(name)
}

func joinPath(parent, name string) string {
	if parent == "." {
		return name
	}
	return path.Join(parent, name)
}

func virtualPath(rel string) string {
	if rel == "." {
		return "/"
	}
	return "/" + rel
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, apiError{Error: message})
}

func writeFSError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "file or directory not found")
	case errors.Is(err, os.ErrExist):
		writeError(w, http.StatusConflict, "file or directory already exists")
	case errors.Is(err, os.ErrPermission):
		writeError(w, http.StatusForbidden, "permission denied")
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("filesystem operation failed: %v", err))
	}
}
