package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const journalName = ".kokoro-restore.json"

type Transaction struct {
	Data        string   `json:"-"`
	RollbackDir string   `json:"rollback_dir"`
	Old         []string `json:"old"`
	New         []string `json:"new"`
}

func managedTop(name string) bool {
	return name == "images" || name == "kokoro.db-wal" || name == "kokoro.db-shm" || Managed(name)
}

// Install is called only after the HTTP server and old database have closed.
// A journal allows a killed process to restore the previous data on next boot.
func Install(data, stage string) (*Transaction, error) {
	data, e := filepath.Abs(data)
	if e != nil {
		return nil, e
	}
	stage, e = filepath.Abs(stage)
	if e != nil {
		return nil, e
	}
	if filepath.Dir(stage) != data || !strings.HasPrefix(filepath.Base(stage), ".restore-stage-") {
		return nil, errors.New("恢复暂存目录不合法")
	}
	tx := &Transaction{Data: data, RollbackDir: filepath.Join("backups", "pre-import-"+time.Now().UTC().Format("20060102-150405.000000000"))}
	for _, pair := range []struct {
		dir string
		out *[]string
	}{{data, &tx.Old}, {stage, &tx.New}} {
		entries, e := os.ReadDir(pair.dir)
		if e != nil {
			return nil, e
		}
		for _, entry := range entries {
			if !managedTop(entry.Name()) {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil, errors.New("恢复路径不能是符号链接")
			}
			*pair.out = append(*pair.out, entry.Name())
		}
	}
	if e = os.MkdirAll(filepath.Join(data, tx.RollbackDir), 0700); e != nil {
		return nil, e
	}
	raw, _ := json.Marshal(tx)
	journal := filepath.Join(data, journalName)
	f, e := os.OpenFile(journal, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, e
	}
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		_ = os.Remove(journal)
		return nil, e
	}
	failed := func(e error) (*Transaction, error) {
		if re := tx.Rollback(); re != nil {
			return nil, fmt.Errorf("恢复失败：%v；回退失败：%w", e, re)
		}
		return nil, e
	}
	for _, name := range tx.Old {
		if e = os.Rename(filepath.Join(data, name), filepath.Join(data, tx.RollbackDir, name)); e != nil {
			return failed(e)
		}
	}
	for _, name := range tx.New {
		if e = os.Rename(filepath.Join(stage, name), filepath.Join(data, name)); e != nil {
			return failed(e)
		}
	}
	_ = os.RemoveAll(stage)
	return tx, nil
}
func (tx *Transaction) Commit() error { return os.Remove(filepath.Join(tx.Data, journalName)) }
func (tx *Transaction) Rollback() error {
	old := map[string]bool{}
	for _, n := range tx.Old {
		old[n] = true
	}
	for _, n := range tx.New {
		if !old[n] {
			if e := os.RemoveAll(filepath.Join(tx.Data, n)); e != nil {
				return e
			}
		}
	}
	for _, n := range tx.Old {
		p := filepath.Join(tx.Data, tx.RollbackDir, n)
		if _, e := os.Lstat(p); os.IsNotExist(e) {
			continue
		} else if e != nil {
			return e
		}
		if e := os.RemoveAll(filepath.Join(tx.Data, n)); e != nil {
			return e
		}
		if e := os.Rename(p, filepath.Join(tx.Data, n)); e != nil {
			return e
		}
	}
	return os.Remove(filepath.Join(tx.Data, journalName))
}
func RecoverPending(data string) error {
	p := filepath.Join(data, journalName)
	b, e := os.ReadFile(p)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var tx Transaction
	if e = json.Unmarshal(b, &tx); e != nil {
		return e
	}
	tx.Data = data
	rel := filepath.Clean(tx.RollbackDir)
	if filepath.Dir(rel) != "backups" || !strings.HasPrefix(filepath.Base(rel), "pre-import-") {
		return errors.New("恢复日志路径不合法")
	}
	for _, n := range append(append([]string{}, tx.Old...), tx.New...) {
		if filepath.Base(n) != n || !managedTop(n) {
			return errors.New("恢复日志文件不合法")
		}
	}
	return tx.Rollback()
}
