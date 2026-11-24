package prusalink

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// The verbs that act on the printer's files. They are core keys, so the host's
// own file pages drive an MK3S the way they drive an MK4S.
const (
	// VerbStartPrint prints a file the printer holds.
	VerbStartPrint = connectorapi.Capability("core.command.start_print")
	// ArgStartPrintPath is that file, storage-prefixed and relative, such as
	// local/part.gcode.
	ArgStartPrintPath = connectorapi.Capability("core.command.start_print#path")
	// argPathLimit bounds a path argument, in bytes.
	argPathLimit = 1024
)

const (
	pathStorage = "/api/v1/storage"
	pathFiles   = "/api/v1/files"

	// progressStep is how many bytes go between two progress reports.
	progressStep = 256 << 10

	// gcodeType is the media type a print file is sent with.
	gcodeType = "text/x.gcode"

	// sfTrue is a true boolean in the form RFC 8941 gives a header, which is
	// how PrusaLink reads its Force and Overwrite headers.
	sfTrue = "?1"
)

// storageReply is GET /api/v1/storage.
type storageReply struct {
	StorageList []struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		Path      string `json:"path"`
		FreeSpace *int64 `json:"free_space"`
		Available *bool  `json:"available"`
		ReadOnly  bool   `json:"read_only"`
	} `json:"storage_list"`
}

// fileReply is GET /api/v1/files/{storage}/{path}: a file, or a folder with its
// children.
type fileReply struct {
	Name        string         `json:"name"`
	DisplayName string         `json:"display_name"`
	Type        string         `json:"type"`
	Size        int64          `json:"size"`
	Modified    int64          `json:"m_timestamp"`
	ReadOnly    bool           `json:"read_only"`
	Children    []fileReply    `json:"children"`
	Meta        map[string]any `json:"meta"`
}

const folderType = "FOLDER"

// storages is what the printer last said about its storages. The host reads it
// without asking the printer, so it is filled at Connect and after a transfer.
type storages struct {
	mu   sync.Mutex
	list []connectorapi.Storage
}

func (s *storages) set(list []connectorapi.Storage) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.list = list
}

func (s *storages) get() []connectorapi.Storage {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]connectorapi.Storage{}, s.list...)
}

var (
	_ connectorapi.Filesystem  = (*Session)(nil)
	_ connectorapi.FileManager = (*Session)(nil)
	_ connectorapi.Transfers   = (*Session)(nil)
)

// readStorages asks the printer for its storages. A storage the printer says is
// not available is left out, such as a printer with no SD card in it.
func readStorages(ctx context.Context, c *client) ([]connectorapi.Storage, error) {
	var reply storageReply
	if _, err := c.get(ctx, pathStorage, &reply); err != nil {
		return nil, err
	}

	out := make([]connectorapi.Storage, 0, len(reply.StorageList))

	for _, s := range reply.StorageList {
		if s.Available != nil && !*s.Available {
			continue
		}

		mount := "/" + strings.Trim(s.Path, "/")
		storage := connectorapi.Storage{
			Mountpoint: mount, Name: strings.TrimPrefix(mount, "/"), Type: s.Type, ReadOnly: s.ReadOnly,
		}

		if s.FreeSpace != nil {
			storage.FreeSpace, storage.FreeSpaceKnown = *s.FreeSpace, true
		}

		out = append(out, storage)
	}

	return out, nil
}

// Storages returns the storages the printer last reported.
func (s *Session) Storages(_ context.Context) ([]connectorapi.Storage, error) {
	return s.storages.get(), nil
}

// List returns one folder, or one file with what the slicer wrote about it.
// The empty path is the root, whose entries are the storages.
func (s *Session) List(ctx context.Context, path string) (connectorapi.Listing, error) {
	if err := connectorapi.RefusePathEscape(path); err != nil {
		return connectorapi.Listing{}, err
	}

	path = strings.Trim(path, "/")

	if path == "" {
		root := connectorapi.Listing{Directory: connectorapi.File{Folder: true}}

		for _, storage := range s.storages.get() {
			root.Entries = append(root.Entries, connectorapi.File{
				Path: storage.Name, Name: storage.Name, Folder: true, ReadOnly: storage.ReadOnly,
			})
		}

		return root, nil
	}

	var reply fileReply
	if _, err := s.client.get(ctx, filesPath(path), &reply); err != nil {
		return connectorapi.Listing{}, connectorapi.ListingRefused.New("the printer did not list the path").
			With(fault.StringParam("path", path), fault.StringParam("machine_reason", err.Error())).Wrap(err)
	}

	listing := connectorapi.Listing{Directory: fileOf(path, reply)}

	if reply.Type == folderType || reply.Children != nil {
		listing.Directory.Folder = true

		for _, child := range reply.Children {
			listing.Entries = append(listing.Entries, fileOf(path+"/"+child.Name, child))
		}

		return listing, nil
	}

	listing.Directory.EstimatedSeconds = metaInt(reply.Meta, "estimated_print_time")
	listing.Directory.LayerHeightMM = metaFloat(reply.Meta, "layer_height")
	listing.Directory.Material = metaText(reply.Meta, "filament_type")
	listing.Directory.PrinterModel = metaText(reply.Meta, "printer_model")

	return listing, nil
}

// fileOf is one entry of the printer's tree at path.
func fileOf(path string, f fileReply) connectorapi.File {
	file := connectorapi.File{
		Path: path, Name: f.Name, Folder: f.Type == folderType, Modified: f.Modified, ReadOnly: f.ReadOnly,
	}

	if f.DisplayName != f.Name {
		file.DisplayName = f.DisplayName
	}

	if !file.Folder {
		file.Size = f.Size
	}

	return file
}

// CreateFolder is not offered: PrusaLink's API has no request that only makes a
// folder. The host does not reach it, because the baseline does not carry the
// verb.
func (s *Session) CreateFolder(_ context.Context, path string) error {
	return connectorapi.FileChangeRefused.New("PrusaLink cannot make a folder through its API").
		With(fault.StringParam("path", path))
}

// DeleteFile removes one file. PrusaLink refuses with 409 the file it prints.
func (s *Session) DeleteFile(ctx context.Context, path string) error {
	return s.remove(ctx, path, false)
}

// DeleteFolder removes one folder, and what is inside it.
func (s *Session) DeleteFolder(ctx context.Context, path string) error {
	return s.remove(ctx, path, true)
}

func (s *Session) remove(ctx context.Context, path string, folder bool) error {
	if err := connectorapi.RefusePathEscape(path); err != nil {
		return err
	}

	headers := http.Header{}
	if folder {
		headers.Set("Force", sfTrue)
	}

	if _, err := s.client.request(ctx, http.MethodDelete, filesPath(path), headers, nil, -1, s.client.timeout); err != nil {
		return connectorapi.FileChangeRefused.New("the printer did not delete the path").
			With(fault.StringParam("path", path), fault.StringParam("machine_reason", err.Error())).Wrap(err)
	}

	return nil
}

// Send puts one file on the printer and confirms its length there.
func (s *Session) Send(ctx context.Context, up connectorapi.Upload) error {
	if err := connectorapi.RefusePathEscape(up.Path); err != nil {
		return err
	}

	path := strings.Trim(up.Path, "/")

	if !s.sending.CompareAndSwap(false, true) {
		return connectorapi.TransferBusy.New("the printer is already taking a file").
			With(fault.StringParam("path", path))
	}
	defer s.sending.Store(false)

	total := up.Content.Size()
	report := func(sent int64) {
		if up.Progress != nil {
			up.Progress(connectorapi.Progress{Total: total, Sent: sent})
		}
	}

	report(0)

	// A Digest server refuses the first request it sees, and a refused body is
	// gone. A status read first makes sure the challenge is known before the
	// upload starts.
	var status statusReply
	_, _ = s.client.get(ctx, pathStatus, &status)

	body, err := up.Content.Open(ctx, 0)
	if err != nil {
		return connectorapi.TransferFailed.New("the file could not be read").
			With(fault.StringParam("path", path), fault.StringParam("machine_reason", err.Error())).Wrap(err)
	}
	defer func() { _ = body.Close() }()

	// text/x.gcode and not application/octet-stream. PrusaLink 0.8.1 checks the
	// type of an octet-stream upload with libmagic, which reads a G-code file
	// as text/plain and refuses it, and it reads the file at its final name
	// before the file is there (prusa/link/web/files.py, file_upload). An upload
	// typed text/x.gcode is taken without that check. PrusaSlicer types it so.
	headers := http.Header{}
	headers.Set("Content-Type", gcodeType)

	counted := &countingReader{r: body, step: progressStep, report: report}

	// The whole transfer is bounded by ctx and not by the request timeout: a
	// Raspberry Pi Zero takes minutes for a large file.
	if _, err = s.client.request(ctx, http.MethodPut, filesPath(path), headers, counted, total, 0); err != nil {
		var answered errStatus
		if errors.As(err, &answered) {
			return connectorapi.TransferRefused.New("the printer refused the file").
				With(fault.StringParam("path", path), fault.StringParam("machine_reason", err.Error())).Wrap(err)
		}

		return connectorapi.TransferFailed.New("the transfer did not finish").
			With(fault.StringParam("path", path), fault.StringParam("machine_reason", err.Error())).Wrap(err)
	}

	report(total)

	if list, err := readStorages(ctx, s.client); err == nil {
		s.storages.set(list)
	}

	var placed fileReply
	if _, err = s.client.get(ctx, filesPath(path), &placed); err != nil {
		return connectorapi.TransferNotOnStorage.New("the file is not on the printer after the transfer").
			With(fault.StringParam("path", path), fault.StringParam("expected_size", strconv.FormatInt(total, 10)),
				fault.StringParam("found", "absent"))
	}

	if placed.Size != total {
		return connectorapi.TransferNotOnStorage.New("the file on the printer has another length").
			With(fault.StringParam("path", path), fault.StringParam("expected_size", strconv.FormatInt(total, 10)),
				fault.StringParam("found", strconv.FormatInt(placed.Size, 10)))
	}

	return nil
}

// startPrint prints a file the printer holds. 409 is a printer that is printing
// already, which is a refusal and not a failure.
func (s *Session) startPrint(ctx context.Context, path string) (connectorapi.Result, error) {
	if err := connectorapi.RefusePathEscape(path); err != nil {
		return connectorapi.Result{}, err
	}

	_, err := s.client.request(ctx, http.MethodPost, filesPath(strings.Trim(path, "/")), nil, nil, -1, s.client.timeout)
	if err == nil {
		return connectorapi.Result{OK: true}, nil
	}

	var answered errStatus
	if errors.As(err, &answered) && (answered.status == http.StatusConflict || answered.status == http.StatusNotFound) {
		return connectorapi.Result{OK: false, FaultCode: NotNow.Code().String(), MachineReason: err.Error()}, nil
	}

	return connectorapi.Result{}, unreachable(s.client.base, "the printer did not start the print").Wrap(err)
}

// filesPath is the URL path of one storage-prefixed path. Every segment is
// escaped, because a file name may hold a space or a hash.
func filesPath(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}

	joined := pathFiles + "/" + strings.Join(segments, "/")

	// The root of a storage is the storage and an empty path. The published API
	// names only /api/v1/files/{storage}/{path}, so the empty path keeps its
	// slash.
	if len(segments) == 1 {
		joined += "/"
	}

	return joined
}

// countingReader reports how far a body has been read, once per step.
type countingReader struct {
	r      io.Reader
	step   int64
	read   int64
	last   int64
	report func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)

	if c.read-c.last >= c.step {
		c.last = c.read
		c.report(c.read)
	}

	return n, err
}

// The metadata keys a slicer writes into a print file, as PrusaLink reports
// them under meta. A key that is missing reads as zero or empty.
func metaFloat(meta map[string]any, key string) float64 {
	if v, ok := meta[key].(float64); ok {
		return v
	}

	return 0
}

func metaInt(meta map[string]any, key string) int64 {
	return int64(metaFloat(meta, key))
}

func metaText(meta map[string]any, key string) string {
	if v, ok := meta[key].(string); ok {
		return v
	}

	return ""
}
