package syncx

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPackUnpack_RoundTrip(t *testing.T) {
	files := map[string][]byte{
		"meta.json":      []byte(`{"extension_id":"uBlock0@raymondhill.net"}`),
		"storage.sqlite": bytes.Repeat([]byte{0x01, 0x02, 0x03}, 5000),
		"metadata-v2":    {0x00, 0x06, 0x54, 0xf0},
		"files/6628":     bytes.Repeat([]byte{0xAB}, 1234),
		"files/6630":     []byte("external value"),
	}
	src := writeTree(t, files)

	packed, err := Pack(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := Unpack(packed, dst); err != nil {
		t.Fatal(err)
	}
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: content mismatch", rel)
		}
	}
}

func TestPack_Deterministic(t *testing.T) {
	files := map[string][]byte{
		"meta.json":      []byte("{}"),
		"storage.sqlite": bytes.Repeat([]byte{0x09}, 9999),
		"files/2":        []byte("b"),
		"files/1":        []byte("a"),
		"files/10":       []byte("c"),
	}
	src := writeTree(t, files)
	a, err := Pack(src)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Pack(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("Pack is not deterministic for identical content")
	}
}

func TestUnpack_RejectsBadHeader(t *testing.T) {
	if err := Unpack([]byte("not a gusset archive"), t.TempDir()); err == nil {
		t.Fatal("accepted a bad header")
	}
}

func TestUnpack_RejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(packMagic)
	putUvarint(&buf, 1)
	putBytes(&buf, []byte("../escape"))
	putBytes(&buf, []byte("payload"))

	dst := t.TempDir()
	if err := Unpack(buf.Bytes(), dst); err == nil {
		t.Fatal("path traversal entry accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "escape")); err == nil {
		t.Fatal("traversal actually wrote outside destDir")
	}
}

func TestUnpack_RejectsAbsolutePath(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(packMagic)
	putUvarint(&buf, 1)
	putBytes(&buf, []byte("/etc/evil"))
	putBytes(&buf, []byte("x"))
	if err := Unpack(buf.Bytes(), t.TempDir()); err == nil {
		t.Fatal("absolute path entry accepted")
	}
}
