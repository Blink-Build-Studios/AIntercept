// fixture is a tiny HTTPS origin used by Milestone M1. It stands in for the
// real provider so MITM correctness can be proven with synthetic content and no
// real credentials.
//
// It serves:
//   - GET  /                synthetic JSON including a known MARKER string
//   - GET  /stream          an SSE stream of several events (tests streaming)
//   - POST /echo            echoes the request body back (proves request-body
//                           bytes are observable end-to-end)
//
// The MARKER lets the smoke test assert that the exact decrypted bytes the
// fixture produced showed up in the observer's output.
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

// Marker is a recognizable synthetic token. The smoke test greps observer
// output for it to prove decrypted body visibility.
const Marker = "SYNTHETIC-FIXTURE-MARKER-7f3a9c"

func main() {
	addr := envDefault("FIXTURE_LISTEN", "0.0.0.0:8443")
	cert := envDefault("FIXTURE_CERT", "/etc/ssl/fixture/fixture.crt")
	key := envDefault("FIXTURE_KEY", "/etc/ssl/fixture/fixture.key")

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"marker":%q,"path":%q,"proto":%q}`+"\n", Marker, r.URL.Path, r.Proto)
	})

	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "event: chunk\ndata: {\"n\":%d,\"marker\":%q}\n\n", i, Marker)
			flusher.Flush()
			time.Sleep(50 * time.Millisecond)
		}
		fmt.Fprintf(w, "event: done\ndata: [DONE]\n\n")
		flusher.Flush()
	})

	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"echoed_bytes":%d,"marker":%q,"body":%q}`+"\n", len(body), Marker, string(body))
	})

	srv := &http.Server{Addr: addr, Handler: logRequests(mux)}
	log.Printf("fixture: HTTPS origin on %s (marker=%s)", addr, Marker)
	if err := srv.ListenAndServeTLS(cert, key); err != nil {
		log.Fatalf("fixture: %v", err)
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("fixture: %s %s %s", r.Proto, r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
