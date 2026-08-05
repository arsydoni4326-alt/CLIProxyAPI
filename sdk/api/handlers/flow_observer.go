package handlers

import (
	"context"
	"sync/atomic"
)

// FlowObserver is notified about websocket conversation turns so listeners
// (e.g. the live-flow visualization hub) can mirror per-turn metadata without
// importing request-handling internals. Implementations must be fast,
// non-blocking and metadata-only, and must never mutate the execution context.
//
// The interface is declared in this package (and injected from internal/api via
// SetFlowObserver) because sdk/api/handlers/openai cannot import internal/api:
// internal/api already imports the sdk handler packages, and Go forbids import
// cycles.
type FlowObserver interface {
	// WSMessageStart fires right before a websocket conversation turn (one
	// request frame) is executed. The request ctx carries the originating
	// *gin.Context under the "gin" key; payload is the raw frame bytes.
	// requestID correlates the turn with the matching WSMessageComplete call.
	WSMessageStart(ctx context.Context, requestID string, payload []byte)
	// WSMessageComplete fires when the turn ends (successfully or not) with the
	// HTTP-style status code of the outcome.
	WSMessageComplete(ctx context.Context, requestID string, status int)
}

// flowObserver holds the process-wide observer. It is unset (nil) unless flow
// visualization is enabled, so the hot websocket path costs a single atomic
// load when inactive.
var flowObserver atomic.Value // stores FlowObserver

// SetFlowObserver installs the process-wide websocket flow observer. Intended
// to be called once during server construction; nil observers are ignored (the
// server simply never calls this when flow visualization is disabled).
func SetFlowObserver(obs FlowObserver) {
	if obs == nil {
		return
	}
	flowObserver.Store(obs)
}

// CurrentFlowObserver returns the installed observer, or nil when flow
// visualization is disabled / no observer was registered.
func CurrentFlowObserver() FlowObserver {
	if v := flowObserver.Load(); v != nil {
		if obs, ok := v.(FlowObserver); ok {
			return obs
		}
	}
	return nil
}
