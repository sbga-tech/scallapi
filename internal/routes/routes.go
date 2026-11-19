// Package routes implements endpoint-prefix routing independently of API protocols.
package routes

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
)

// Config describes a named endpoint and its credential selectors.
type Config struct {
	Name        string   `yaml:"name" json:"name"`
	Path        string   `yaml:"path" json:"path"`
	RewriteTo   string   `yaml:"rewrite-to" json:"rewrite-to"`
	Providers   []string `yaml:"providers" json:"providers"`
	Credentials []string `yaml:"credentials" json:"credentials"`
}

// Normalize validates routes and normalizes provider names in place.
func Normalize(configs []Config) error {
	names, paths := make(map[string]bool, len(configs)), make(map[string]bool, len(configs))
	for i := range configs {
		c := &configs[i]
		if strings.TrimSpace(c.Name) == "" || names[c.Name] {
			return fmt.Errorf("routes[%d]: name must be non-empty and unique", i)
		}
		names[c.Name] = true
		if !validPrefix(c.Path) || paths[c.Path] {
			return fmt.Errorf("routes[%d]: path must start with /, have no trailing /, and be unique", i)
		}
		paths[c.Path] = true
		if c.RewriteTo != "" && !validPrefix(c.RewriteTo) {
			return fmt.Errorf("routes[%d]: rewrite-to must start with / and have no trailing / (use empty for root)", i)
		}
		for j := range c.Providers {
			c.Providers[j] = strings.ToLower(strings.TrimSpace(c.Providers[j]))
		}
	}
	return nil
}

func validPrefix(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.HasSuffix(path, "/")
}

// Route is immutable after compilation. Selectors are intentionally not exposed.
type Route struct {
	name, path, rewriteTo  string
	providers, credentials map[string]struct{}
}

func (r *Route) Name() string {
	if r == nil {
		return ""
	}
	return r.name
}

// RestrictsProviders reports whether provider intersection is needed.
func (r *Route) RestrictsProviders() bool { return r != nil && len(r.providers) != 0 }

// AllowsProvider accepts any matching canonical or original provider name.
func (r *Route) AllowsProvider(providers ...string) bool {
	if r == nil {
		return false
	}
	if len(r.providers) == 0 {
		return true
	}
	for _, provider := range providers {
		if _, ok := r.providers[strings.ToLower(strings.TrimSpace(provider))]; ok {
			return true
		}
	}
	return false
}

// AllowsCredential accepts any file base name or config API-key group name.
func (r *Route) AllowsCredential(names ...string) bool {
	if r == nil {
		return false
	}
	if len(r.credentials) == 0 {
		return true
	}
	for _, name := range names {
		if name != "" {
			if _, ok := r.credentials[name]; ok {
				return true
			}
		}
	}
	return false
}

// Snapshot is an immutable, longest-prefix-first routing table.
type Snapshot struct {
	ordered []*Route
	named   map[string]*Route
}

// Compile copies the configuration so later config edits cannot mutate a snapshot.
// Call Normalize at the configuration boundary before compiling.
func Compile(configs []Config) *Snapshot {
	s := &Snapshot{named: make(map[string]*Route, len(configs))}
	for _, c := range configs {
		r := &Route{name: c.Name, path: c.Path, rewriteTo: c.RewriteTo, providers: make(map[string]struct{}, len(c.Providers)), credentials: make(map[string]struct{}, len(c.Credentials))}
		for _, p := range c.Providers {
			r.providers[strings.ToLower(strings.TrimSpace(p))] = struct{}{}
		}
		for _, name := range c.Credentials {
			r.credentials[name] = struct{}{}
		}
		s.ordered = append(s.ordered, r)
		s.named[r.name] = r
	}
	sort.SliceStable(s.ordered, func(i, j int) bool { return len(s.ordered[i].path) > len(s.ordered[j].path) })
	return s
}

func (s *Snapshot) Named(name string) *Route {
	if s == nil {
		return nil
	}
	return s.named[name]
}
func (s *Snapshot) Match(path string) *Route {
	if s != nil {
		for _, r := range s.ordered {
			if path == r.path || (len(path) > len(r.path) && strings.HasPrefix(path, r.path) && path[len(r.path)] == '/') {
				return r
			}
		}
	}
	return nil
}

// Store atomically publishes routing snapshots. Its zero value has no routes.
type Store struct{ current atomic.Pointer[Snapshot] }

func (s *Store) Load() *Snapshot {
	if s == nil {
		return nil
	}
	return s.current.Load()
}
func (s *Store) Update(configs []Config) { s.current.Store(Compile(configs)) }

type contextKey struct{}

// FromContext returns the matched route, including when no rewrite was needed.
func FromContext(ctx context.Context) *Route {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(contextKey{}).(*Route)
	return r
}

// Wrap rewrites only matched requests before the protocol router sees them.
// It leaves the response writer untouched, including its websocket interfaces.
func (s *Store) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r := s.Load().Match(req.URL.Path)
		if r == nil {
			next.ServeHTTP(w, req)
			return
		}
		req = req.WithContext(context.WithValue(req.Context(), contextKey{}, r))
		rewritten := *req.URL
		rewritten.Path = r.rewriteTo + strings.TrimPrefix(req.URL.Path, r.path)
		if rewritten.Path == "" {
			rewritten.Path = "/"
		}
		if req.URL.RawPath != "" {
			// Retain escaped suffixes (including encoded slashes) without assuming the
			// configured prefix uses the same percent-encoding as the request.
			escaped := req.URL.EscapedPath()
			decodedBytes := 0
			cut := 0
			for cut < len(escaped) && decodedBytes < len(r.path) {
				step := 1
				if escaped[cut] == '%' && cut+2 < len(escaped) {
					step = 3
				}
				decodedBytes++
				cut += step
			}
			rewritten.RawPath = (&url.URL{Path: r.rewriteTo}).EscapedPath() + escaped[cut:]
			if rewritten.RawPath == "" {
				rewritten.RawPath = "/"
			}
		}
		req.URL = &rewritten
		next.ServeHTTP(w, req)
	})
}
