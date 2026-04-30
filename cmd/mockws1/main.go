// Command mockws1 is a long-running stand-in WS1 tenant. It serves the
// minimal API surface the alice-lock demo exercises (users.search/get,
// devices.search/get, /commands/lock|wipe, /commands/bulk).
//
// Listens on 127.0.0.1:<port> (default 9911). Print a request log to
// stderr; the body is JSON-encoded by test/mockws1.
//
// This binary is NOT shipped to end users; it lives under cmd/ for ease
// of `go run`.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xyzbuilds/ws1-uem-agent/test/mockws1"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9911", "address to listen on")
	flag.Parse()

	srv := mockws1.New()
	logger := log.New(os.Stderr, "mockws1 ", log.LstdFlags|log.Lmsgprefix)

	// Wrap the package's handler with a verbose request log for demos.
	h := loggingMiddleware(srv.HTTPHandler(), logger)
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Bring up the listener.
	go func() {
		logger.Printf("listening on http://%s (Ctrl-C to stop)", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("listen: %v", err)
		}
	}()

	// Wait for SIGINT/SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	logger.Println("shutting down")
	_ = httpSrv.Close()
	fmt.Fprintln(os.Stderr, "mockws1: bye")
}

func loggingMiddleware(h http.Handler, logger *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, code: 200}
		h.ServeHTTP(rw, r)
		logger.Printf("%-6s %-40s -> %d (%s)", r.Method, r.URL.RequestURI(), rw.code, time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}
