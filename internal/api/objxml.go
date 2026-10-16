package api

import (
	"io"
	"net/http"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// ObjectXMLer reads and redefines networks and storage pools as raw XML (implemented by
// *virt.Client). P4: every libvirt setting is reachable, not only what the forms offer.
type ObjectXMLer interface {
	NetworkXML(name string) (string, error)
	DefineNetworkXML(name, doc string) (virt.ObjectSaveResult, error)
	PoolXML(name string) (string, error)
	DefinePoolXML(name, doc string) (virt.ObjectSaveResult, error)
}

func (s *Server) objectXMLRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/networks/{name}/xml", s.getNetworkXML)
	mux.HandleFunc("PUT /api/v1/networks/{name}/xml", s.putNetworkXML)
	mux.HandleFunc("GET /api/v1/storage/pools/{pool}/xml", s.getPoolXML)
	mux.HandleFunc("PUT /api/v1/storage/pools/{pool}/xml", s.putPoolXML)
}

func (s *Server) objectXMLer(w http.ResponseWriter) (ObjectXMLer, bool) {
	o, ok := s.backend.(ObjectXMLer)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "XML editing is not available"})
	}
	return o, ok
}

func (s *Server) writeXML(w http.ResponseWriter, doc string, err error) {
	if err != nil {
		s.writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, doc)
}

func (s *Server) readXML(w http.ResponseWriter, r *http.Request) (string, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the XML: " + err.Error()})
		return "", false
	}
	return string(body), true
}

func (s *Server) getNetworkXML(w http.ResponseWriter, r *http.Request) {
	if o, ok := s.objectXMLer(w); ok {
		doc, err := o.NetworkXML(r.PathValue("name"))
		s.writeXML(w, doc, err)
	}
}

func (s *Server) putNetworkXML(w http.ResponseWriter, r *http.Request) {
	o, ok := s.objectXMLer(w)
	if !ok {
		return
	}
	doc, ok := s.readXML(w, r)
	if !ok {
		return
	}
	res, err := o.DefineNetworkXML(r.PathValue("name"), doc)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusOK, res)
}

func (s *Server) getPoolXML(w http.ResponseWriter, r *http.Request) {
	if o, ok := s.objectXMLer(w); ok {
		doc, err := o.PoolXML(r.PathValue("pool"))
		s.writeXML(w, doc, err)
	}
}

func (s *Server) putPoolXML(w http.ResponseWriter, r *http.Request) {
	o, ok := s.objectXMLer(w)
	if !ok {
		return
	}
	doc, ok := s.readXML(w, r)
	if !ok {
		return
	}
	res, err := o.DefinePoolXML(r.PathValue("pool"), doc)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusOK, res)
}
