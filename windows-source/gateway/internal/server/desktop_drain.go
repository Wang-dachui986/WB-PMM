package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
)

// desktopDrainState counts the entire admitted handler lifetime, including body
// reads and account selection. Admission and begin must use the same lock.
type desktopDrainState struct {
	mu         sync.Mutex
	generation uint64 // Retained after end so an old begin cannot close admission again.
	active     bool
	inFlight   uint64
}

func (d *desktopDrainState) admit() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.active {
		return false
	}
	d.inFlight++
	return true
}

func (d *desktopDrainState) release() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inFlight--
}

type desktopDrainResponse struct {
	OK         bool   `json:"ok"`
	Generation uint64 `json:"generation"`
	Drained    bool   `json:"drained"`
	Accepting  bool   `json:"accepting"`
	InFlight   uint64 `json:"in_flight"`
}

func (d *desktopDrainState) update(operation string, generation uint64) (desktopDrainResponse, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch operation {
	case "begin":
		if d.active {
			if generation != d.generation {
				return desktopDrainResponse{}, false
			}
		} else {
			if generation <= d.generation {
				return desktopDrainResponse{}, false
			}
			d.generation = generation
			d.active = true
		}
	case "status", "end":
		if generation != d.generation {
			return desktopDrainResponse{}, false
		}
		if operation == "end" {
			d.active = false
		}
	}
	return desktopDrainResponse{
		OK: true, Generation: d.generation, Drained: d.inFlight == 0,
		Accepting: !d.active, InFlight: d.inFlight,
	}, true
}

func (h *Handler) handleDesktopDrain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Operation  string `json:"operation"`
		Generation uint64 `json:"generation"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = errors.New("expected exactly one JSON object")
		}
	}
	if err != nil || req.Generation == 0 ||
		(req.Operation != "begin" && req.Operation != "status" && req.Operation != "end") {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "expected operation begin, status or end and a positive uint64 generation in a JSON object of at most 1024 bytes")
		return
	}
	state, ok := h.drain.update(req.Operation, req.Generation)
	if !ok {
		writeOpenAIError(w, http.StatusConflict, "drain_generation_conflict", "generation does not match the active drain or has already ended")
		return
	}
	writeJSON(w, http.StatusOK, state)
}
