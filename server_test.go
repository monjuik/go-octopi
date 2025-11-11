package gooctopi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerIndexRendering(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(NewServer())
	t.Cleanup(ts.Close)

	tests := []struct {
		name   string
		assert func(t *testing.T, body string)
	}{
		{
			name: "contains OctoPi",
			assert: func(t *testing.T, body string) {
				if !strings.Contains(body, "OctoPi") {
					t.Fatalf("body missing OctoPi: %q", body)
				}
			},
		},
		{
			name: "contains embedded logo",
			assert: func(t *testing.T, body string) {
				const prefix = `src="data:image/png;base64,`
				if !strings.Contains(body, prefix) {
					t.Fatalf("response missing embedded logo (looking for %q)", prefix)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp, err := http.Get(ts.URL + "/")
			if err != nil {
				t.Fatalf("failed to GET index: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("unexpected status: got %d, want %d", resp.StatusCode, http.StatusOK)
			}

			bodyBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("failed to read body: %v", err)
			}

			tt.assert(t, string(bodyBytes))
		})
	}
}

func TestWalletPageRendering(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(NewServer())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/wallet/demo")
	if err != nil {
		t.Fatalf("failed to GET wallet page: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	if !strings.Contains(string(body), "Wallet summary") {
		t.Fatalf("wallet page missing summary section: %q", body)
	}
}
