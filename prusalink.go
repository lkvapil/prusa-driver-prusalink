// Package prusalink is a printer driver for PrusaLink, the web server Prusa
// runs on a Raspberry Pi attached to an Original Prusa MK3S, MK3S+, MK3 or
// MK2.5S. These printers have the RAMBo board, which cannot speak to Prusa
// Connect itself, so a Raspberry Pi Zero 2 W on the printer's GPIO header runs
// PrusaLink and drives the printer over its serial line.
//
// It is an outbound dialect: the host dials the address the endpoint list
// holds. PrusaLink pushes nothing, so the session asks GET /api/v1/status on a
// timer and writes what changed at the paths the xBuddy dialect writes for an
// MK4S. A client reads both printers at the same addresses.
//
// PrusaLink signs a request in one of two ways, and the endpoint options say
// which:
//
//	api_key   an API key set in PrusaLink. It goes in the X-Api-Key header
//	username  the PrusaLink user, for HTTP Digest. The default user is "maker"
//	password  that user's password, shown in the printer's menu
//	unit_uid  the stable identity. Empty uses the serial number the printer reports
//
// What this driver does in version 0.1: telemetry, the print on the bed, and
// pause, resume and stop. PrusaLink's API has no request to set a temperature,
// so this driver offers none.
package prusalink

import (
	"context"
	_ "embed"
	"net/http"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// Type is the value of a connector's `type` in configuration that selects this
// driver.
const Type connectorapi.TypeName = "prusalink"

// The capabilities this driver can carry out. Every key is one the published
// vocabulary already names, so a client that drives an MK4S drives an MK3S with
// the same words.
const (
	// VerbSendStateInfo reads the printer's state and moves nothing, which is
	// what makes it the verb a conformance run may send.
	VerbSendStateInfo = connectorapi.Capability("core.command.send_state_info")
	// VerbPausePrint pauses the running print.
	VerbPausePrint = connectorapi.Capability("core.command.pause_print")
	// VerbResumePrint resumes a paused print.
	VerbResumePrint = connectorapi.Capability("core.command.resume_print")
	// VerbStopPrint stops the running print.
	VerbStopPrint = connectorapi.Capability("core.command.stop_print")
)

const (
	// DefaultTimeout bounds one request to the printer when the operator sets
	// no timeout. It is the default the `timeout` setting declares in Doc.
	DefaultTimeout = 10 * time.Second

	// DefaultPollInterval is how often the status is read when the operator
	// sets nothing. A Raspberry Pi Zero answers a status in tens of
	// milliseconds, so two seconds costs it nothing.
	DefaultPollInterval = 2 * time.Second

	// silenceInPolls is how many intervals the printer may fail to answer
	// before the session ends and the host dials again.
	silenceInPolls = 15

	// patchBacklog bounds how many patches may wait for the host.
	patchBacklog = 64
)

// The endpoint options this dialect reads.
const (
	optionAPIKey   = "api_key"
	optionUser     = "username"
	optionPassword = "password"
	optionUnitUID  = "unit_uid"

	vendor     = "Prusa Research"
	unitPrefix = "prusalink:"
)

//nolint:gochecknoinits // registration has to happen before main; the host reaches it through a blank import.
func init() { connectorapi.Register(Type, Doc(), New, dialect()) }

// models maps the type code PrusaLink reports in /api/version onto the model
// this driver reports. The MK3S+ reports the code of the MK3S.
//
//nolint:gochecknoglobals // a lookup table, read-only after init.
var models = map[string]string{
	"1.3.0": "MK3",
	"1.3.1": "MK3S",
	"1.2.5": "MK2.5",
	"1.2.6": "MK2.5S",
}

// fallbackModel is what a printer that reports no known type code is called.
const fallbackModel = "MK3S"

// dialect describes the printers of this driver to the page that adds one.
// The core has rows only for the Prusa printers with a Buddy board, so the
// RAMBo printers are declared here.
func dialect() connectorapi.Dialect {
	row := func(model, display string) connectorapi.DeviceModel {
		return connectorapi.DeviceModel{
			Model: model, DisplayName: display, Vendor: vendor,
			Artwork: connectorapi.Artwork{MediaType: connectorapi.ArtworkSVG, Bytes: artwork},
		}
	}

	return connectorapi.Dialect{
		Brands: []string{"Original Prusa"},
		Needs:  connectorapi.NeedsAddress,
		Models: []connectorapi.DeviceModel{
			row("MK3S", "Original Prusa MK3S/MK3S+"),
			row("MK3", "Original Prusa MK3"),
			row("MK2.5S", "Original Prusa MK2.5S"),
			row("MK2.5", "Original Prusa MK2.5"),
		},
		States: states(),
	}
}

// artwork is the drawing of a printer with a Raspberry Pi.
//
//go:embed artwork.svg
var artwork []byte

// Doc is what the published extension reference says about this driver, and
// the settings table an operator reads. It is exported so a test can hold the
// declared defaults to the code's.
func Doc() extension.Doc {
	return extension.Doc{
		Summary: "An Original Prusa MK3S, MK3 or MK2.5S with PrusaLink on a Raspberry Pi, over its HTTP API.",
		Description: "The host dials the address the endpoint list holds and reads PrusaLink's status " +
			"on a timer. It writes the readings at the paths the xBuddy dialect writes for an MK4S, " +
			"so a client reads both printers the same way. It can pause, resume and stop a print. " +
			"The endpoint options api_key, or username and password, sign the requests.",
		Version:   "0.1.0",
		Contract:  connectorapi.Contract,
		Stability: extension.StabilityExperimental,
		Options: []extension.Option{
			{
				Key:     "timeout",
				Type:    extension.OptionDuration,
				Default: DefaultTimeout.String(),
				Summary: "How long one request to the printer may take before it is abandoned.",
			},
			{
				Key:     "poll_interval",
				Type:    extension.OptionDuration,
				Default: DefaultPollInterval.String(),
				Summary: "How often the printer's status is read.",
			},
		},
	}
}

// Config is what an operator writes under a connector's `config` block.
type Config struct {
	// Timeout bounds one request to the printer, as time.ParseDuration reads it.
	Timeout string `json:"timeout,omitempty"`
	// PollInterval is how often the status is read.
	PollInterval string `json:"poll_interval,omitempty"`
}

// Driver is the connectorapi.Outbound this package registers. It holds no
// per-printer state. That is on the Session built for each printer.
type Driver struct {
	timeout time.Duration
	every   time.Duration
	http    *http.Client
}

var _ connectorapi.Outbound = (*Driver)(nil)

// New builds a Driver from its spec. It is the connectorapi.Factory for Type.
func New(spec connectorapi.Spec) (connectorapi.Driver, error) {
	var cfg Config
	if err := spec.DecodeConfig(&cfg); err != nil {
		return nil, InvalidConfig.New("the config block is not readable").Wrap(err)
	}

	timeout, err := duration("timeout", cfg.Timeout, DefaultTimeout)
	if err != nil {
		return nil, err
	}

	every, err := duration("poll_interval", cfg.PollInterval, DefaultPollInterval)
	if err != nil {
		return nil, err
	}

	driver := NewDriver(timeout)
	driver.every = every

	return driver, nil
}

// duration reads one positive duration from the config block.
func duration(key, written string, fallback time.Duration) (time.Duration, error) {
	if written == "" {
		return fallback, nil
	}

	parsed, err := time.ParseDuration(written)
	if err != nil {
		return 0, InvalidConfig.New(key+" is not a duration").With(fault.StringParam("key", key)).Wrap(err)
	}

	if parsed <= 0 {
		return 0, InvalidConfig.New(key+" must be positive, and it is "+parsed.String()).
			With(fault.StringParam("key", key))
	}

	return parsed, nil
}

// NewDriver returns a driver with its timeout given directly, for tests and for
// a host that wires its own built-ins. Zero or less takes the default.
func NewDriver(timeout time.Duration) *Driver {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	return &Driver{timeout: timeout, every: DefaultPollInterval, http: &http.Client{}}
}

// WithPollInterval returns the driver with another interval, for a test that
// cannot wait two seconds.
func (d *Driver) WithPollInterval(every time.Duration) *Driver {
	if every > 0 {
		d.every = every
	}

	return d
}

// Baseline is what this driver can do, for any PrusaLink printer. It returns a
// fresh set each time.
func (d *Driver) Baseline() connectorapi.Set {
	return connectorapi.Set{
		VerbSendStateInfo: {Flag: true},
		VerbPausePrint:    {Flag: true},
		VerbResumePrint:   {Flag: true},
		VerbStopPrint:     {Flag: true},
	}
}

// Connect reads what the printer is and its first status, and returns a
// session that reads the status from then on. profile is what the host offers,
// and the result is never wider than it.
func (d *Driver) Connect(
	ctx context.Context, endpoint connectorapi.Endpoint, profile connectorapi.Set,
) (connectorapi.Session, error) {
	base, err := baseURL(endpoint.Address)
	if err != nil {
		return nil, InvalidConfig.New(err.Error()).With(fault.StringParam("key", "address"))
	}

	c := &client{
		http:     d.http,
		base:     base,
		timeout:  d.timeout,
		apiKey:   endpoint.Options[optionAPIKey],
		user:     endpoint.Options[optionUser],
		password: endpoint.Options[optionPassword],
	}

	var version versionReply
	if _, err = c.get(ctx, pathVersion, &version); err != nil {
		return nil, unreachable(base, "the printer did not answer "+pathVersion).Wrap(err)
	}

	var info infoReply
	if _, err = c.get(ctx, pathInfo, &info); err != nil {
		return nil, unreachable(base, "the printer did not answer "+pathInfo).Wrap(err)
	}

	probe := d.Baseline().Intersect(profile)

	s := &Session{
		identity: identityOf(endpoint, base, version, info),
		caps:     probe,
		probe:    probe.Clone(),
		client:   c,
		every:    d.every,
		silence:  d.every * silenceInPolls,
		patches:  make(chan connectorapi.StatePatch, patchBacklog),
		done:     make(chan struct{}),
	}

	var status statusReply
	if _, err = c.get(ctx, pathStatus, &status); err != nil {
		return nil, unreachable(base, "the printer did not answer "+pathStatus).Wrap(err)
	}

	s.first = append(appendNumber(nil, nozzleDiamPath, info.NozzleDiameter), s.patchesFrom(ctx, status)...)
	s.start()

	return s, nil
}

// identityOf builds the identity from what the printer reported and what the
// endpoint options say. The unit id is the serial number, which survives a new
// address and a new Raspberry Pi.
func identityOf(endpoint connectorapi.Endpoint, base string, version versionReply, info infoReply) connectorapi.Identity {
	model, known := models[version.Printer]
	if !known {
		model = fallbackModel
	}

	unit := endpoint.Options[optionUnitUID]

	switch {
	case unit != "":
	case info.Serial != "":
		unit = unitPrefix + info.Serial
	default:
		unit = unitPrefix + strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://")
	}

	return connectorapi.Identity{
		Vendor:   vendor,
		Model:    model,
		Firmware: connectorapi.NewFirmwareLabel(version.Firmware),
		Serial:   info.Serial,
		UnitUID:  unit,
	}
}

// unreachable is one DEVICE_UNREACHABLE naming the address that did not
// answer, which is the value an operator needs in order to act on it.
func unreachable(address, message string) *fault.Fault {
	return DeviceUnreachable.New(message).With(fault.StringParam("address", address))
}
