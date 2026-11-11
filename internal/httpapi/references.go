package httpapi

import "net/http"

func (s *Server) handleReferenceSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, truncated, err := s.chat.ReferenceCandidates(r.Context(), q.Get("conversationId"), q.Get("projectId"), q.Get("agentId"), q.Get("q"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "reference_search_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}
