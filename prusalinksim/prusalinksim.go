// Package prusalinksim is a fake PrusaLink for tests. It serves the requests
// the prusalink driver sends, with the fields of the published OpenAPI file,
// and it keeps one print that a test starts, pauses and ends.
//
// It signs nothing in on its own: a test chooses an API key, a Digest user, or
// neither.
package prusalinksim

import (
	"crypto/md5" //nolint:gosec // HTTP Digest in RFC 7616 with MD5, as PrusaLink asks for it.
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

// Options says how the fake printer signs in and what it reports.
type Options struct {
	// APIKey, when set, is the only X-Api-Key the printer accepts.
	APIKey string
	// User and Password, when set, make the printer ask for HTTP Digest.
	User     string
	Password string
	// Type is the printer type /api/version reports in original, as PrusaLink
	// 0.8.1 names it. Empty is I3MK3S, an MK3S.
	Type string
	// Serial is the serial number /api/v1/info reports.
	Serial string
}

// Printer is one fake PrusaLink.
type Printer struct {
	opts   Options
	server *httptest.Server

	mu       sync.Mutex
	state    string
	job      int64
	file     string
	progress float64
	// Requests counts every request that reached a handler, signed or not.
	requests int
	// files is the local storage of the Raspberry Pi: path inside it to bytes.
	files map[string][]byte
}

// Start serves a fake printer until Close.
func Start(opts Options) *Printer {
	if opts.Type == "" {
		opts.Type = "I3MK3S"
	}

	p := &Printer{opts: opts, state: "IDLE", files: map[string][]byte{"examples/benchy.gcode": []byte(benchy)}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", p.signed(p.version))
	mux.HandleFunc("GET /api/v1/info", p.signed(p.info))
	mux.HandleFunc("GET /api/v1/status", p.signed(p.status))
	mux.HandleFunc("GET /api/v1/job", p.signed(p.currentJob))
	mux.HandleFunc("PUT /api/v1/job/{id}/pause", p.signed(p.command("PRINTING", "PAUSED")))
	mux.HandleFunc("PUT /api/v1/job/{id}/resume", p.signed(p.command("PAUSED", "PRINTING")))
	mux.HandleFunc("DELETE /api/v1/job/{id}", p.signed(p.stop))
	mux.HandleFunc("GET /api/v1/storage", p.signed(p.storage))
	mux.HandleFunc("GET /api/v1/files/local/{path...}", p.signed(p.listFiles))
	mux.HandleFunc("PUT /api/v1/files/local/{path...}", p.signed(p.upload))
	mux.HandleFunc("POST /api/v1/files/local/{path...}", p.signed(p.printFile))
	mux.HandleFunc("DELETE /api/v1/files/local/{path...}", p.signed(p.deleteFile))

	p.server = httptest.NewServer(mux)

	return p
}

// Address is what an operator writes as the endpoint address.
func (p *Printer) Address() string { return p.server.URL }

// Close stops serving.
func (p *Printer) Close() { p.server.Close() }

// Requests is how many requests reached the printer.
func (p *Printer) Requests() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.requests
}

// StartPrint puts a print of file on the bed, with the given job id.
func (p *Printer) StartPrint(job int64, file string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.state, p.job, p.file, p.progress = "PRINTING", job, file, 0
}

// Finish ends the print in the given state: FINISHED, STOPPED or ERROR. The job
// stays in the status, as PrusaLink keeps it there for a while.
func (p *Printer) Finish(state string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.state, p.progress = state, 100
}

// Clear takes the ended job off the status, as PrusaLink does once the printer
// is ready again.
func (p *Printer) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.state, p.job, p.file = "IDLE", 0, ""
}

// State is the state the printer reports now.
func (p *Printer) State() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.state
}

func (p *Printer) version(w http.ResponseWriter, _ *http.Request) {
	// The fields PrusaLink 0.8.1 sends. It has no printer field, though the
	// published API file names one.
	writeJSON(w, map[string]any{
		"api": "2.0.0", "server": "0.8.1", "original": "PrusaLink " + p.opts.Type, "text": "PrusaLink 0.8.1",
		"firmware": "3.14.1-8237", "sdk": "0.8.1", "capabilities": map[string]any{"upload-by-put": true},
		"hostname": "prusa-mk3s",
	})
}

func (p *Printer) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"name": "Fake MK3S", "hostname": "prusa-mk3s", "serial": p.opts.Serial, "nozzle_diameter": 0.4,
	})
}

func (p *Printer) status(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	body := map[string]any{
		"printer": map[string]any{
			"state": p.state, "temp_nozzle": 214.8, "target_nozzle": 215.0, "temp_bed": 59.9, "target_bed": 60.0,
			"axis_z": 1.2, "flow": 100, "speed": 100, "fan_hotend": 4800, "fan_print": 3100,
		},
	}

	if p.job != 0 {
		body["job"] = map[string]any{"id": p.job, "progress": p.progress, "time_printing": 125, "time_remaining": 3600}
	}

	writeJSON(w, body)
}

func (p *Printer) currentJob(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.job == 0 {
		w.WriteHeader(http.StatusNoContent)

		return
	}

	writeJSON(w, map[string]any{
		"id": p.job, "state": p.state,
		"file": map[string]any{"name": strings.ToUpper(p.file), "display_name": p.file, "path": "/usb"},
	})
}

// command moves a print from one state to another, and answers 409 when the
// print is not in the first one, as PrusaLink does.
func (p *Printer) command(from, to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()

		if strconv.FormatInt(p.job, 10) != r.PathValue("id") {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		if p.state != from {
			w.WriteHeader(http.StatusConflict)

			return
		}

		p.state = to
		w.WriteHeader(http.StatusNoContent)
	}
}

func (p *Printer) stop(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if strconv.FormatInt(p.job, 10) != r.PathValue("id") {
		w.WriteHeader(http.StatusNotFound)

		return
	}

	p.state = "STOPPED"
	w.WriteHeader(http.StatusNoContent)
}

// benchy is the one print file the fake printer holds from the start. Its
// header carries what a slicer writes, so a listing of it reads metadata.
const benchy = "; estimated printing time (normal mode) = 1h 12m\nG28\n"

// File returns the bytes stored at a path of the local storage, and false when
// there is none.
func (p *Printer) File(path string) ([]byte, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	data, ok := p.files[path]

	return data, ok
}

func (p *Printer) storage(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"storage_list": []map[string]any{
		{"name": "PrusaLink gcodes", "type": "LOCAL", "path": "/local", "free_space": 7_000_000_000, "available": true, "read_only": false},
		{"name": "SD Card", "type": "SDCARD", "path": "/sdcard", "available": false, "read_only": true},
	}})
}

// listFiles answers one file with its metadata, or one folder with its
// children, the way PrusaLink does.
func (p *Printer) listFiles(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	path := strings.Trim(r.PathValue("path"), "/")

	if data, ok := p.files[path]; ok {
		writeJSON(w, map[string]any{
			"name": baseName(path), "display_name": baseName(path), "type": "PRINT_FILE", "size": len(data),
			"m_timestamp": 1764000000, "read_only": false,
			"meta": map[string]any{"estimated_print_time": 4320, "layer_height": 0.2, "filament_type": "PETG", "printer_model": "MK3S"},
		})

		return
	}

	children := []map[string]any{}
	folders := map[string]bool{}

	for name, data := range p.files {
		rest, inside := strings.CutPrefix(name, path+"/")
		if path == "" {
			rest, inside = name, true
		}

		if !inside {
			continue
		}

		if head, _, nested := strings.Cut(rest, "/"); nested {
			if !folders[head] {
				folders[head] = true
				children = append(children, map[string]any{"name": head, "display_name": head, "type": "FOLDER"})
			}

			continue
		}

		children = append(children, map[string]any{
			"name": rest, "display_name": rest, "type": "PRINT_FILE", "size": len(data), "m_timestamp": 1764000000,
		})
	}

	if path != "" && len(children) == 0 {
		w.WriteHeader(http.StatusNotFound)

		return
	}

	writeJSON(w, map[string]any{"name": baseName(path), "type": "FOLDER", "children": children})
}

// upload stores the body, and refuses a name that is taken, as PrusaLink does
// without the Overwrite header.
func (p *Printer) upload(w http.ResponseWriter, r *http.Request) {
	// PrusaLink 0.8.1 takes text/x.gcode without a check. An octet-stream
	// upload goes through a libmagic check that refuses a G-code file, so the
	// fake refuses every other type, and a driver that sends one finds out here.
	if r.Header.Get("Content-Type") != "text/x.gcode" {
		w.WriteHeader(http.StatusUnsupportedMediaType)

		return
	}

	if r.ContentLength <= 0 {
		w.WriteHeader(http.StatusLengthRequired)

		return
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	path := strings.Trim(r.PathValue("path"), "/")
	if _, taken := p.files[path]; taken && r.Header.Get("Overwrite") != "?1" {
		w.WriteHeader(http.StatusConflict)

		return
	}

	p.files[path] = data
	w.WriteHeader(http.StatusCreated)
}

// printFile starts a print of a stored file, and refuses while one runs.
func (p *Printer) printFile(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	path := strings.Trim(r.PathValue("path"), "/")
	if _, ok := p.files[path]; !ok {
		w.WriteHeader(http.StatusNotFound)

		return
	}

	if p.state == "PRINTING" || p.state == "PAUSED" {
		w.WriteHeader(http.StatusConflict)

		return
	}

	p.job++
	p.state, p.file, p.progress = "PRINTING", baseName(path), 0
	w.WriteHeader(http.StatusNoContent)
}

// deleteFile removes a file, or a folder and what is in it.
func (p *Printer) deleteFile(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	path := strings.Trim(r.PathValue("path"), "/")
	removed := false

	for name := range p.files {
		if name == path || strings.HasPrefix(name, path+"/") {
			delete(p.files, name)
			removed = true
		}
	}

	if !removed {
		w.WriteHeader(http.StatusNotFound)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func baseName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}

	return path
}

// realm and nonce are the fixed Digest challenge of the fake printer.
const (
	realm = "Printer API"
	nonce = "1f3e5d7c9b"
)

// signed refuses a request that is not signed the way the options ask for.
func (p *Printer) signed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.requests++
		p.mu.Unlock()

		if p.opts.APIKey != "" && r.Header.Get("X-Api-Key") == p.opts.APIKey {
			next(w, r)

			return
		}

		if p.opts.User != "" && p.digestValid(r) {
			next(w, r)

			return
		}

		if p.opts.APIKey == "" && p.opts.User == "" {
			next(w, r)

			return
		}

		if p.opts.User != "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="`+realm+`", nonce="`+nonce+`", qop="auth"`)
		}

		w.WriteHeader(http.StatusUnauthorized)
	}
}

// digestValid checks the response of an Authorization header against the
// password, as a PrusaLink server does.
func (p *Printer) digestValid(r *http.Request) bool {
	header, found := strings.CutPrefix(r.Header.Get("Authorization"), "Digest ")
	if !found {
		return false
	}

	fields := map[string]string{}

	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			fields[key] = strings.Trim(value, `"`)
		}
	}

	ha1 := md5Hex(p.opts.User + ":" + realm + ":" + p.opts.Password)
	ha2 := md5Hex(r.Method + ":" + fields["uri"])
	want := md5Hex(ha1 + ":" + nonce + ":" + fields["nc"] + ":" + fields["cnonce"] + ":auth:" + ha2)

	return fields["username"] == p.opts.User && fields["response"] == want && fields["uri"] == r.URL.RequestURI()
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // see the import.

	return hex.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
