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
// cmd/bridge's runHealthcheck.
func runHealthcheck(addr string) int {
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + host + "/healthz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "host: healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "host: healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
