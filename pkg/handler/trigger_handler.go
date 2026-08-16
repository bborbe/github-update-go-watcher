// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/bborbe/errors"
	"github.com/bborbe/run"
	"github.com/golang/glog"
	"github.com/gorilla/mux"

	"github.com/bborbe/github-update-go-watcher/pkg"
)

//counterfeiter:generate -o ../../mocks/trigger_handler.go --fake-name TriggerHandler . TriggerHandler

// TriggerHandler handles POST /trigger.
//
// It runs the forced cycle IN-PROCESS: the request acquires the single-cycle
// slot, hands the cycle to a run.BackgroundRunner bound to the application's
// long-lived context, and returns 202 immediately. There is no Kafka command
// topic, no command consumer and no key-value store behind this endpoint —
// that surface is not worth its cost for an endpoint whose only caller is an
// operator's curl (spec § Deliberate deviation from the template).
//
// Security: the handler reads ONLY the optional ?force=<bool> query parameter.
// It takes no owner, repo or scope parameter, so a forced cycle can only
// re-examine repos that already pass the allowlist and the per-repo opt-in
// gate. Unknown query parameters are ignored.
type TriggerHandler interface {
	ServeHTTP(ctx context.Context, resp http.ResponseWriter, req *http.Request) error
}

// httpAdapter wraps a TriggerHandler into an http.Handler so it can be
// registered with gorilla/mux.
type httpAdapter struct {
	h TriggerHandler
}

func (a *httpAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := a.h.ServeHTTP(r.Context(), w, r); err != nil {
		glog.Warningf("trigger handler error: %v", err)
	}
}

// NewTriggerHandler returns the forced-cycle handler. baseCtx is the
// application's long-lived context: the background cycle must NOT run under
// the request context, which is cancelled the moment the 202 is written.
// It is bound here into a run.BackgroundRunner, which owns the goroutine.
func NewTriggerHandler(
	baseCtx context.Context,
	watcher pkg.Watcher,
	gate pkg.CycleGate,
) TriggerHandler {
	return &triggerHandler{
		runner:  run.NewBackgroundRunner(baseCtx),
		watcher: watcher,
		gate:    gate,
	}
}

// NewTriggerHandlerHTTPAdapter wraps a TriggerHandler in an http.Handler
// suitable for registration with gorilla/mux.
func NewTriggerHandlerHTTPAdapter(
	baseCtx context.Context,
	watcher pkg.Watcher,
	gate pkg.CycleGate,
) http.Handler {
	return &httpAdapter{NewTriggerHandler(baseCtx, watcher, gate)}
}

type triggerHandler struct {
	runner  run.BackgroundRunner
	watcher pkg.Watcher
	gate    pkg.CycleGate
}

func (h *triggerHandler) ServeHTTP(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
) error {
	_ = mux.Vars(req) // ignored, but must not cause issues
	forceStr := req.URL.Query().Get("force")
	force := forceStr == "true" || forceStr == "1"

	if !h.gate.TryAcquire() {
		glog.Warningf("trigger rejected: a poll cycle is already running")
		resp.Header().Set("Content-Type", "application/json")
		resp.WriteHeader(http.StatusConflict)
		return json.NewEncoder(resp).Encode(map[string]interface{}{
			"status": "conflict",
			"error":  "a poll cycle is already running",
		})
	}

	// CatchPanic converts a panic in Poll into an error instead of taking the
	// whole process down. The inner defer still releases the gate first: a
	// deferred call runs during panic unwinding, so the gate is freed either
	// way — what a bare goroutine would lose is the process, not the slot.
	if err := h.runner.Run(run.CatchPanic(func(ctx context.Context) error {
		defer h.gate.Release()
		if err := h.watcher.Poll(ctx, force); err != nil {
			return errors.Wrapf(ctx, err, "forced poll cycle failed force=%t", force)
		}
		return nil
	})); err != nil {
		h.gate.Release()
		glog.Errorf("failed to start forced poll cycle force=%t err=%v", force, err)
		resp.Header().Set("Content-Type", "application/json")
		resp.WriteHeader(http.StatusInternalServerError)
		return json.NewEncoder(resp).Encode(map[string]interface{}{
			"status": "error",
			"error":  "failed to start poll cycle",
		})
	}

	glog.Warningf("forced poll cycle accepted force=%t", force)
	resp.Header().Set("Content-Type", "application/json")
	resp.WriteHeader(http.StatusAccepted)
	return json.NewEncoder(resp).Encode(map[string]interface{}{
		"status": "accepted",
	})
}
