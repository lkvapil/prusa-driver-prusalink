package prusalink

import (
	"encoding/json"
	"math"
	"strconv"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

// Where a PrusaLink reading lands in the state tree. The paths are the ones
// the xBuddy dialect writes for an MK4S, so a client reads an MK3S and an MK4S
// at the same addresses.
const (
	statePath        = "/state"
	nozzlePath       = "/tools/0/nozzle_temp"
	nozzleTargetPath = "/tools/0/nozzle_target_temp"
	nozzleDiamPath   = "/tools/0/features/nozzle_diameter"
	printFanPath     = "/tools/0/print_fan_rpm"
	heatbreakFanPath = "/tools/0/heatbreak_fan_rpm"
	bedPath          = "/temperature/bed"
	bedTargetPath    = "/temperature/bed_target"
	speedPath        = "/speed"
	flowPath         = "/flow"
	axisXPath        = "/axis/x"
	axisYPath        = "/axis/y"
	axisZPath        = "/axis/z"
	jobNamePath      = "/job/name"
	jobIDPath        = connectorapi.JobIDPath
	jobProgressPath  = connectorapi.JobProgressPath
	jobPrintingPath  = connectorapi.JobTimePrintingPath
	jobRemainingPath = connectorapi.JobTimeRemainingPath
	jobOutcomePath   = connectorapi.JobOutcomePath
	decimalsKept     = 100
	unknownStateWord = "UNKNOWN"
)

// outcomes maps the state a print ends in onto the closed set the host
// publishes. A print that ends in another state writes no outcome, and the host
// then publishes it as unknown.
//
//nolint:gochecknoglobals // a lookup table, read-only after init.
var outcomes = map[string]connectorapi.JobOutcome{
	"FINISHED": connectorapi.JobFinished,
	"STOPPED":  connectorapi.JobCancelled,
	"ERROR":    connectorapi.JobFailed,
}

// reader is what this driver remembers between two readings of one printer.
// A reading alone does not say that a print has just ended, so the job of the
// reading before is kept.
type reader struct {
	// job is the print on the bed in the last reading, zero when none.
	job int64
	// name is the file of that print, written once per print.
	name string
	// ended is the last print whose outcome was written. PrusaLink keeps an
	// ended job in the status for a while, and its outcome is written once.
	ended int64
}

// patchesFrom turns one reading into patches, in a fixed order, so two runs of
// one reading produce the same list. jobName is the file the print reads, or
// empty when it is not known yet.
//
// A field the printer did not send produces no patch. A field PrusaLink sends
// on one model and not another must not blank what the tree holds.
func (r *reader) patchesFrom(status statusReply, jobName string) []connectorapi.StatePatch {
	var out []connectorapi.StatePatch

	p := status.Printer
	state := p.State

	if state == "" {
		state = unknownStateWord
	}

	out = appendText(out, statePath, state)

	out = r.appendJob(out, status.Job, state, jobName)

	out = appendNumber(out, nozzlePath, p.TempNozzle)
	out = appendNumber(out, nozzleTargetPath, p.TargetNozzle)
	out = appendNumber(out, bedPath, p.TempBed)
	out = appendNumber(out, bedTargetPath, p.TargetBed)
	out = appendNumber(out, axisXPath, p.AxisX)
	out = appendNumber(out, axisYPath, p.AxisY)
	out = appendNumber(out, axisZPath, p.AxisZ)
	out = appendNumber(out, speedPath, p.Speed)
	out = appendNumber(out, flowPath, p.Flow)
	out = appendNumber(out, printFanPath, p.FanPrint)
	out = appendNumber(out, heatbreakFanPath, p.FanHotend)

	return out
}

// appendJob writes the print on the bed, and the outcome of a print that has
// just left it.
//
// The host names the row and the events of a print by /job/id, so every other
// job path is written only beside an id.
func (r *reader) appendJob(
	out []connectorapi.StatePatch, job *statusJob, state, jobName string,
) []connectorapi.StatePatch {
	if job == nil || job.ID == nil {
		if r.job != 0 {
			// The print has left the bed. The outcome goes beside the id it
			// belongs to, so the host closes the right row.
			out = appendText(out, jobIDPath, strconv.FormatInt(r.job, 10))

			if outcome, ended := outcomes[state]; ended {
				out = appendText(out, jobOutcomePath, outcome)
			}

			r.ended, r.job, r.name = r.job, 0, ""
		}

		return out
	}

	if *job.ID == r.ended {
		return out
	}

	if *job.ID != r.job {
		r.job, r.name = *job.ID, ""
	}

	out = appendText(out, jobIDPath, strconv.FormatInt(*job.ID, 10))

	if jobName != "" && jobName != r.name {
		r.name = jobName
		out = appendText(out, jobNamePath, jobName)
	}

	out = appendNumber(out, jobProgressPath, job.Progress)
	out = appendNumber(out, jobPrintingPath, job.TimePrinting)
	out = appendNumber(out, jobRemainingPath, job.TimeRemaining)

	if outcome, ended := outcomes[state]; ended {
		// PrusaLink keeps the job in the status for a while after it ended,
		// with the state that ended it. The outcome is written once.
		out = appendText(out, jobOutcomePath, outcome)
		r.ended, r.job, r.name = *job.ID, 0, ""
	}

	return out
}

// appendText writes one string at a path.
func appendText(out []connectorapi.StatePatch, path, value string) []connectorapi.StatePatch {
	encoded, err := json.Marshal(value)
	if err != nil {
		return out
	}

	return append(out, connectorapi.StatePatch{Path: path, Value: encoded})
}

// appendNumber writes one number at a path, rounded to two decimals. A number
// the printer did not send writes nothing.
func appendNumber(out []connectorapi.StatePatch, path string, value *float64) []connectorapi.StatePatch {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return out
	}

	rounded := math.Round(*value*decimalsKept) / decimalsKept

	return append(out, connectorapi.StatePatch{
		Path:  path,
		Value: json.RawMessage(strconv.FormatFloat(rounded, 'f', -1, 64)),
	})
}
