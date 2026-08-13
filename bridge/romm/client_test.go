package romm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestGetUnauthenticatedDoesNotMutateOrSendToken(t *testing.T) {
	const token = "never-send-this-token"
	var authenticated int64
	var unauthenticated int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authenticated":
			if r.Header.Get("X-Api-Key") != token {
				t.Errorf("authenticated request credential = %q, want configured token", r.Header.Get("X-Api-Key"))
			}
			atomic.AddInt64(&authenticated, 1)
		case "/openapi.json":
			if got := r.Header.Get("X-Api-Key"); got != "" {
				t.Errorf("unauthenticated request sent credential %q", got)
			}
			atomic.AddInt64(&unauthenticated, 1)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	scheme, ok := LookupAuthScheme("x-api-key")
	if !ok {
		t.Fatal("x-api-key auth scheme is missing")
	}
	c := NewClient(srv.URL, token, scheme)

	const requests = 50
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if status, _, err := c.Get(context.Background(), "/authenticated", nil); err != nil || status != http.StatusOK {
				t.Errorf("authenticated GET: status=%d err=%v", status, err)
			}
		}()
		go func() {
			defer wg.Done()
			if status, _, err := c.GetUnauthenticated(context.Background(), "/openapi.json"); err != nil || status != http.StatusOK {
				t.Errorf("unauthenticated GET: status=%d err=%v", status, err)
			}
		}()
	}
	wg.Wait()

	if c.Token != token {
		t.Fatalf("client token changed to %q", c.Token)
	}
	if got := atomic.LoadInt64(&authenticated); got != requests {
		t.Errorf("authenticated requests = %d, want %d", got, requests)
	}
	if got := atomic.LoadInt64(&unauthenticated); got != requests {
		t.Errorf("unauthenticated requests = %d, want %d", got, requests)
	}
}

func TestGetRefusesCrossOriginRedirectBeforeCredentialCanLeak(t *testing.T) {
	const token = "redirect-secret"
	var destinationRequests int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&destinationRequests, 1)
		if got := r.Header.Get("X-Api-Key"); got != "" {
			t.Errorf("redirect destination received credential %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/stolen", http.StatusFound)
	}))
	defer source.Close()

	scheme, _ := LookupAuthScheme("x-api-key")
	c := NewClient(source.URL, token, scheme)
	status, _, err := c.Get(context.Background(), "/api/roms", nil)
	if err == nil {
		t.Fatal("cross-origin redirect was followed")
	}
	if status != 0 {
		t.Errorf("status = %d, want 0 for a refused redirect", status)
	}
	if !strings.Contains(err.Error(), "different origin") {
		t.Errorf("error = %q, want a different-origin explanation", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("redirect error contains the credential")
	}
	if got := atomic.LoadInt64(&destinationRequests); got != 0 {
		t.Fatalf("redirect destination received %d requests, want 0", got)
	}
}

func TestGetAllowsSameOriginRedirect(t *testing.T) {
	const token = "same-origin-token"
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/finish", http.StatusFound)
	})
	mux.HandleFunc("/finish", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("redirected request credential = %q", got)
		}
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	scheme, _ := LookupAuthScheme("bearer")
	c := NewClient(srv.URL, token, scheme)
	status, body, err := c.Get(context.Background(), "/start", nil)
	if err != nil {
		t.Fatalf("same-origin redirect: %v", err)
	}
	if status != http.StatusOK || string(body) != "ok" {
		t.Fatalf("status=%d body=%q, want 200 and ok", status, body)
	}
}

func TestGetRejectsOversizedResponse(t *testing.T) {
	const maxBody = 8 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, strings.Repeat("x", maxBody+1))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", AuthScheme{})
	status, body, err := c.GetUnauthenticated(context.Background(), "/openapi.json")
	if err == nil {
		t.Fatal("oversized response was accepted")
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if body != nil {
		t.Errorf("body length = %d, want no partial body", len(body))
	}
	if !strings.Contains(err.Error(), "response exceeds") {
		t.Errorf("error = %q, want size explanation", err)
	}
}
