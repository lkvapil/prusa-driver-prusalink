package prusalink_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/connectorapi/connectorapitest"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/fault"

	prusalink "github.com/lkvapil/prusa-driver-prusalink"
	"github.com/lkvapil/prusa-driver-prusalink/prusalinksim"
)

// Qualified is how an operator selects this driver when another module has
// registered the same bare name. It is derived from the module path.
const Qualified = "com.github.aux.lkvapil.prusa-driver-prusalink/prusalink"

// testInterval is how often a test session reads the status. A test cannot
// wait the shipping two seconds.
const testInterval = 10 * time.Millisecond

// TestContract runs the published conformance suite against this driver and a
// fake PrusaLink.
func TestContract(t *testing.T) {
	connectorapitest.RunDriverContract(t, func(t *testing.T) connectorapitest.Subject {
		printer := startPrinter(t, prusalinksim.Options{APIKey: "k3y", Serial: "CZPX1234X004XK12345"})

		return connectorapitest.Subject{
			Driver: prusalink.NewDriver(0).WithPollInterval(testInterval),
			Endpoint: connectorapi.Endpoint{
				Address: printer.Address(),
				Options: map[string]string{"api_key": "k3y"},
			},
			Connectable: true,
			SafeVerb:    prusalink.VerbSendStateInfo,
		}
	})
}

func TestTheDriverIsRegistered(t *testing.T) {
	for _, descriptor := range connectorapi.Descriptors() {
		if descriptor.Name() == prusalink.Type.String() {
			return
		}
	}

	t.Fatalf("%s registered nothing, so no host can build it", prusalink.Type)
}

func TestTheNameAnOperatorWrites(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("this binary carries no build info, so the module path cannot be read")
	}

	namespace, err := extension.NamespaceOf(info.Main.Path)
	if err != nil {
		t.Fatalf("no host can give %s a namespace: %v", info.Main.Path, err)
	}

	if qualified := namespace.String() + "/" + prusalink.Type.String(); qualified != Qualified {
		t.Fatalf("module %s registers as %q, and this driver documents %q", info.Main.Path, qualified, Qualified)
	}
}

// TestTheDeclaredDefaultsAreTheOnesTheCodeUses holds the settings table and the
// code to the same numbers.
func TestTheDeclaredDefaultsAreTheOnesTheCodeUses(t *testing.T) {
	want := map[string]time.Duration{"timeout": prusalink.DefaultTimeout, "poll_interval": prusalink.DefaultPollInterval}

	for _, option := range prusalink.Doc().Options {
		code, known := want[option.Key]
		if !known {
			t.Fatalf("the setting %q is declared and the code reads no such key", option.Key)
		}

		parsed, err := time.ParseDuration(option.Default)
		if err != nil || parsed != code {
			t.Fatalf("%s declares %q and the code uses %s", option.Key, option.Default, code)
		}

		if option.Summary == "" || len(option.Summary) > 120 {
			t.Fatalf("the summary of %s is %d characters. Write one sentence of at most 120", option.Key, len(option.Summary))
		}

		delete(want, option.Key)
	}

	if len(want) != 0 {
		t.Fatalf("the code reads settings that are not declared: %v", want)
	}
}

func TestAConfigBlockThatCannotBeReadIsRefusedByCode(t *testing.T) {
	for _, block := range []string{`{"timeout":"not a duration"}`, `{"poll_interval":"-1s"}`} {
		_, err := prusalink.New(connectorapi.Spec{Config: []byte(block)})
		if !fault.HasCode(err, prusalink.InvalidConfig.Code()) {
			t.Fatalf("%s: the refusal is %v, want %s", block, err, prusalink.InvalidConfig.Code())
		}
	}
}

func TestAConfigBlockTheDriverCanReadIsAccepted(t *testing.T) {
	if _, err := prusalink.New(connectorapi.Spec{Config: []byte(`{"timeout":"3s","poll_interval":"5s"}`)}); err != nil {
		t.Fatalf("a readable config block was refused: %v", err)
	}
}

func TestAPrinterThatDoesNotAnswerIsReportedByCode(t *testing.T) {
	dead := httptest.NewServer(http.NewServeMux())
	dead.Close()

	_, err := prusalink.NewDriver(time.Second).
		Connect(t.Context(), connectorapi.Endpoint{Address: dead.URL}, prusalink.NewDriver(0).Baseline())
	if !fault.HasCode(err, prusalink.DeviceUnreachable.Code()) {
		t.Fatalf("the failure is %v, want %s", err, prusalink.DeviceUnreachable.Code())
	}
}

// TestTheIdentityComesFromThePrinter reads the model from the type code and
// takes the serial number as the stable identity.
func TestTheIdentityComesFromThePrinter(t *testing.T) {
	printer := startPrinter(t, prusalinksim.Options{Serial: "CZPX1234X004XK12345", Printer: "1.2.6"})
	session := connect(t, printer, nil)

	identity := session.Identity()
	if identity.Model != "MK2.5S" || identity.Vendor != "Prusa Research" ||
		identity.UnitUID != "prusalink:CZPX1234X004XK12345" || identity.Firmware.String() != "3.14.1-8237" {
		t.Fatalf("the identity is %+v", identity)
	}
}

// TestDigestSignIn signs in as the PrusaLink user, the way an MK3S with a
// Raspberry Pi asks for, and refuses a wrong password.
func TestDigestSignIn(t *testing.T) {
	printer := startPrinter(t, prusalinksim.Options{User: "maker", Password: "s3cret"})

	connect(t, printer, map[string]string{"username": "maker", "password": "s3cret"})

	_, err := prusalink.NewDriver(time.Second).Connect(t.Context(), connectorapi.Endpoint{
		Address: printer.Address(), Options: map[string]string{"username": "maker", "password": "wrong"},
	}, prusalink.NewDriver(0).Baseline())
	if !fault.HasCode(err, prusalink.DeviceUnreachable.Code()) {
		t.Fatalf("a wrong password gave %v, want %s", err, prusalink.DeviceUnreachable.Code())
	}
}

// TestAPrintFromStartToStop follows one print: the telemetry, its id and name,
// pause, resume and stop, and the outcome written once.
func TestAPrintFromStartToStop(t *testing.T) {
	printer := startPrinter(t, prusalinksim.Options{APIKey: "k3y"})
	session := connect(t, printer, map[string]string{"api_key": "k3y"})

	tree := collect(session)

	result, err := session.Do(t.Context(), connectorapi.Command{Verb: prusalink.VerbPausePrint})
	if err != nil || result.OK || result.FaultCode != prusalink.NoJob.Code().String() {
		t.Fatalf("a pause with no print gave %+v, %v", result, err)
	}

	printer.StartPrint(42, "T500_Adapter.gcode")
	tree.waitFor(t, "/job/name", `"T500_Adapter.gcode"`)
	tree.waitFor(t, "/job/id", `"42"`)
	tree.waitFor(t, "/tools/0/nozzle_temp", `214.8`)
	tree.waitFor(t, "/temperature/bed_target", `60`)

	do(t, session, prusalink.VerbPausePrint)
	tree.waitFor(t, "/state", `"PAUSED"`)

	result, err = session.Do(t.Context(), connectorapi.Command{Verb: prusalink.VerbPausePrint})
	if err != nil || result.OK || result.FaultCode != prusalink.NotNow.Code().String() {
		t.Fatalf("a pause of a paused print gave %+v, %v", result, err)
	}

	do(t, session, prusalink.VerbResumePrint)
	tree.waitFor(t, "/state", `"PRINTING"`)

	do(t, session, prusalink.VerbStopPrint)
	tree.waitFor(t, "/job/outcome", `"cancelled"`)

	// PrusaLink keeps the ended job in the status. The outcome is not written
	// again for it.
	time.Sleep(10 * testInterval)

	if count := tree.count("/job/outcome"); count != 1 {
		t.Fatalf("the outcome was written %d times, want once", count)
	}
}

// TestASilentPrinterEndsTheSession proves that a printer that stops answering
// ends the session, so the host dials it again.
func TestASilentPrinterEndsTheSession(t *testing.T) {
	printer := startPrinter(t, prusalinksim.Options{})
	session := connect(t, printer, nil)

	printer.Close()

	deadline := time.After(5 * time.Second)

	for {
		select {
		case _, open := <-session.State():
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("the session went on after the printer stopped answering")
		}
	}
}

func startPrinter(t *testing.T, opts prusalinksim.Options) *prusalinksim.Printer {
	t.Helper()

	printer := prusalinksim.Start(opts)
	t.Cleanup(printer.Close)

	return printer
}

func connect(t *testing.T, printer *prusalinksim.Printer, options map[string]string) connectorapi.Session {
	t.Helper()

	driver := prusalink.NewDriver(time.Second).WithPollInterval(testInterval)

	session, err := driver.Connect(t.Context(),
		connectorapi.Endpoint{Address: printer.Address(), Options: options}, driver.Baseline())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	t.Cleanup(func() { _ = session.Close() })

	return session
}

func do(t *testing.T, session connectorapi.Session, verb connectorapi.Capability) {
	t.Helper()

	result, err := session.Do(t.Context(), connectorapi.Command{Verb: verb})
	if err != nil || !result.OK {
		t.Fatalf("%s gave %+v, %v", verb, result, err)
	}
}

// tree keeps every patch a session sent, in order, for a test to read.
type tree struct {
	patches chan connectorapi.StatePatch
	seen    []connectorapi.StatePatch
}

func collect(session connectorapi.Session) *tree {
	out := &tree{patches: make(chan connectorapi.StatePatch, 1024)}

	go func() {
		for patch := range session.State() {
			out.patches <- patch
		}
	}()

	return out
}

func (tr *tree) drain() {
	for {
		select {
		case patch := <-tr.patches:
			tr.seen = append(tr.seen, patch)
		default:
			return
		}
	}
}

func (tr *tree) waitFor(t *testing.T, path, value string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		tr.drain()

		for _, patch := range tr.seen {
			if patch.Path == path && compact(patch.Value) == value {
				return
			}
		}

		time.Sleep(testInterval)
	}

	t.Fatalf("no patch set %s to %s", path, value)
}

func (tr *tree) count(path string) int {
	tr.drain()

	n := 0

	for _, patch := range tr.seen {
		if patch.Path == path {
			n++
		}
	}

	return n
}

func compact(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}

	out, _ := json.Marshal(v)

	return string(out)
}
