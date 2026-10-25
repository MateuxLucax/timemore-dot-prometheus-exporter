// Package api serves the upload contract (testdata/contract): POST and GET /api/v1/brews and
// GET /healthz. Routing, statuses, error bodies and the access log match caparao/stub; the
// difference is that brews are upserted into SQLite and 2xx is sent only after the commit.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/brew"
	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/store"
)

// MaxBodyBytes is the request body limit.
const MaxBodyBytes = 4 << 20 // 4 MiB

// Server is the HTTP API. Create it with NewServer.
type Server struct {
	tokenHash [sha256.Size]byte
	logger    *log.Logger
	store     *store.Store
}

// NewServer returns a handler that requires "Authorization: Bearer <token>" on the /api routes.
func NewServer(token string, st *store.Store, logger *log.Logger) *Server {
	return &Server{tokenHash: sha256.Sum256([]byte(token)), logger: logger, store: st}
}

type ctxKey struct{}

// reqInfo lets handlers report the brew id to the access log.
type reqInfo struct{ id string }

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// ServeHTTP routes, logs one line per request (never the Authorization header).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	info := &reqInfo{}
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, info))

	switch r.URL.Path {
	case "/healthz":
		s.healthz(sw, r)
	case "/api/v1/brews":
		s.brewsRoute(sw, r)
	default:
		writeError(sw, http.StatusNotFound, "not found")
	}

	id := info.id
	if id == "" {
		id = "-"
	}
	s.logger.Printf("%s %q %d id=%s", r.Method, r.URL.Path, sw.status, id)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok")
}

func (s *Server) brewsRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	if r.Method == http.MethodPost {
		s.postBrew(w, r)
	} else {
		s.listBrews(w, r)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return false
	}
	got := sha256.Sum256([]byte(h[len(prefix):]))
	return subtle.ConstantTimeCompare(got[:], s.tokenHash[:]) == 1
}

func (s *Server) postBrew(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "body larger than 4 MiB")
		} else {
			writeError(w, http.StatusBadRequest, "could not read body")
		}
		return
	}
	b, err := brew.Parse(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if info, ok := r.Context().Value(ctxKey{}).(*reqInfo); ok {
		info.id = b.ID // validated UUID, safe to log
	}

	existed, err := s.store.Upsert(r.Context(), b)
	if err != nil {
		s.logger.Printf("ERROR store brew %s: %v", b.ID, err)
		writeError(w, http.StatusInternalServerError, "could not store brew")
		return
	}

	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]string{"id": b.ID})
}

func (s *Server) listBrews(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.List(r.Context())
	if err != nil {
		s.logger.Printf("ERROR list brews: %v", err)
		writeError(w, http.StatusInternalServerError, "could not list brews")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
