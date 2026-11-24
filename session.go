package prusalink

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// Session is one connected printer.
//
// PrusaLink pushes nothing, so the session asks for the status on a timer. One
// goroutine does that, and it is the only writer to the patch channel and
// therefore the only thing that closes it.
type Session struct {
	identity connectorapi.Identity
	caps     connectorapi.Set
	probe    connectorapi.Set

	client *client
	every  time.Duration
	// silence is how long the printer may fail to answer before the session
	// ends. The host then dials again, which is how a printer that was switched
	// off comes back.
	silence time.Duration

	reader reader

	// storages is what the printer last said about its storages.
	storages storages
	// sending is true while a transfer runs. PrusaLink takes one at a time.
	sending atomic.Bool
	// job is the print the last status named, read by Do to address a pause,
	// a resume or a stop. Zero when nothing prints.
	job atomic.Int64

	// first is the reading Connect made. The poller writes it before its loop,
	// so the patch channel has exactly one writer.
	first []connectorapi.StatePatch

	patches chan connectorapi.StatePatch
	cancel  context.CancelFunc
	done    chan struct{}
	closing sync.Once

	dropped atomic.Uint64
	refused atomic.Uint64
}

var (
	_ connectorapi.Session      = (*Session)(nil)
	_ connectorapi.CountsLink   = (*Session)(nil)
	_ connectorapi.ReportsProbe = (*Session)(nil)
)

// start begins asking for the status. The context is detached from the call
// that built the session, because a session outlives Connect.
func (s *Session) start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	go s.poll(ctx)
}

// Identity is what the printer said it is.
func (s *Session) Identity() connectorapi.Identity { return s.identity }

// Capabilities is the resolved set and its epoch. The set is copied out, so a
// caller cannot move the set in force. The epoch is 1 and never changes: a
// printer whose abilities change is reached again as a new session.
func (s *Session) Capabilities() (connectorapi.Set, uint64) { return s.caps.Clone(), 1 }

// Probe is what this printer can do. PrusaLink has the same API on every
// printer it runs on, so the probe is the verbs this driver sends.
func (s *Session) Probe() connectorapi.Set { return s.probe.Clone() }

// State is the state tree's update channel. It closes when the session ends.
func (s *Session) State() <-chan connectorapi.StatePatch { return s.patches }

// Dropped is how many patches this session discarded because the host was not
// reading them.
func (s *Session) Dropped() uint64 { return s.dropped.Load() }

// Refused is how many status answers this session could not read.
func (s *Session) Refused() uint64 { return s.refused.Load() }

// Close ends the session. It is safe to call more than once. Cancelling the
// poller is what ends it, and the poller closes the channel, so there is one
// way out.
func (s *Session) Close() error {
	s.closing.Do(func() { s.cancel() })

	return nil
}

// Do sends one command to the printer and reports what it made of it.
//
// The arguments were checked against Capabilities before this was reached.
func (s *Session) Do(ctx context.Context, cmd connectorapi.Command) (connectorapi.Result, error) {
	if !s.caps.Has(cmd.Verb) {
		return connectorapi.Result{}, connectorapi.CapabilityNotSupported.
			New("the printer does not have that capability").
			With(fault.StringParam("verb", cmd.Verb.String()))
	}

	// Before the request and not only around it. A caller who has already given
	// up must not pause a print.
	if err := ctx.Err(); err != nil {
		return connectorapi.Result{}, err
	}

	if cmd.Verb == VerbSendStateInfo {
		var status statusReply
		if _, err := s.client.get(ctx, pathStatus, &status); err != nil {
			return connectorapi.Result{}, unreachable(s.client.base, "the printer did not answer the status").Wrap(err)
		}

		return connectorapi.Result{OK: true}, nil
	}

	switch cmd.Verb {
	case VerbStartPrint:
		return s.startPrint(ctx, cmd.Args[startPrintArgument])
	case connectorapi.VerbSendInfo, connectorapi.VerbSendFileInfo, connectorapi.VerbFileUpload:
		// The host reaches these through Storages, List and Send. Asked as a
		// command, each reads nothing and moves nothing.
		return connectorapi.Result{OK: true}, nil
	case connectorapi.VerbDeleteFile, connectorapi.VerbDeleteFolder:
		return connectorapi.Result{}, connectorapi.CapabilityNotSupported.
			New("a file is deleted through the file interface, not as a command").
			With(fault.StringParam("verb", cmd.Verb.String()))
	}

	job := s.job.Load()
	if job == 0 {
		return connectorapi.Result{OK: false, FaultCode: NoJob.Code().String(), MachineReason: "no print is running"}, nil
	}

	method, path := jobRequest(cmd.Verb, job)
	if path == "" {
		return connectorapi.Result{}, connectorapi.CapabilityNotSupported.
			New("this driver sends no request for that verb").
			With(fault.StringParam("verb", cmd.Verb.String()))
	}

	status, err := s.client.do(ctx, method, path, nil)
	if err != nil {
		var answered errStatus
		if errors.As(err, &answered) && answered.status == http.StatusConflict {
			// PrusaLink answers 409 when the print is not in a state that takes
			// the command, such as a resume of a print that is not paused.
			return connectorapi.Result{OK: false, FaultCode: NotNow.Code().String(), MachineReason: err.Error()}, nil
		}

		return connectorapi.Result{}, unreachable(s.client.base, "the printer did not take "+cmd.Verb.String()).Wrap(err)
	}

	return connectorapi.Result{OK: status < http.StatusMultipleChoices}, nil
}

// jobRequest is the request one verb becomes for the print with this id.
func jobRequest(verb connectorapi.Capability, job int64) (method, path string) {
	base := pathJob + "/" + strconv.FormatInt(job, 10)

	switch verb {
	case VerbPausePrint:
		return http.MethodPut, base + "/pause"
	case VerbResumePrint:
		return http.MethodPut, base + "/resume"
	case VerbStopPrint:
		return http.MethodDelete, base
	default:
		return "", ""
	}
}

// poll asks for the status until the session closes or the printer has not
// answered for longer than silence.
func (s *Session) poll(ctx context.Context) {
	defer close(s.patches)
	defer close(s.done)

	for _, patch := range s.first {
		s.report(patch)
	}

	ticker := time.NewTicker(s.every)
	defer ticker.Stop()

	lastAnswer := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if s.read(ctx) {
			lastAnswer = time.Now()

			continue
		}

		if ctx.Err() != nil || time.Since(lastAnswer) > s.silence {
			return
		}
	}
}

// read asks for the status once and reports what changed. It answers false
// when the printer did not answer.
func (s *Session) read(ctx context.Context) bool {
	var status statusReply
	if _, err := s.client.get(ctx, pathStatus, &status); err != nil {
		var answered errStatus
		if !errors.As(err, &answered) {
			return false
		}

		// The printer answered, but not with a status. It is there, so the
		// session goes on, and the answer is counted.
		s.refused.Add(1)

		return true
	}

	for _, patch := range s.patchesFrom(ctx, status) {
		s.report(patch)
	}

	return true
}

// patchesFrom turns a status into patches. The file name of a print is asked
// for once, when a new print appears, because the status does not carry it.
func (s *Session) patchesFrom(ctx context.Context, status statusReply) []connectorapi.StatePatch {
	name := ""

	if status.Job != nil && status.Job.ID != nil {
		if *status.Job.ID != s.reader.job && *status.Job.ID != s.reader.ended {
			var job jobReply
			if _, err := s.client.get(ctx, pathJob, &job); err == nil {
				name = job.name()
			}
		}

		s.job.Store(*status.Job.ID)
	} else {
		s.job.Store(0)
	}

	if outcome, ended := outcomes[status.Printer.State]; ended && outcome != "" {
		s.job.Store(0)
	}

	return s.reader.patchesFrom(status, name)
}

// report queues one patch for the host and never waits. A printer that reports
// faster than the host reads costs a fixed amount of memory: the oldest patch
// goes, because telemetry is a repeating reading.
func (s *Session) report(patch connectorapi.StatePatch) {
	select {
	case s.patches <- patch:
		return
	default:
	}

	select {
	case <-s.patches:
		s.dropped.Add(1)
	default:
	}

	select {
	case s.patches <- patch:
	default:
		s.dropped.Add(1)
	}
}
