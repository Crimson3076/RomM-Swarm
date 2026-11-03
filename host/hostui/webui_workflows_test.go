package hostui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestInvitationOptionsAndInvalidLimits(t *testing.T) {
	base, client, swarmPath := setUpSwarmForCatalogueTests(t)
	resp, err := client.PostForm(base+swarmPath+"/invitations", url.Values{
		"max_uses": {"3"}, "expires_days": {"2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := readAll(resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "code-display") {
		t.Fatalf("issue invitation: %d %s", resp.StatusCode, body)
	}
	resp, err = client.Get(base + swarmPath)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = readAll(resp)
	if !strings.Contains(body, "0/3") {
		t.Fatal("invitation use limit was not saved")
	}
	for _, values := range []url.Values{
		{"max_uses": {"0"}}, {"max_uses": {"101"}}, {"expires_days": {"31"}}, {"expires_days": {"bad"}},
	} {
		resp, err := client.PostForm(base+swarmPath+"/invitations", values)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := readAll(resp)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("invalid options accepted: %d %s", resp.StatusCode, body)
		}
	}
}

func TestDeleteSwarmWithUploadedCatalogue(t *testing.T) {
	base, client, swarmPath := setUpSwarmForCatalogueTests(t)
	dat := buildTestCatalogueDATForHostui("Test Game (USA)", []byte("fixture payload"))
	resp := postMultipartCatalogue(t, client, base+swarmPath+"/reference-catalogues", "gb", "gb.dat", dat)
	body, _ := readAll(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d %s", resp.StatusCode, body)
	}
	resp, err := client.PostForm(base+swarmPath+"/delete", url.Values{"confirm_name": {"Test Swarm"}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ = readAll(resp)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("delete Swarm with catalogue: %d %s", resp.StatusCode, body)
	}
	resp, err = client.Get(base + swarmPath)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted Swarm still resolves: %d", resp.StatusCode)
	}
}
