package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// Optimistic concurrency for a machine's definition: the settings form and the XML editor
// get a version (a hash of the next-boot XML, also sent as ETag) and send it back in
// If-Match when saving. If the definition changed in between (the other editor, another
// user, virsh), the save is refused with 409 instead of silently undoing that change.
// A save without If-Match is not checked (scripts, older clients).

// definitionVersion is the version of a machine's next-boot definition.
func (s *Server) definitionVersion(uuid string) (string, error) {
	x, err := s.backend.DomainXML(uuid, true)
	if err != nil {
		return "", err
	}
	return xmlVersion(x), nil
}

func xmlVersion(x string) string {
	sum := sha256.Sum256([]byte(x))
	return hex.EncodeToString(sum[:8])
}

// ifMatch is the version a request was based on ("" when it sent none).
func ifMatch(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	v = strings.TrimPrefix(v, "W/")
	return strings.Trim(v, `"`)
}

// checkVersion answers 409 and returns false when the request was based on an older
// version of the machine's definition.
func (s *Server) checkVersion(w http.ResponseWriter, r *http.Request, uuid string) bool {
	want := ifMatch(r)
	if want == "" || want == "*" {
		return true
	}
	have, err := s.definitionVersion(uuid)
	if err != nil {
		s.writeError(w, err)
		return false
	}
	if have != want {
		s.writeJSON(w, http.StatusConflict, map[string]string{
			"error": "The machine's configuration was changed elsewhere (another tab, another user or virsh) since you opened it. Reload to see the changes, then make yours again.",
			"code":  "changed",
		})
		return false
	}
	return true
}
