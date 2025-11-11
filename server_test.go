package gooctopi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServerServesIndexWithOctoPi(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(NewServer())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("failed to GET index: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	if got := string(body); got != "OctoPi" {
		t.Fatalf("unexpected body: got %q, want %q", got, "OctoPi")
	}
}
