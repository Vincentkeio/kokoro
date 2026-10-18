// Package backup implements authenticated, bounded-memory Kokoro backup files.
package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
)

const magic = "KOKORO-BACKUP-1\n"
const chunkSize = 1 << 20

func key(pass string, salt []byte) ([]byte, error) {
	if len(pass) < 12 || len(pass) > 256 {
		return nil, errors.New("备份密码须为 12～256 字节")
	}
	return pbkdf2.Key(sha256.New, pass, salt, 300000, 32)
}

type encryptWriter struct {
	dst    io.Writer
	aead   cipher.AEAD
	prefix []byte
	seq    uint64
	buf    []byte
}

func newEncryptWriter(dst io.Writer, pass string) (*encryptWriter, error) {
	salt := make([]byte, 16)
	prefix := make([]byte, 4)
	if _, e := rand.Read(salt); e != nil {
		return nil, e
	}
	if _, e := rand.Read(prefix); e != nil {
		return nil, e
	}
	k, e := key(pass, salt)
	if e != nil {
		return nil, e
	}
	b, e := aes.NewCipher(k)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	for _, p := range [][]byte{[]byte(magic), salt, prefix} {
		if _, e = dst.Write(p); e != nil {
			return nil, e
		}
	}
	return &encryptWriter{dst: dst, aead: a, prefix: prefix, buf: make([]byte, 0, chunkSize)}, nil
}
func nonce(prefix []byte, seq uint64) []byte {
	n := make([]byte, 12)
	copy(n, prefix)
	binary.BigEndian.PutUint64(n[4:], seq)
	return n
}
func (w *encryptWriter) flush(p []byte) error {
	n := nonce(w.prefix, w.seq)
	w.seq++
	sealed := w.aead.Seal(nil, n, p, []byte(magic))
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(sealed)))
	if _, e := w.dst.Write(size[:]); e != nil {
		return e
	}
	_, e := w.dst.Write(sealed)
	return e
}
func (w *encryptWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		n := min(chunkSize-len(w.buf), len(p))
		w.buf = append(w.buf, p[:n]...)
		p = p[n:]
		if len(w.buf) == chunkSize {
			if e := w.flush(w.buf); e != nil {
				return total - len(p), e
			}
			w.buf = w.buf[:0]
		}
	}
	return total, nil
}
func (w *encryptWriter) Close() error {
	if len(w.buf) > 0 {
		if e := w.flush(w.buf); e != nil {
			return e
		}
	}
	return w.flush(nil)
}

// decryptTo requires an authenticated final frame and rejects appended data.
func decryptTo(dst io.Writer, src io.Reader, pass string) error {
	head := make([]byte, len(magic)+20)
	if _, e := io.ReadFull(src, head); e != nil {
		return errors.New("备份文件不完整")
	}
	if string(head[:len(magic)]) != magic {
		return errors.New("不是受支持的 Kokoro 备份文件")
	}
	k, e := key(pass, head[len(magic):len(magic)+16])
	if e != nil {
		return e
	}
	b, _ := aes.NewCipher(k)
	a, _ := cipher.NewGCM(b)
	prefix := head[len(magic)+16:]
	var seq uint64
	var total int64
	for {
		var size [4]byte
		if _, e := io.ReadFull(src, size[:]); e != nil {
			return errors.New("备份文件缺少完整结束标记")
		}
		n := binary.BigEndian.Uint32(size[:])
		if n < 16 || n > chunkSize+16 {
			return errors.New("备份数据块无效")
		}
		sealed := make([]byte, n)
		if _, e := io.ReadFull(src, sealed); e != nil {
			return errors.New("备份文件不完整")
		}
		p, e := a.Open(nil, nonce(prefix, seq), sealed, []byte(magic))
		seq++
		if e != nil {
			return errors.New("密码错误或备份文件已损坏")
		}
		if len(p) == 0 {
			var extra [1]byte
			n, e := src.Read(extra[:])
			if n != 0 || e != io.EOF {
				return errors.New("备份包含多余数据")
			}
			return nil
		}
		total += int64(len(p))
		if total > MaxArchiveSize {
			return errors.New("备份超过大小限制")
		}
		if _, e := dst.Write(p); e != nil {
			return e
		}
	}
}
