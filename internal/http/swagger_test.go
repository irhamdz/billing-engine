package httpapi_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestDocs_Routes is a smoke test for the embedded Swagger UI. It verifies that
// the embed directive picked up the spec, the index page, and a vendored asset.
func TestDocs_Routes(t *testing.T) {
	srv := newServer(t)

	cases := []struct {
		name        string
		path        string
		wantCT      string
		wantBodyHas string
	}{
		{
			name:        "index html",
			path:        "/docs",
			wantCT:      "text/html",
			wantBodyHas: "swagger-ui",
		},
		{
			name:        "openapi spec",
			path:        "/docs/openapi.yaml",
			wantCT:      "application/yaml",
			wantBodyHas: "openapi: 3.",
		},
		{
			name:        "ui bundle js",
			path:        "/docs/swagger-ui-bundle.js",
			wantCT:      "application/javascript",
			wantBodyHas: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := doReq(t, srv, "GET", tc.path, "", nil)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, tc.wantCT) {
				t.Fatalf("Content-Type=%q want prefix %q", got, tc.wantCT)
			}
			if tc.wantBodyHas != "" {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				if !strings.Contains(string(body), tc.wantBodyHas) {
					t.Fatalf("body missing %q; first 200 bytes: %q", tc.wantBodyHas, truncate(body, 200))
				}
			}
		})
	}
}

// TestDocs_UnknownAsset verifies the asset handler 404s on a missing file
// rather than leaking a 500 or path-traversing into the package directory.
func TestDocs_UnknownAsset(t *testing.T) {
	srv := newServer(t)
	resp := doReq(t, srv, "GET", "/docs/nope.js", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want 404", resp.StatusCode)
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
