package prusalink_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/fault"

	prusalink "github.com/lkvapil/prusa-driver-prusalink"
	"github.com/lkvapil/prusa-driver-prusalink/prusalinksim"
)

// memory is a file the host offers, held in memory, which is enough for a test.
type memory []byte

func (m memory) Size() int64 { return int64(len(m)) }

func (m memory) Open(_ context.Context, offset int64) (io.ReadCloser, error) {
	if offset > int64(len(m)) {
		offset = int64(len(m))
	}

	return io.NopCloser(bytes.NewReader(m[offset:])), nil
}

// TestStoragesAndFiles lists the storages and a folder, and reads what the
// slicer wrote about one file. A storage the printer says is not available,
// here the SD card, is left out.
func TestStoragesAndFiles(t *testing.T) {
	printer := startPrinter(t, prusalinksim.Options{})
	files := filesOf(t, connect(t, printer, nil))

	storages, err := files.Storages(t.Context())
	if err != nil || len(storages) != 1 || storages[0].Name != "local" || storages[0].Mountpoint != "/local" ||
		!storages[0].FreeSpaceKnown {
		t.Fatalf("the storages are %+v, %v", storages, err)
	}

	root, err := files.List(t.Context(), "")
	if err != nil || len(root.Entries) != 1 || root.Entries[0].Path != "local" || !root.Entries[0].Folder {
		t.Fatalf("the root is %+v, %v", root, err)
	}

	local, err := files.List(t.Context(), "local")
	if err != nil || len(local.Entries) != 1 || local.Entries[0].Path != "local/examples" || !local.Entries[0].Folder {
		t.Fatalf("local is %+v, %v", local, err)
	}

	one, err := files.List(t.Context(), "local/examples/benchy.gcode")
	if err != nil || one.Directory.Folder || one.Directory.EstimatedSeconds != 4320 ||
		one.Directory.Material != "PETG" || one.Directory.LayerHeightMM != 0.2 || one.Directory.PrinterModel != "MK3S" {
		t.Fatalf("the file is %+v, %v", one.Directory, err)
	}

	if _, err = files.List(t.Context(), "local/../etc"); !fault.HasCode(err, connectorapi.PathNotAllowed.Code()) {
		t.Fatalf("a path that leaves its storage gave %v", err)
	}
}

// TestSendPrintAndDelete puts a file on the printer, prints it, and deletes it.
func TestSendPrintAndDelete(t *testing.T) {
	printer := startPrinter(t, prusalinksim.Options{User: "maker", Password: "s3cret"})
	session := connect(t, printer, map[string]string{"username": "maker", "password": "s3cret"})

	transfers, ok := connectorapi.Uploads(session)
	if !ok {
		t.Fatal("the session takes no file")
	}

	content := memory(bytes.Repeat([]byte("G1 X10 Y10\n"), 100_000))

	var last connectorapi.Progress

	reports := 0

	err := transfers.Send(t.Context(), connectorapi.Upload{
		Path: "local/T500 Adapter.gcode", Content: content,
		Progress: func(p connectorapi.Progress) { last = p; reports++ },
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	if stored, _ := printer.File("T500 Adapter.gcode"); !bytes.Equal(stored, content) {
		t.Fatalf("the printer holds %d bytes, want %d", len(stored), len(content))
	}

	if last.Sent != content.Size() || last.Total != content.Size() || reports < 3 {
		t.Fatalf("the last of %d reports is %+v", reports, last)
	}

	// A name that is taken is refused, as PrusaLink does without Overwrite.
	err = transfers.Send(t.Context(), connectorapi.Upload{Path: "local/T500 Adapter.gcode", Content: memory("G28\n")})
	if !fault.HasCode(err, connectorapi.TransferRefused.Code()) {
		t.Fatalf("a taken name gave %v", err)
	}

	tree := collect(session)

	result, err := session.Do(t.Context(), connectorapi.Command{
		Verb: prusalink.VerbStartPrint, Args: map[string]string{"path": "local/T500 Adapter.gcode"},
	})
	if err != nil || !result.OK {
		t.Fatalf("start_print gave %+v, %v", result, err)
	}

	tree.waitFor(t, "/state", `"PRINTING"`)
	tree.waitFor(t, "/job/name", `"T500 Adapter.gcode"`)

	result, err = session.Do(t.Context(), connectorapi.Command{
		Verb: prusalink.VerbStartPrint, Args: map[string]string{"path": "local/examples/benchy.gcode"},
	})
	if err != nil || result.OK || result.FaultCode != prusalink.NotNow.Code().String() {
		t.Fatalf("a second print while one runs gave %+v, %v", result, err)
	}

	manager, _ := connectorapi.Manages(session)
	if err = manager.DeleteFolder(t.Context(), "local/examples"); err != nil {
		t.Fatalf("delete a folder: %v", err)
	}

	if _, held := printer.File("examples/benchy.gcode"); held {
		t.Fatal("the folder was not deleted")
	}

	if err = manager.DeleteFile(t.Context(), "local/missing.gcode"); !fault.HasCode(err, connectorapi.FileChangeRefused.Code()) {
		t.Fatalf("deleting a file that is not there gave %v", err)
	}
}

func filesOf(t *testing.T, session connectorapi.Session) connectorapi.Filesystem {
	t.Helper()

	files, ok := connectorapi.Files(session)
	if !ok {
		t.Fatal("the session lists no files")
	}

	return files
}
