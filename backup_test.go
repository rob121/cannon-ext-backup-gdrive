package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rob121/cannon/extension"
)

func TestCreateBackupArchiveIncludesSitesAndDataRoot(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(dataRoot, "site-a"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "site-a", "hello.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataRoot, "sockets"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "sockets", "x.sock"), []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}
	sitesJSON := filepath.Join(root, "sites.json")
	if err := os.WriteFile(sitesJSON, []byte(`{"data_root":"./data"}`), 0644); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(root, "out.tar.gz")
	if err := createBackupArchive(archive, dataRoot, sitesJSON); err != nil {
		t.Fatal(err)
	}

	names := tarNames(t, archive)
	if !names["sites.json"] {
		t.Fatalf("missing sites.json in archive: %v", keys(names))
	}
	if !names["data_root/site-a/hello.txt"] {
		t.Fatalf("missing data file in archive: %v", keys(names))
	}
	for name := range names {
		if strings.Contains(name, "sockets") {
			t.Fatalf("sockets should be excluded, found %q", name)
		}
	}
}

func TestParseCron(t *testing.T) {
	if _, err := parseCron("0 2 * * *"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseCron("@daily"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseCron("not a cron"); err == nil {
		t.Fatal("expected error")
	}
}

func TestOAuthStateConsumeOnce(t *testing.T) {
	app := &backupApp{}
	state, err := app.newOAuthState("https://admin.example/ext/abc/oauth/callback")
	if err != nil {
		t.Fatal(err)
	}
	redirect, ok := app.consumeOAuthState(state)
	if !ok || redirect != "https://admin.example/ext/abc/oauth/callback" {
		t.Fatalf("consume = %q ok=%v", redirect, ok)
	}
	if _, ok := app.consumeOAuthState(state); ok {
		t.Fatal("state should be single-use")
	}
}

func TestPublicBaseFromRequest(t *testing.T) {
	base, err := publicBaseFromRequest(extension.WireRequest{
		Header: map[string][]string{
			"X-Forwarded-Proto": {"https"},
			"X-Forwarded-Host":  {"cms.example.com"},
			"Host":              {"127.0.0.1:8001"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if base != "https://cms.example.com" {
		t.Fatalf("base = %q", base)
	}

	base, err = publicBaseFromRequest(extension.WireRequest{
		Header: map[string][]string{
			"Host": {"localhost:8001"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if base != "http://localhost:8001" {
		t.Fatalf("base = %q", base)
	}
}

func TestOAuthCallbackPathStable(t *testing.T) {
	if oauthCallbackPath != "/admin/extension-apps/cannon-ext-backup-gdrive/oauth/callback" {
		t.Fatalf("callback path = %q", oauthCallbackPath)
	}
	app := &backupApp{}
	got, err := app.oauthRedirectURL(extension.WireRequest{
		Header: map[string][]string{
			"X-Forwarded-Proto": {"https"},
			"Host":              {"admin.example.com"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://admin.example.com/admin/extension-apps/cannon-ext-backup-gdrive/oauth/callback"
	if got != want {
		t.Fatalf("redirect = %q, want %q", got, want)
	}
}

func tarNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = true
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
