// Package diagnostics exposes optional local-only runtime diagnostics.
package diagnostics

import (
	"net/http"
	"net/http/pprof"
	"strings"
)

// NewPprofHandler returns the standard Go pprof endpoints under /debug/pprof/.
// The caller should bind it to a loopback-only address in development.
func NewPprofHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	for _, name := range []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"} {
		mux.Handle("/debug/pprof/"+name, pprof.Handler(name))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/debug/pprof/") {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
