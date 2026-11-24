package prusalink

// The states PrusaLink reports at /state, the moves between them, and the verbs
// each one accepts. The words are PrusaLink's own, and they are the words the
// xBuddy dialect reports, so a client branches on one vocabulary.
//
// A print starts from the printer's screen, from PrusaSlicer or from the
// PrusaLink page, so a move into PRINTING is one the printer makes on its own.

import (
	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

const (
	stateIdle      connectorapi.StateName = "IDLE"
	stateReady     connectorapi.StateName = "READY"
	stateBusy      connectorapi.StateName = "BUSY"
	statePrinting  connectorapi.StateName = "PRINTING"
	statePaused    connectorapi.StateName = "PAUSED"
	stateFinished  connectorapi.StateName = "FINISHED"
	stateStopped   connectorapi.StateName = "STOPPED"
	stateError     connectorapi.StateName = "ERROR"
	stateAttention connectorapi.StateName = "ATTENTION"
)

// states is this dialect's declared state set.
func states() connectorapi.StateSet {
	read := []connectorapi.Capability{VerbSendStateInfo}

	return connectorapi.StateSet{
		States: []connectorapi.DeviceState{
			{Name: stateIdle, DisplayName: "Idle", Accepts: read},
			{Name: stateReady, DisplayName: "Ready", Accepts: read},
			{Name: stateBusy, DisplayName: "Busy", Accepts: read},
			{Name: statePrinting, DisplayName: "Printing", Accepts: append(
				[]connectorapi.Capability{VerbPausePrint, VerbStopPrint}, read...)},
			{Name: statePaused, DisplayName: "Paused", Accepts: append(
				[]connectorapi.Capability{VerbResumePrint, VerbStopPrint}, read...)},
			{Name: stateFinished, DisplayName: "Finished", Accepts: read},
			{Name: stateStopped, DisplayName: "Stopped", Accepts: read},
			{Name: stateError, DisplayName: "Error", Accepts: read},
			{Name: stateAttention, DisplayName: "Needs attention", Accepts: append(
				[]connectorapi.Capability{VerbStopPrint}, read...)},
		},
		Transitions: []connectorapi.Transition{
			{From: stateIdle, To: statePrinting},
			{From: stateReady, To: statePrinting},
			{From: stateFinished, To: statePrinting},
			{From: stateStopped, To: statePrinting},
			{From: statePrinting, To: statePaused, Verb: VerbPausePrint},
			{From: statePaused, To: statePrinting, Verb: VerbResumePrint},
			{From: statePrinting, To: stateStopped, Verb: VerbStopPrint},
			{From: statePaused, To: stateStopped, Verb: VerbStopPrint},
			{From: stateAttention, To: stateStopped, Verb: VerbStopPrint},
			{From: statePrinting, To: stateAttention},
			{From: stateAttention, To: statePrinting},
			{From: statePrinting, To: stateFinished},
			{From: statePrinting, To: stateError},
			{From: stateFinished, To: stateIdle},
			{From: stateStopped, To: stateIdle},
			{From: stateError, To: stateIdle},
			{From: stateIdle, To: stateBusy},
			{From: stateBusy, To: stateIdle},
		},
	}
}
