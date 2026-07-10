package server

import (
	"net/http"

	"github.com/gorilla/mux"
)

// RegisterRoutes attaches all HTTP handlers to the router.
// Routes are added here by `esb add handler`.
func RegisterRoutes(router *mux.Router) {
	router.HandleFunc("/health", healthHandler).Methods(http.MethodGet)

	// esb:inject:routes
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
}
