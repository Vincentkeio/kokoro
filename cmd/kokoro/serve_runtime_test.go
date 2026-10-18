package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/backup"
	"github.com/Vincentkeio/kokoro/internal/hub"
	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/store"
)

func TestServeHubImportReopensDatabaseAndInvalidatesSessions(t *testing.T) {
	source := t.TempDir()
	st, e := store.Open(filepath.Join(source, "kokoro.db"))
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256([]byte("nt_original"))
	n := &model.Node{Name: "migrated-node", TokenHash: hex.EncodeToString(hash[:])}
	if e = st.CreateNode(n); e != nil {
		t.Fatal(e)
	}
	hub.SetAdminCredentials(st, "source-admin", "source-password-long")
	st.Close()
	var encrypted bytes.Buffer
	id := strings.Repeat("b", 64)
	if e = backup.Create(&encrypted, "backup-password-long", source, filepath.Join(source, "kokoro.db"), backup.Manifest{Format: 1, ID: id, Schema: store.SchemaVersion}); e != nil {
		t.Fatal(e)
	}
	data := t.TempDir()
	target, e := store.Open(filepath.Join(data, "kokoro.db"))
	if e != nil {
		t.Fatal(e)
	}
	hub.SetAdminCredentials(target, "target-admin", "target-password-long")
	target.Close()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := ln.Addr().String()
	ln.Close()
	origin := "http://" + addr
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveHub(ctx, &model.HubConfig{Listen: addr, DataDir: data, TLSMode: "none"}) }()
	defer func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(10 * time.Second):
			t.Error("Hub did not stop")
		}
	}()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	wait := func(check func() bool) {
		t.Helper()
		end := time.Now().Add(10 * time.Second)
		for time.Now().Before(end) {
			if check() {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("Hub state did not converge")
	}
	wait(func() bool {
		resp, e := client.Get(origin + "/admin")
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
	login := func(user, pass string) int {
		t.Helper()
		values := url.Values{"username": {user}, "password": {pass}}
		req, _ := http.NewRequest("POST", origin+"/admin", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		resp, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if login("target-admin", "target-password-long") != 303 {
		t.Fatal("could not log into target")
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	f, _ := mw.CreateFormFile("backup", "export.kokoro")
	f.Write(encrypted.Bytes())
	mw.WriteField("password", "backup-password-long")
	mw.WriteField("confirm", "覆盖并恢复")
	mw.Close()
	req, _ := http.NewRequest("POST", origin+"/admin/backup/import", &body)
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("import: %d %s", resp.StatusCode, b)
	}
	wait(func() bool {
		probe, _ := http.NewRequest("POST", origin+"/api/v1/migration/node", nil)
		probe.Header.Set("Authorization", "Bearer nt_original")
		r, e := client.Do(probe)
		if e != nil {
			return false
		}
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		return r.StatusCode == 200 && strings.Contains(string(body), n.ID)
	})
	oldSession, _ := http.NewRequest("GET", origin+"/admin/backup/status", nil)
	oldSession.Header.Set("Accept", "application/json")
	oldResp, e := client.Do(oldSession)
	if e != nil {
		t.Fatal(e)
	}
	oldResp.Body.Close()
	if oldResp.StatusCode == 200 {
		t.Fatal("target session survived restore")
	}
	// 导入的是数据，不是登录凭据：面板保留导入前那套账密，备份里的那份不生效。
	if login("target-admin", "target-password-long") != 303 {
		t.Fatal("导入后应当仍能用当前面板的账密登录")
	}
	if login("source-admin", "source-password-long") != 200 {
		t.Fatal("备份里的旧账密不该顶掉当前面板的账密")
	}
}

// 备份文件里不该出现管理员账密：账密属于面板本身，跟着备份走就会在导入时
// 把目标面板现有的账密顶掉（会话同时被清空，操作者会以为密码突然失效）。
func TestPrepareExportSnapshotOmitsAdminCredentials(t *testing.T) {
	dir := t.TempDir()
	st, e := store.Open(filepath.Join(dir, "kokoro.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	hash := sha256.Sum256([]byte("nt_keepme"))
	n := &model.Node{Name: "node-a", TokenHash: hex.EncodeToString(hash[:])}
	if e = st.CreateNode(n); e != nil {
		t.Fatal(e)
	}
	if e = hub.SetAdminCredentials(st, "source-admin", "source-password-long"); e != nil {
		t.Fatal(e)
	}
	if e = hub.PrepareExportSnapshot(context.Background(), st); e != nil {
		t.Fatal(e)
	}
	if hub.AdminCredentialsConfigured(st) {
		t.Error("备份里不该包含管理员口令")
	}
	if value, err := st.GetSetting("admin_user"); err == nil && value != "" {
		t.Errorf("备份里不该包含管理员用户名，得到 %q", value)
	}
	// 数据本身必须原样留在快照里，别把账密和数据一起清了。
	if _, e = st.GetNodeByTokenHash(hex.EncodeToString(hash[:])); e != nil {
		t.Errorf("备份应当仍然包含节点数据: %v", e)
	}
}
