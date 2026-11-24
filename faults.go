// The codes this driver fails with.
//
// A failure that reaches a person or a client is a registered code, never a
// bare English sentence. A command result carries the code out to whoever sent
// the command, the browser half picks a translation by it, and the English
// sentence travels beside it so a log line still reads.
//
// Registering a code takes its documentation with it: what went wrong, what the
// caller should do, whether sending the same request again can work, and the
// parameters the sentence names. The sdk refuses a registration without them.
package prusalink

import (
	"fmt"
	"net/http"

	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// faultDomain is the first half of every code below. It is this module's
// namespace, which a host derives from the Go module path github.com/lkvapil/prusa-driver-prusalink, so no
// other author can register a code under it. The sdk refuses the core
// namespace, which is what keeps this driver's codes apart from the server's.
//
//nolint:gochecknoglobals // a domain built once at start-up; a const cannot hold one.
var faultDomain = mustDomain("com.github.aux.lkvapil.prusa-driver-prusalink")

// The codes this driver raises. Add a code here, and nowhere else, so the list
// a caller branches on is one file.
//
//nolint:gochecknoglobals // definitions built once at start-up.
var (
	// InvalidConfig is a `config` block this driver cannot act on. The operator
	// has to change the configuration file, so no retry can help.
	InvalidConfig = fault.Register(faultDomain, "INVALID_CONFIG", http.StatusBadRequest, fault.Document(
		"The configuration for this printer cannot be read",
		"Correct the connector's config block and reload. The message names the key.",
		fault.RetryNever,
		fault.ShowDetail,
		"key",
	))

	// DeviceUnreachable is a printer that did not answer, or answered something
	// this dialect cannot read. A printer that is switched off or still booting
	// answers the next attempt, so a retry is allowed.
	DeviceUnreachable = fault.Register(faultDomain, "DEVICE_UNREACHABLE", http.StatusBadGateway, fault.Document(
		"The printer did not answer",
		"Check that the printer is switched on and that the address in the connector's config block reaches it.",
		fault.RetryAllowed,
		fault.ShowDetail,
		"address",
	))

	// NoJob is a pause, a resume or a stop sent while nothing prints. The
	// command reached no printer, and it can work once a print runs.
	NoJob = fault.Register(faultDomain, "NO_JOB", http.StatusConflict, fault.Document(
		"No print is running",
		"Start a print first. Pause, resume and stop act on the print on the bed.",
		fault.RetryAllowed,
		fault.ShowDetail,
	))

	// NotNow is a command PrusaLink refused because the print is not in a state
	// that takes it, such as a resume of a print that is not paused.
	NotNow = fault.Register(faultDomain, "NOT_NOW", http.StatusConflict, fault.Document(
		"The printer cannot do that now",
		"Wait until the print is in a state that takes the command, then send it again.",
		fault.RetryAllowed,
		fault.ShowDetail,
	))
)

// mustDomain builds this driver's fault domain. A name the sdk refuses is a
// mistake in this file, so it stops the process at start-up rather than
// reporting a failure with an empty code.
func mustDomain(name string) fault.Domain {
	domain, err := fault.NewDomain(name)
	if err != nil {
		panic(fmt.Sprintf("prusalink: %s is not a fault domain: %v", name, err))
	}

	return domain
}
