package rest

import (
	"encoding/base64"
	"net/http"
	"strconv"
)

func parsePageQuery(w http.ResponseWriter, r *http.Request) (int, string, bool) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_limit", Message: "limit must be between 1 and 200"})
			return 0, "", false
		}
		limit = value
	}
	cursor := ""
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || len(decoded) == 0 || len(decoded) > 512 {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_cursor", Message: "cursor is invalid"})
			return 0, "", false
		}
		cursor = string(decoded)
	}
	return limit, cursor, true
}

func pageCursor(value string) string {
	if value == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}
