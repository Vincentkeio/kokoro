package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/store"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "pbkdf2$sha256$") {
		t.Fatalf("哈希格式不对: %s", hash)
	}
	if got := len(strings.Split(hash, "$")); got != 5 {
		t.Fatalf("哈希应有 5 段，实际 %d: %s", got, hash)
	}
	if ok, upgrade := verifyPassword(hash, "correct horse battery staple"); !ok || upgrade {
		t.Errorf("正确口令应通过且不需升级，得到 ok=%v upgrade=%v", ok, upgrade)
	}
	if ok, _ := verifyPassword(hash, "wrong"); ok {
		t.Error("错误口令不该通过")
	}
	// 同一口令两次哈希必须不同（盐是随机的）
	hash2, _ := hashPassword("correct horse battery staple")
	if hash == hash2 {
		t.Error("两次哈希相同，说明没加随机盐")
	}
}

// TestLegacySha256AcceptedAndUpgraded 保证老库（裸 sha256）还能登录，
// 并且登录成功后哈希会被原地升级成 PBKDF2。
func TestLegacySha256AcceptedAndUpgraded(t *testing.T) {
	sum := sha256.Sum256([]byte("oldpass123"))
	legacy := hex.EncodeToString(sum[:])

	ok, upgrade := verifyPassword(legacy, "oldpass123")
	if !ok || !upgrade {
		t.Fatalf("旧格式应通过且提示升级，得到 ok=%v upgrade=%v", ok, upgrade)
	}
	if ok, _ := verifyPassword(legacy, "nope"); ok {
		t.Error("旧格式下错误口令不该通过")
	}
	if _, upgrade := verifyPassword(legacy, "oldpass123"); !upgrade {
		t.Error("旧格式必须被标记为需要升级")
	}
}

func newTestHub(t *testing.T) (*Hub, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir + "/kokoro.db")
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &model.HubConfig{
		Listen:   "127.0.0.1:0",
		DataDir:  dir,
		SiteName: "Kokoro 测试",
		TLSMode:  "none",
	}
	h, err := New(cfg, st)
	if err != nil {
		t.Fatalf("初始化 Hub 失败: %v", err)
	}
	return h, st
}

// TestLoginFlow 端到端走一遍：用户名+密码登录 → 拿到会话 cookie → 用 cookie 进后台。
func TestLoginFlow(t *testing.T) {
	h, st := newTestHub(t)
	if err := SetAdminCredentials(st, "boss", "s3cret-pass"); err != nil {
		t.Fatalf("设置账号失败: %v", err)
	}

	// 1. 未登录访问后台：应该看到登录页而不是后台内容
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	h.handleAdmin(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "管理员用户名") {
		t.Errorf("未登录应返回登录页，实际内容片段: %.120s", body)
	}
	if strings.Contains(body, "管理员账号") {
		t.Error("未登录不该看到后台内容")
	}

	// 2. 密码错：登录失败
	form := url.Values{"username": {"boss"}, "password": {"wrong-pass"}}
	req = httptest.NewRequest(http.MethodPost, "/admin", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.handleAdmin(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("登录失败应回渲染登录页(200)，得到 %d", w.Code)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Error("密码错误时不该下发会话 cookie")
	}
	if !strings.Contains(w.Body.String(), "用户名或密码不对") {
		t.Error("应提示用户名或密码不对")
	}

	// 3. 用户名错：也不放行
	form = url.Values{"username": {"admin"}, "password": {"s3cret-pass"}}
	req = httptest.NewRequest(http.MethodPost, "/admin", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.handleAdmin(w, req)
	if len(w.Result().Cookies()) != 0 {
		t.Error("用户名错误时不该下发会话 cookie")
	}

	// 4. 正确登录：303 + cookie
	form = url.Values{"username": {"boss"}, "password": {"s3cret-pass"}}
	req = httptest.NewRequest(http.MethodPost, "/admin", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.handleAdmin(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("登录成功应 303，得到 %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("登录成功应下发会话 cookie")
	}
	sess := cookies[0]
	if sess.Name != adminCookie || sess.Value == "" {
		t.Fatalf("cookie 不对: %+v", sess)
	}
	// cookie 里绝不能出现口令哈希（无论新旧格式）
	hash, _ := st.GetSetting(settingAdminPass)
	if hash != "" && strings.Contains(sess.Value, hash) {
		t.Error("会话 cookie 里出现了口令哈希，泄露风险")
	}

	// 5. 带 cookie 访问后台：应该看到后台
	req = httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(sess)
	w = httptest.NewRecorder()
	h.handleAdmin(w, req)
	if body := w.Body.String(); !strings.Contains(body, "管理员账号") {
		t.Errorf("带会话应进入后台，实际内容片段: %.160s", body)
	}

	// 6. 注销后再访问：回到登录页
	req = httptest.NewRequest(http.MethodPost, "/admin/logout", nil)
	req.AddCookie(sess)
	w = httptest.NewRecorder()
	h.handleAdminLogout(w, req)
	req = httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(sess)
	w = httptest.NewRecorder()
	h.handleAdmin(w, req)
	if strings.Contains(w.Body.String(), "管理员账号") {
		t.Error("注销后不该还能进后台")
	}
}

// TestSessionExpiry 过期的会话必须失效。
func TestSessionExpiry(t *testing.T) {
	h, st := newTestHub(t)
	if err := SetAdminCredentials(st, "boss", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	token := "deadbeefdeadbeefdeadbeefdeadbeef"
	if err := st.SetSetting(sessionKeyPrefix+sessionID(token),
		"1"); err != nil { // 1970 年的时间戳，必然过期
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(&http.Cookie{Name: adminCookie, Value: token})
	if h.adminAuthed(req) {
		t.Error("过期会话应被判为未登录")
	}
}

// TestChangePasswordDropsSessions 改密码后旧会话全部失效。
func TestChangePasswordDropsSessions(t *testing.T) {
	h, st := newTestHub(t)
	if err := SetAdminCredentials(st, "boss", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	// 手工造一个有效会话
	token := "cafebabecafebabecafebabecafebabe"
	exp := strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
	if err := st.SetSetting(sessionKeyPrefix+sessionID(token), exp); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(&http.Cookie{Name: adminCookie, Value: token})
	if !h.adminAuthed(req) {
		t.Fatal("造出来的会话应当有效（可能是时间格式写错了）")
	}

	form := url.Values{
		"username":         {"boss"},
		"current_password": {"s3cret-pass"},
		"new_password":     {"brand-new-pass"},
		"confirm_password": {"brand-new-pass"},
	}
	req = httptest.NewRequest(http.MethodPost, "/admin/account", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: adminCookie, Value: token})
	w := httptest.NewRecorder()
	h.handleAdminAccount(w, req)

	// 旧会话应该已被清掉
	req2 := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req2.AddCookie(&http.Cookie{Name: adminCookie, Value: token})
	if h.adminAuthed(req2) {
		t.Error("改密码后旧会话应失效")
	}
	// 新密码能登录
	ok, _ := verifyPassword(mustSetting(t, st, settingAdminPass), "brand-new-pass")
	if !ok {
		t.Error("新密码应能通过校验")
	}
	okOld, _ := verifyPassword(mustSetting(t, st, settingAdminPass), "s3cret-pass")
	if okOld {
		t.Error("旧密码不该还能用")
	}
}

// TestSetAdminCredentialsValidation 口令太短/为空要被拒绝。
func TestSetAdminCredentialsValidation(t *testing.T) {
	h, st := newTestHub(t)
	_ = h
	if err := SetAdminCredentials(st, "boss", ""); err == nil {
		t.Error("空口令应被拒绝")
	}
	if err := SetAdminCredentials(st, strings.Repeat("x", 40), "pass12345"); err == nil {
		t.Error("超长用户名应被拒绝")
	}
}

func mustSetting(t *testing.T, st *store.Store, key string) string {
	t.Helper()
	v, err := st.GetSetting(key)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", key, err)
	}
	return v
}
