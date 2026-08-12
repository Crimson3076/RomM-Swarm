package hostui_test

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func buildTestCatalogueDATForHostui(gameName string, payload []byte) []byte {
	d := protocol.DigestBytes(payload)
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\"?>\n<datafile>\n")
	b.WriteString("  <header>\n    <name>Nintendo - Game Boy</name>\n    <version>1</version>\n  </header>\n")
	fmt.Fprintf(&b, "  <game name=%q>\n    <rom name=\"%s.gb\" size=\"%d\" crc=\"%s\" md5=\"%s\" sha1=\"%s\"/>\n  </game>\n",
		gameName, gameName, d.Size, strings.ToUpper(d.CRC32), strings.ToUpper(d.MD5), strings.ToUpper(d.SHA1))
	b.WriteString("</datafile>\n")
	return []byte(b.String())
}

// postMultipartCatalogue uploads content as dat_file, with platform as a
// regular form field — mirroring what a browser's <input type="file"> form
// on the Swarm page actually sends, since url.Values/PostForm can't
// express a multipart body.
func postMultipartCatalogue(t *testing.T, client *http.Client, url, platform, filename string, content []byte) *http.Response {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("platform", platform); err != nil {
		t.Fatalf("writing platform field: %v", err)
	}
	part, err := w.CreateFormFile("dat_file", filename)
	if err != nil {
		t.Fatalf("creating form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("writing file content: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// setUpSwarmForCatalogueTests walks setup -> login -> create Swarm, the
// same sequence every other Swarm-page test uses, returning the client
// (already authenticated) and the Swarm's page path.
func setUpSwarmForCatalogueTests(t *testing.T) (srvURL string, client *http.Client, swarmPath string) {
	t.Helper()
	srv, _ := newTestServer(t)
	client = newClient(t)

	form := url.Values{
		"username": {"owner"}, "display_name": {"The Owner"},
		"password": {"a-long-enough-password"}, "password_confirm": {"a-long-enough-password"},
	}
	if _, err := client.PostForm(srv.URL+"/setup", form); err != nil {
		t.Fatalf("POST /setup: %v", err)
	}
	resp, err := client.PostForm(srv.URL+"/swarms", url.Values{"name": {"Test Swarm"}})
	if err != nil {
		t.Fatalf("POST /swarms: %v", err)
	}
	return srv.URL, client, resp.Header.Get("Location")
}

func TestPhase2_UploadReferenceCatalogueRendersInTheSwarmPage(t *testing.T) {
	srvURL, client, swarmPath := setUpSwarmForCatalogueTests(t)

	dat := buildTestCatalogueDATForHostui("Test Game (USA)", []byte("a fake payload"))
	resp := postMultipartCatalogue(t, client, srvURL+swarmPath+"/reference-catalogues", "gb", "gb.dat", dat)
	body, _ := readAll(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST reference-catalogues: status %d, body %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "gb.dat") || !strings.Contains(body, "<td>gb</td>") {
		t.Fatalf("Swarm page did not show the uploaded catalogue: %s", body)
	}
}

func TestPhase2_UploadReferenceCatalogueRejectsABadFile(t *testing.T) {
	srvURL, client, swarmPath := setUpSwarmForCatalogueTests(t)

	resp := postMultipartCatalogue(t, client, srvURL+swarmPath+"/reference-catalogues", "gb", "bad.dat", []byte("not a dat file"))
	body, _ := readAll(resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST reference-catalogues with a bad file: status %d, body %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "could not upload the catalogue") {
		t.Fatalf("Swarm page did not show the upload error: %s", body)
	}
}

func TestPhase2_DeleteReferenceCatalogueRemovesItFromTheSwarmPage(t *testing.T) {
	srvURL, client, swarmPath := setUpSwarmForCatalogueTests(t)

	dat := buildTestCatalogueDATForHostui("Test Game (USA)", []byte("a fake payload"))
	uploadResp := postMultipartCatalogue(t, client, srvURL+swarmPath+"/reference-catalogues", "gb", "gb.dat", dat)
	if uploadResp.StatusCode != http.StatusOK {
		body, _ := readAll(uploadResp)
		t.Fatalf("upload: status %d, body %s", uploadResp.StatusCode, body)
	}

	delResp, err := client.PostForm(srvURL+swarmPath+"/reference-catalogues/gb/delete", nil)
	if err != nil {
		t.Fatalf("POST delete catalogue: %v", err)
	}
	if delResp.StatusCode != http.StatusSeeOther {
		body, _ := readAll(delResp)
		t.Fatalf("POST delete catalogue: status %d, body %s", delResp.StatusCode, body)
	}

	page, err := client.Get(srvURL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s: %v", swarmPath, err)
	}
	body, _ := readAll(page)
	if strings.Contains(body, "gb.dat") {
		t.Fatalf("Swarm page still lists the deleted catalogue: %s", body)
	}
	if !strings.Contains(body, "No catalogues loaded yet") {
		t.Fatalf("Swarm page did not fall back to the empty-catalogues message: %s", body)
	}
}
