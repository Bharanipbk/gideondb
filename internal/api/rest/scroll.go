package rest

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) scroll(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_limit", Message: "limit must be between 1 and 200"})
			return
		}
		limit = value
	}
	afterNamespace, afterID := "", ""
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		parts := strings.SplitN(string(decoded), "\x00", 2)
		if err != nil || len(parts) != 2 || parts[1] == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_cursor", Message: "cursor is invalid"})
			return
		}
		afterNamespace, afterID = parts[0], parts[1]
	}
	records, more, err := s.engine.Scroll(r.PathValue("name"), r.URL.Query().Get("namespace"), afterNamespace, afterID, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	includeVector := r.URL.Query().Get("include_vector") == "true"
	if !includeVector {
		for index := range records {
			records[index].Vector = nil
		}
	}
	next := ""
	if more && len(records) != 0 {
		last := records[len(records)-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(last.Namespace + "\x00" + last.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records, "next_cursor": next, "vectors_included": includeVector})
}
