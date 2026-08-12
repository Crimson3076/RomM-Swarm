package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// runHealthcheck is what `host -healthcheck` does for Docker's HEALTHCHECK:
// the distroless runtime image has no shell, so the binary checks itself
// rather than something shelling out to curl/wget. Same shape as
// cmd/bridge's runHealthcheck, extended to check both of this daemon's
// servers — the API and the web UI must both be up for the container to
// count as healthy.
func runHealthcheck(addrs ...string) int {
	client := &http.Client{Timeout: 5 * time.Second}
	for _, addr := range addrs {
		host := addr
		if strings.HasPrefix(host, ":") {
			host = "127.0.0.1" + host
		}
		resp, err := client.Get("http://" + host + "/healthz")
		if err != nil {
			fmt.Fprintf(os.Stderr, "host: healthcheck (%s): %v\n", addr, err)
			return 1
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "host: healthcheck (%s): status %d\n", addr, resp.StatusCode)
			return 1
		}
	}
	return 0
}
