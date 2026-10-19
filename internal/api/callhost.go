package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/cluster"
)

// callHost runs an API request on cluster member hostID as id: in-process for this host,
// over the peer protocol for the others.
func (s *Server) callHost(ctx context.Context, id auth.Identity, hostID, method, path string, in, out any) error {
	if hostID == s.cluster.SelfID() {
		var body io.Reader = http.NoBody
		if in != nil {
			raw, err := json.Marshal(in)
			if err != nil {
				return err
			}
			body = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(context.WithValue(ctx, identityKey, id), method, path, body)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		if s.inner == nil {
			return errors.New("the API is not serving yet")
		}
		s.inner.ServeHTTP(rec, req)
		if rec.Code >= 300 {
			var e struct{ Error string }
			_ = json.Unmarshal(rec.Body.Bytes(), &e)
			return &cluster.StatusError{Code: rec.Code, Message: cmpOrStr(e.Error, http.StatusText(rec.Code))}
		}
		if out != nil && rec.Body.Len() > 0 {
			return json.Unmarshal(rec.Body.Bytes(), out)
		}
		return nil
	}
	n, ok := s.cluster.Member(hostID)
	if !ok {
		return fmt.Errorf("%s is no longer a cluster member", hostID)
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.peer(n).As(id.Name, id.Role.String(), id.Token).Call(c, method, path, in, out)
}
