package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const password = "correct-horse-backup-password"

func TestEncryptedFramesRejectCorruptionTruncationAndReordering(t *testing.T) {
	data := bytes.Repeat([]byte("sensitive-data-"), 200000)
	var sealed bytes.Buffer
	w, e := newEncryptWriter(&sealed, password)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.Write(data); e != nil {
		t.Fatal(e)
	}
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	var clear bytes.Buffer
	if e = decryptTo(&clear, bytes.NewReader(sealed.Bytes()), password); e != nil || !bytes.Equal(data, clear.Bytes()) {
		t.Fatalf("roundtrip: %v", e)
	}
	for name, modify := range map[string]func([]byte) []byte{"truncated": func(b []byte) []byte { return b[:len(b)-20] }, "modified": func(b []byte) []byte { b[len(b)/2] ^= 1; return b }, "appended": func(b []byte) []byte { return append(b, 0) }} {
		t.Run(name, func(t *testing.T) {
			b := modify(append([]byte(nil), sealed.Bytes()...))
			if e := decryptTo(io.Discard, bytes.NewReader(b), password); e == nil {
				t.Fatal("unauthenticated backup accepted")
			}
		})
	}
	if e = decryptTo(io.Discard, bytes.NewReader(sealed.Bytes()), "wrong-password-long"); e == nil {
		t.Fatal("wrong password accepted")
	}
}
func TestArchiveRoundTripAndPathRestrictions(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "images"), 0700)
	os.WriteFile(filepath.Join(dir, "images", "fixture.webp"), []byte("image"), 0600)
	os.WriteFile(filepath.Join(dir, "avatar.png"), []byte("avatar"), 0600)
	os.WriteFile(filepath.Join(dir, "private-script.sh"), []byte("never"), 0600)
	snap := filepath.Join(t.TempDir(), "snapshot")
	os.WriteFile(snap, []byte("database"), 0600)
	var out bytes.Buffer
	m := Manifest{Format: 1, ID: "id", Schema: 23}
	if e := Create(&out, password, dir, snap, m); e != nil {
		t.Fatal(e)
	}
	stage := t.TempDir()
	got, e := Extract(bytes.NewReader(out.Bytes()), password, stage)
	if e != nil {
		t.Fatal(e)
	}
	if got.ID != m.ID {
		t.Fatal("manifest lost")
	}
	for _, name := range []string{"kokoro.db", "avatar.png", "images/fixture.webp"} {
		if _, e = os.Stat(filepath.Join(stage, name)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = os.Stat(filepath.Join(stage, "private-script.sh")); !os.IsNotExist(e) {
		t.Fatal("unexpected file included")
	}
	for _, name := range []string{"../kokoro.db", "/kokoro.db", "images/../avatar.png", "images\\evil.png", "images/sub/img.png", "dist/install.sh", "kokoro.db-wal", "avatar.exe"} {
		if Managed(name) {
			t.Fatalf("unsafe path allowed: %q", name)
		}
	}
	var malicious bytes.Buffer
	ew, _ := newEncryptWriter(&malicious, password)
	gz := gzip.NewWriter(ew)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0600, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	ew.Close()
	if _, e = Extract(bytes.NewReader(malicious.Bytes()), password, t.TempDir()); e == nil {
		t.Fatal("path traversal accepted")
	}
}
func TestInterruptedRestoreRecoversOriginalData(t *testing.T) {
	data := t.TempDir()
	os.WriteFile(filepath.Join(data, "kokoro.db"), []byte("old"), 0600)
	os.WriteFile(filepath.Join(data, "kokoro.db-wal"), []byte("wal"), 0600)
	stage, e := os.MkdirTemp(data, ".restore-stage-")
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(stage, "kokoro.db"), []byte("new"), 0600)
	os.WriteFile(filepath.Join(stage, "avatar.png"), []byte("newasset"), 0600)
	if _, e = Install(data, stage); e != nil {
		t.Fatal(e)
	}
	if e = RecoverPending(data); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(data, "kokoro.db"))
	if string(b) != "old" {
		t.Fatal("old database was not restored")
	}
	b, _ = os.ReadFile(filepath.Join(data, "kokoro.db-wal"))
	if string(b) != "wal" {
		t.Fatal("old WAL was not restored")
	}
	if _, e = os.Stat(filepath.Join(data, "avatar.png")); !os.IsNotExist(e) {
		t.Fatal("imported asset survived rollback")
	}
}
