//go:build unix

package adminui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A path-shaped id must be refused before the journal touches the
// filesystem. The journal swallows read errors, so the rendered page cannot
// show the difference; instead a FIFO sits where "../fifo" would resolve, and
// any attempt to read it blocks.
func TestActivityDoesNotReadTheJournalForAMalformedTransferID(t *testing.T) {
	s, backend := loggedInServerWithBackend(t, "https://x", "t")
	token := mustSessionToken(t, s)

	fifo := filepath.Join(filepath.Dir(backend.journal.Dir), "fifo.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/activity?id=../fifo", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		s.ServeHTTP(rec, r)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// Release the blocked reader so the goroutine and temp dir clean up.
		if f, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			f.Close()
		}
		<-done
		t.Fatal("/activity read the journal path for a malformed transfer id")
	}
}
