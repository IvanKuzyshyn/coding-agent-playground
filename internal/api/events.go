package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"terrarium/internal/session"
	"terrarium/internal/store"
)

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownedID(w, r)
	if !ok {
		return
	}
	after := queryInt(r, "after", 0, 0)
	kind := r.URL.Query().Get("kind")
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		events, err := s.Store.Events(r.Context(), id, after, kind, int(queryInt(r, "limit", 500, 1000)))
		if err != nil {
			s.internal(w, err)
			return
		}
		next := after
		if len(events) > 0 {
			next = events[len(events)-1].ID
		}
		if events == nil {
			events = []store.Event{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events, "next_after": next})
		return
	}

	if v, err := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64); err == nil && v > after {
		after = v
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	lastWrite := time.Now()
	ended := false
	for {
		events, err := s.Store.Events(ctx, id, after, kind, 500)
		if err != nil {
			if ctx.Err() == nil {
				s.Log.Error("sse events", "err", err)
			}
			return
		}
		for _, e := range events {
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Kind, b)
			after = e.ID
		}
		if len(events) > 0 {
			flusher.Flush()
			lastWrite = time.Now()
			continue // drain backlog before sleeping
		}
		if ended {
			return // the read after observing "stopped" found nothing more
		}
		ss, err := s.Store.GetSession(ctx, id)
		if err == nil && ss.Status == session.StatusStopped {
			// The terminal event may have committed after the read above; read once more.
			ended = true
			continue
		}
		if time.Since(lastWrite) > 15*time.Second {
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
			lastWrite = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.EventPoll):
		}
	}
}
