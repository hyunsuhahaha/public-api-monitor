package web

import (
	"net/http"
	"strconv"
)

// CheckHistory is how many raw checks /api/v1/checks returns by default: a day at the
// 5-minute interval (the drawer's "최근 확인" shows the newest 40 from the series and
// loads these on 더 보기).
const CheckHistory = 288

// checks: GET /api/v1/checks?target=id[&limit=n], a target's newest raw checks from every
// location, newest first.
func (s *Server) checks(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("target")
	if s.target(id) == nil {
		http.NotFound(w, r)
		return
	}
	limit := CheckHistory
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}
	list, err := s.Store.RecentChecks(r.Context(), id, limit)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, recentChecks(list))
}
