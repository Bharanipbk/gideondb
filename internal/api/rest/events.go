package rest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type httpEvent struct {
	Sequence   uint64    `json:"sequence"`
	Timestamp  time.Time `json:"timestamp"`
	Level      string    `json:"level"`
	Event      string    `json:"event"`
	Method     string    `json:"method"`
	Route      string    `json:"route"`
	Status     int       `json:"status"`
	DurationMS float64   `json:"duration_ms"`
	TraceID    string    `json:"trace_id"`
	SpanID     string    `json:"span_id"`
}

type eventLog struct {
	mu         sync.RWMutex
	capacity   int
	next       uint64
	events     []httpEvent
	path       string
	diskRows   int
	persistErr error
}

func newEventLog(capacity int) *eventLog { return &eventLog{capacity: capacity} }

func newPersistentEventLog(path string, capacity int) *eventLog {
	l := &eventLog{capacity: capacity, path: path}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return l
	}
	if err != nil {
		l.persistErr = err
		return l
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		var event httpEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil || !validHTTPEvent(event) {
			continue
		}
		l.diskRows++
		if event.Sequence > l.next {
			l.next = event.Sequence
		}
		if len(l.events) == capacity {
			copy(l.events, l.events[1:])
			l.events[len(l.events)-1] = event
		} else {
			l.events = append(l.events, event)
		}
	}
	if err := scanner.Err(); err != nil {
		l.persistErr = err
	}
	return l
}

func (l *eventLog) add(event httpEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	event.Sequence = l.next
	event.Event = "http.server.request"
	event.Level = "info"
	if event.Status >= 500 {
		event.Level = "error"
	} else if event.Status >= 400 {
		event.Level = "warn"
	}
	if len(l.events) == l.capacity {
		copy(l.events, l.events[1:])
		l.events[len(l.events)-1] = event
	} else {
		l.events = append(l.events, event)
	}
	l.persistLocked(event)
}

func (l *eventLog) persistLocked(event httpEvent) {
	if l.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0700); err != nil {
		l.persistErr = err
		return
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		l.persistErr = err
		return
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err == nil {
		_, err = file.Write(encoded)
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		l.persistErr = err
		return
	}
	l.diskRows++
	l.persistErr = nil
	if l.diskRows >= l.capacity*2 {
		l.compactLocked()
	}
}

func (l *eventLog) compactLocked() {
	temporary := l.path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err == nil {
		writer := bufio.NewWriter(file)
		for _, event := range l.events {
			var encoded []byte
			encoded, err = json.Marshal(event)
			if err != nil {
				break
			}
			if _, err = writer.Write(append(encoded, '\n')); err != nil {
				break
			}
		}
		if err == nil {
			err = writer.Flush()
		}
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(temporary, l.path)
		}
	}
	if err != nil {
		_ = os.Remove(temporary)
		l.persistErr = fmt.Errorf("compact event log: %w", err)
		return
	}
	l.diskRows = len(l.events)
	l.persistErr = nil
}

func (l *eventLog) persistence() (bool, string) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.path == "" {
		return false, ""
	}
	if l.persistErr != nil {
		return false, l.persistErr.Error()
	}
	return true, ""
}

func (l *eventLog) recent(limit int, level string) []httpEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	result := make([]httpEvent, 0, limit)
	for index := len(l.events) - 1; index >= 0 && len(result) < limit; index-- {
		if level == "" || l.events[index].Level == level {
			result = append(result, l.events[index])
		}
	}
	return result
}

func (s *Server) recentLogs(w http.ResponseWriter, r *http.Request) {
	limit, level, ok := parseLogQuery(w, r)
	if !ok {
		return
	}
	durable, persistError := s.events.persistence()
	writeJSON(w, http.StatusOK, map[string]any{"events": s.events.recent(limit, level), "retention": s.events.capacity, "durable": durable, "persistence_error": persistError})
}

func parseLogQuery(w http.ResponseWriter, r *http.Request) (int, string, bool) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_limit", Message: "limit must be between 1 and 200"})
			return 0, "", false
		}
		limit = value
	}
	level := r.URL.Query().Get("level")
	if level != "" && level != "info" && level != "warn" && level != "error" {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_level", Message: "level must be info, warn, or error"})
		return 0, "", false
	}
	return limit, level, true
}
