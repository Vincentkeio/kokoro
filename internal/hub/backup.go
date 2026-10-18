package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/backup"
	"github.com/Vincentkeio/kokoro/internal/store"
)

var ErrRestoreReady = errors.New("hub: restore is ready")

// prepareExportSnapshot 把快照整理成"可以离开这台机器"的样子：注销其中的会话、
// 取消未完成的任务，并去掉管理员账密。
//
// 账密属于面板本身，不是要迁移的数据。留在备份里的话，导入端会拿它顶掉目标
// 面板现有的账密（会话又刚被清空，操作者会以为密码凭空失效了）。要迁移登录
// 凭据没有意义：目标面板自己的凭据继续用 kokoro passwd 管理。
func PrepareExportSnapshot(ctx context.Context, st *store.Store) error {
	if err := st.PrepareBackupRestore(ctx); err != nil {
		return err
	}
	return st.DeleteSettings(ctx, settingAdminUser, settingAdminPass)
}

func randomMigrationID() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func (h *Hub) PendingRestore() string { return h.restoreStage }

// SetReadyCallback runs after the replacement Hub has initialized and acquired
// its listener. Only then may the restore transaction become permanent.
func (h *Hub) SetReadyCallback(fn func() error) { h.onReady = fn }

func (h *Hub) backupMethod(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeErr(w, 405, "只接受 POST")
		return false
	}
	if h.cfg.DataDir == "" {
		writeErr(w, 503, "数据目录尚未配置")
		return false
	}
	return true
}
func (h *Hub) handleBackupExport(w http.ResponseWriter, r *http.Request) {
	if !h.backupMethod(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if e := r.ParseForm(); e != nil {
		writeErr(w, 400, "表单无效")
		return
	}
	downloadID := r.FormValue("download_id")
	if len(downloadID) > 64 || strings.ContainsAny(downloadID, ";\r\n ") {
		downloadID = ""
	}
	resultCookie := func(result string) {
		if downloadID != "" {
			http.SetCookie(w, &http.Cookie{Name: "kokoro_backup_result", Value: downloadID + "." + result, Path: "/admin", MaxAge: 120, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || h.cfg.BehindProxy})
		}
	}
	// A failed marker is sent with any error response. A successful attachment
	// replaces it in the same response, so native downloads need no Blob in RAM.
	resultCookie("failed")
	if !h.transferMu.TryLock() {
		writeErr(w, 409, "已有备份或恢复操作进行中")
		return
	}
	defer h.transferMu.Unlock()
	if h.restoreQueued || h.updateBusy() {
		writeErr(w, 409, "面板正在恢复")
		return
	}
	pass := r.FormValue("password")
	if len(pass) < 12 || len(pass) > 256 || pass != r.FormValue("confirm_password") {
		resultCookie("failed")
		writeErr(w, 400, "两次备份密码须一致，长度 12～256 字节")
		return
	}
	stage, e := os.MkdirTemp(h.cfg.DataDir, ".backup-export-")
	if e != nil {
		writeErr(w, 500, "无法创建备份暂存目录")
		return
	}
	defer os.RemoveAll(stage)
	id := randomMigrationID()
	secret, e := h.store.GetSetting("panel_migration_key")
	if e != nil {
		writeErr(w, 500, "无法读取迁移身份")
		return
	}
	if secret == "" {
		secret = randomMigrationID()
		if e = h.store.SetSetting("panel_migration_key", secret); e != nil {
			writeErr(w, 500, "无法保存迁移身份")
			return
		}
	}
	snapshot := filepath.Join(stage, "kokoro.db")
	if e = h.store.Snapshot(r.Context(), snapshot); e != nil {
		log.Printf("[backup] snapshot: %v", e)
		writeErr(w, 500, "生成数据库快照失败")
		return
	}
	st, e := store.Open(snapshot)
	if e != nil {
		writeErr(w, 500, "校验快照失败")
		return
	}
	e = PrepareExportSnapshot(r.Context(), st)
	if e == nil {
		e = st.SetSetting("panel_restored_backup_id", id)
	}
	if e == nil {
		e = st.ValidateBackup(r.Context())
	}
	closeErr := st.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		log.Printf("[backup] prepare: %v", e)
		writeErr(w, 500, "备份数据库校验失败")
		return
	}
	archive := filepath.Join(stage, "backup.kokoro")
	f, e := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		writeErr(w, 500, "无法创建备份文件")
		return
	}
	defer f.Close()
	m := backup.Manifest{Format: 1, ID: id, Version: Version, Schema: store.SchemaVersion, CreatedAt: time.Now().UnixMilli(), Source: h.cfg.Domain}
	if e = backup.Create(f, pass, h.cfg.DataDir, snapshot, m); e != nil {
		log.Printf("[backup] archive: %v", e)
		writeErr(w, 500, "生成备份失败，请检查磁盘空间和数据文件")
		return
	}
	if e = h.store.SetSetting("panel_last_export_id", id); e != nil {
		writeErr(w, 500, "记录备份身份失败")
		return
	}
	if _, e = f.Seek(0, 0); e != nil {
		writeErr(w, 500, "读取备份失败")
		return
	}
	info, e := f.Stat()
	if e != nil {
		writeErr(w, 500, "读取备份失败")
		return
	}
	resultCookie("ready")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="kokoro-backup-%s.kokoro"`, time.Now().Format("20060102-150405")))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	_, _ = io.Copy(w, f)
}

func (h *Hub) handleBackupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "只接受 GET")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	busy := true
	if h.transferMu.TryLock() {
		busy = h.restoreQueued || h.updateBusy()
		h.transferMu.Unlock()
	}
	writeJSON(w, 200, map[string]any{"ok": true, "busy": busy})
}

func (h *Hub) handleBackupImport(w http.ResponseWriter, r *http.Request) {
	if !h.backupMethod(w, r) {
		return
	}
	if !h.transferMu.TryLock() {
		writeErr(w, 409, "已有备份或恢复操作进行中")
		return
	}
	defer h.transferMu.Unlock()
	if h.restoreQueued || h.updateBusy() {
		writeErr(w, 409, "面板正在恢复")
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, backup.MaxArchiveSize+(64<<20))
	if e := r.ParseMultipartForm(1 << 20); e != nil {
		writeErr(w, 400, "读取备份失败，文件上限为 4 GB")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if r.FormValue("confirm") != "覆盖并恢复" {
		writeErr(w, 400, "请输入“覆盖并恢复”确认导入")
		return
	}
	pass := r.FormValue("password")
	src, _, e := r.FormFile("backup")
	if e != nil {
		writeErr(w, 400, "请选择备份文件")
		return
	}
	defer src.Close()
	stage, e := os.MkdirTemp(h.cfg.DataDir, ".restore-stage-")
	if e != nil {
		writeErr(w, 500, "无法创建恢复暂存目录")
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(stage)
		}
	}()
	m, e := backup.Extract(src, pass, stage)
	if e != nil {
		writeErr(w, 400, e.Error())
		return
	}
	if m.Schema > store.SchemaVersion {
		writeErr(w, 400, "备份来自较新的面板，请先升级当前面板")
		return
	}
	st, e := store.Open(filepath.Join(stage, "kokoro.db"))
	if e != nil {
		writeErr(w, 400, "备份数据库无法打开或版本不兼容")
		return
	}
	e = st.ValidateBackup(r.Context())
	if e == nil {
		e = st.PrepareBackupRestore(r.Context())
	}
	if e == nil {
		// 导入的是数据，不是这台面板的登录凭据。备份本身也不带账密，这里再把
		// 当前面板的账密写进去：操作者现在的密码必须继续可用，否则他刚导入完
		// 就被旧凭据挡在门外（会话同时也被清空了，看起来就像密码突然不对）。
		e = applyAdminCredentials(st, snapshotAdminCredentials(h.store))
	}
	if e == nil {
		e = st.SetSetting("panel_restored_backup_id", m.ID)
	}
	if e == nil {
		e = st.SetSetting("panel_restored_at", fmt.Sprint(time.Now().UnixMilli()))
	}
	if e == nil {
		_, e = st.DB().ExecContext(r.Context(), "UPDATE nodes SET online=0,last_seen=0")
	}
	closeErr := st.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		log.Printf("[backup] import validation: %v", e)
		writeErr(w, 400, "备份数据库校验失败")
		return
	}
	// Signal only after all validation and staging has completed. Run drains HTTP
	// handlers, closes the old DB, and restores before reopening the listener.
	h.restoreQueued = true
	keep = true
	writeJSON(w, 200, map[string]any{"ok": true, "message": "备份已校验，正在恢复。请稍后使用原面板的管理员账号登录。"})
	h.restoreCh <- stage
}

// ValidateRestoredData is used by the CLI before committing a restore.
func ValidateRestoredData(ctx context.Context, st *store.Store) error { return st.ValidateBackup(ctx) }
