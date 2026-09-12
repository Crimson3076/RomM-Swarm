package adminui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/ingest"
	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func TestLibrarySearchSortAndPagination(t *testing.T) {
	s, backend := loggedInServerWithBackend(t, "https://romm.example", "secret-token")
	backend.conn = &romm.Connection{}
	backend.library = []scan.ROMRecord{
		{ID: "3", Name: "Quest Z", FSName: "z.gb", PlatformSlug: "gb", FSSizeBytes: 30},
		{ID: "2", Name: "Quest A", FSName: "a.gba", PlatformSlug: "gba", FSSizeBytes: 10},
		{ID: "1", Name: "Quest A", FSName: "a.gb", PlatformSlug: "gb", FSSizeBytes: 20},
	}
	before := append([]scan.ROMRecord(nil), backend.library...)
	token := mustSessionToken(t, s)
	for _, tc := range []struct {
		query string
		id    string
		total int
	}{
		{"q=quest&platform=gb&sort=name&limit=1", "1", 2},
		{"q=QUEST&platform=gb&sort=size_desc&limit=1", "3", 2},
		{"q=A.GBA&sort=name", "2", 1},
		{"q=missing", "", 0},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/library/items?"+tc.query, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		var body struct {
			Items []libraryItem
			Total int
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("query %s: %d %s", tc.query, rec.Code, rec.Body.String())
		}
		if body.Total != tc.total || (tc.id != "" && (len(body.Items) == 0 || body.Items[0].ID != tc.id)) {
			t.Fatalf("query %s: %+v", tc.query, body)
		}
		if tc.total == 0 && len(body.Items) != 0 {
			t.Fatal("empty search returned items")
		}
	}
	if !reflect.DeepEqual(before, backend.library) {
		t.Fatal("search mutated the cached library")
	}
	// An offset near MaxInt must not overflow offset+limit or panic.
	params := url.Values{"offset": {strconv.Itoa(int(^uint(0) >> 1))}, "limit": {"100"}}
	req := httptest.NewRequest(http.MethodGet, "/api/library/items?"+params.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "\"items\":[]") {
		t.Fatalf("large offset: %d %s", rec.Code, rec.Body.String())
	}
}

func TestActivityAPIRequiresLoginAndRejectsInvalidIDs(t *testing.T) {
	s, backend := loggedInServerWithBackend(t, "https://romm.example", "never-expose-this")
	id := protocol.NewTransferID()
	if err := backend.journal.Append(id, ingest.Transition{To: protocol.StateReceiving, At: time.Now(), Detail: "Staging file"}); err != nil {
		t.Fatal(err)
	}
	token := mustSessionToken(t, s)
	for _, path := range []string{"/api/activity", "/api/activity?id=" + string(id)} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated activity returned %d", rec.Code)
		}
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		rec = httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Staging file") ||
			strings.Contains(rec.Body.String(), "never-expose-this") {
			t.Fatalf("activity response: %d %s", rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{"/api/activity?id=../../config", "/activity?id=../../config"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid ID: %d", rec.Code)
		}
	}
}

func TestImportJSONResponseAndValidation(t *testing.T) {
	s, backend := loggedInServerWithBackend(t, "https://romm.example", "token")
	s.InboxDir = t.TempDir()
	backend.startImportID = protocol.NewTransferID()
	token := mustSessionToken(t, s)
	for _, tc := range []struct {
		platform string
		status   int
	}{
		{"gb", http.StatusAccepted},
		{"", http.StatusBadRequest},
	} {
		form := url.Values{"source": {"inbox"}, "path": {"game.gb"}, "platform": {tc.platform}}
		req := httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != tc.status || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("import response: %d %s", rec.Code, rec.Body.String())
		}
		if tc.status == http.StatusAccepted && !strings.Contains(rec.Body.String(), string(backend.startImportID)) {
			t.Fatal("accepted response did not identify the transfer")
		}
	}
	if len(backend.startImportCalls) != 1 {
		t.Fatal("invalid submission started an import")
	}
}

func TestClientPagesRenderWithEmptyAndConnectedState(t *testing.T) {
	s, backend := loggedInServerWithBackend(t, "https://romm.example", "token")
	token := mustSessionToken(t, s)
	for _, connected := range []bool{false, true} {
		if connected {
			backend.conn = &romm.Connection{Report: &romm.Report{}}
		}
		for _, path := range []string{"/", "/library", "/inbox", "/activity", "/swarm", "/settings"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "internal error") ||
				!strings.Contains(rec.Body.String(), "</html>") {
				t.Fatalf("%s connected=%v: %d %s", path, connected, rec.Code, rec.Body.String())
			}
		}
	}
}
