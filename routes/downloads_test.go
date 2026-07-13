package routes

import (
	"strings"
	"testing"

	"gopkg.in/mgo.v2/bson"
)

// DEL-010: attach may only finalize paths inside the tenant+product namespace.
func TestDownloadObjectPathPrefix(t *testing.T) {
	tenant := bson.ObjectIdHex("5f2b6c9e8f1b2c3d4e5f6a7b")
	prefix := downloadObjectPathPrefix(tenant, "prodpub1")
	if prefix != "5f2b6c9e8f1b2c3d4e5f6a7b/downloads/prodpub1/" {
		t.Fatalf("unexpected prefix %q", prefix)
	}

	legit := prefix + "abc_file.pdf"
	if !strings.HasPrefix(legit, prefix) {
		t.Fatal("legit path must match its own prefix")
	}

	otherTenant := "6a3c7d0f9a2b3c4d5e6f7a8b/downloads/prodpub1/abc_file.pdf"
	otherProduct := "5f2b6c9e8f1b2c3d4e5f6a7b/downloads/otherprod/abc_file.pdf"
	traversal := "5f2b6c9e8f1b2c3d4e5f6a7b/downloads/prodpub1/../../../victim/file.pdf"
	for _, p := range []string{otherTenant, otherProduct, ""} {
		if strings.HasPrefix(p, prefix) {
			t.Fatalf("foreign path %q must not match prefix", p)
		}
	}
	// Prefix check alone permits dot segments — the UploadIntent lookup is the
	// authority that rejects them (paths are only ever minted server-side
	// without dot segments). Assert the assumption documented here holds.
	if !strings.HasPrefix(traversal, prefix) {
		t.Fatal("test premise changed")
	}
}
