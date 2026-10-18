package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const MaxArchiveSize int64 = 4 << 30
const MaxFiles = 100000

type Manifest struct {
	Format    int    `json:"format"`
	ID        string `json:"id"`
	Version   string `json:"version"`
	Schema    int    `json:"schema"`
	CreatedAt int64  `json:"created_at"`
	Source    string `json:"source"`
}

// Managed limits restores to application data; binaries, scripts, backups and
// server TLS configuration can never be installed from an uploaded archive.
func Managed(name string) bool {
	for _, c := range []byte(name) {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' || c == '/') {
			return false
		}
	}
	if name == "kokoro.db" {
		return true
	}
	if name == "" || strings.ContainsAny(name, "\\:\x00") || path.Clean(name) != name || strings.HasPrefix(name, "/") {
		return false
	}
	base := path.Base(name)
	ext := strings.ToLower(path.Ext(base))
	image := ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".webp" || ext == ".gif"
	if !image {
		return false
	}
	if strings.HasPrefix(name, "images/") && strings.Count(name, "/") == 1 {
		return true
	}
	return !strings.Contains(name, "/") && (strings.TrimSuffix(name, ext) == "avatar" || strings.TrimSuffix(name, ext) == "wallpaper" || strings.HasPrefix(name, "wallpaper-"))
}

func Create(dst io.Writer, pass, dataDir, snapshot string, m Manifest) error {
	ew, e := newEncryptWriter(dst, pass)
	if e != nil {
		return e
	}
	gz := gzip.NewWriter(ew)
	tw := tar.NewWriter(gz)
	add := func(name string, src io.Reader, size int64) error {
		if e := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: size, Typeflag: tar.TypeReg}); e != nil {
			return e
		}
		_, e := io.CopyN(tw, src, size)
		return e
	}
	meta, e := json.Marshal(m)
	if e != nil {
		return e
	}
	if e = add("manifest.json", strings.NewReader(string(meta)), int64(len(meta))); e != nil {
		return e
	}
	var total int64
	count := 0
	addFile := func(name, p string) error {
		info, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("备份不接受特殊文件：%s", name)
		}
		total += info.Size()
		count++
		if total > MaxArchiveSize || count > MaxFiles {
			return errors.New("备份数据超过大小限制")
		}
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		defer f.Close()
		return add(name, f, info.Size())
	}
	if e = addFile("kokoro.db", snapshot); e != nil {
		return e
	}
	e = filepath.WalkDir(dataDir, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if p == dataDir {
			return nil
		}
		rel, e := filepath.Rel(dataDir, p)
		if e != nil {
			return e
		}
		name := filepath.ToSlash(rel)
		if d.IsDir() {
			if name == "images" {
				return nil
			}
			return filepath.SkipDir
		}
		if !Managed(name) || name == "kokoro.db" {
			return nil
		}
		return addFile(name, p)
	})
	if e != nil {
		return e
	}
	if e = tw.Close(); e != nil {
		return e
	}
	if e = gz.Close(); e != nil {
		return e
	}
	return ew.Close()
}

// Extract authenticates the whole encrypted input before parsing the archive.
// Caller owns stage and must remove it on failure. No existing data is touched.
func Extract(src io.Reader, pass, stage string) (Manifest, error) {
	var m Manifest
	packed, e := os.CreateTemp(stage, ".packed-*")
	if e != nil {
		return m, e
	}
	defer os.Remove(packed.Name())
	e = decryptTo(packed, src, pass)
	if e != nil {
		packed.Close()
		return m, e
	}
	if _, e = packed.Seek(0, 0); e != nil {
		packed.Close()
		return m, e
	}
	defer packed.Close()
	gz, e := gzip.NewReader(packed)
	if e != nil {
		return m, errors.New("备份压缩数据无效")
	}
	defer gz.Close()
	// Include headers (including PAX records consumed internally by tar.Reader)
	// in the decompression budget, not just the visible file payloads.
	tr := tar.NewReader(io.LimitReader(gz, MaxArchiveSize+(128<<20)))
	seen := map[string]bool{}
	var total int64
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return m, e
		}
		if h.Typeflag != tar.TypeReg || h.Size < 0 || seen[h.Name] || len(seen) >= MaxFiles {
			return m, errors.New("备份包含重复或不支持的文件")
		}
		seen[h.Name] = true
		total += h.Size
		if total > MaxArchiveSize {
			return m, errors.New("备份解压后超过大小限制")
		}
		if h.Name == "manifest.json" {
			if h.Size > 8192 {
				return m, errors.New("备份清单过大")
			}
			b, e := io.ReadAll(io.LimitReader(tr, 8193))
			if e != nil {
				return m, e
			}
			if e = json.Unmarshal(b, &m); e != nil {
				return m, e
			}
			continue
		}
		if !Managed(h.Name) {
			return m, fmt.Errorf("备份包含不允许的路径：%s", h.Name)
		}
		p := filepath.Join(stage, filepath.FromSlash(h.Name))
		if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			return m, e
		}
		f, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return m, e
		}
		_, copyErr := io.CopyN(f, tr, h.Size)
		closeErr := f.Close()
		if copyErr != nil {
			return m, copyErr
		}
		if closeErr != nil {
			return m, closeErr
		}
	}
	// Drain gzip through its checksum and reject payload after tar's end marker.
	tail, e := io.Copy(io.Discard, io.LimitReader(gz, 1<<20))
	if e != nil || tail > 0 {
		return m, errors.New("备份压缩校验失败或包含多余内容")
	}
	if m.Format != 1 || m.ID == "" || len(m.ID) > 128 || m.Schema <= 0 || !seen["kokoro.db"] || !seen["manifest.json"] {
		return m, errors.New("备份清单不完整或格式版本不受支持")
	}
	return m, nil
}
