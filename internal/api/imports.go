package api

import (
	"context"
	"io"
	"net/http"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// OVAImporter turns an OVA appliance into a machine (implemented by *virt.Client).
type OVAImporter interface {
	ImportOVA(ctx context.Context, pool, name string, r io.Reader, report func(string)) (virt.CreateResult, error)
}

func (s *Server) importRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/storage/pools/{pool}/import-ova", s.importOVA)
}

// importOVA reads an OVA from the request body (?name= overrides the appliance's name),
// converts its disks into the pool and creates the machine, stopped.
func (s *Server) importOVA(w http.ResponseWriter, r *http.Request) {
	imp, ok := s.backend.(OVAImporter)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "importing appliances is not available"})
		return
	}
	res, err := imp.ImportOVA(r.Context(), r.PathValue("pool"), r.URL.Query().Get("name"), r.Body, func(string) {})
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusCreated, res)
}
