package hub

// 后台鉴权：用户名 + 密码登录，登录后发一个随机会话令牌（cookie 里只放令牌，
// 不放任何口令派生物）。
//
// 口令存储用 PBKDF2-HMAC-SHA256（每账号一个 16 字节随机盐，100k 次迭代），
// 存储格式：pbkdf2$sha256$<迭代数>$<盐 hex>$<派生密钥 hex>
//
// 兼容旧库：早期版本存的是裸 sha256 十六进制（64 位、无 $ 分隔），
// 登录时仍接受，但校验通过后会立刻原地升级成 PBKDF2。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kokoro-probe/kokoro/internal/store"
)

const (
	// adminCookie 后台会话 cookie 名。
	adminCookie = "kokoro_admin"
	// sessionTTL 会话有效期。
	sessionTTL = 7 * 24 * time.Hour
	// sessionKeyPrefix settings 表里会话记录的键前缀。
	sessionKeyPrefix = "adm_sess:"

	pbkdf2Iter    = 100_000
	pbkdf2KeyLen  = 32
	pbkdf2SaltLen = 16

	// settingAdminUser / settingAdminPass 管理员账号在 settings 里的键名。
	settingAdminUser = "admin_user"
	settingAdminPass = "admin_pass_hash"
)

// ---- 口令存储 ----

// hashPassword 生成 PBKDF2 口令串。
func hashPassword(pass string) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2SHA256([]byte(pass), salt, pbkdf2Iter, pbkdf2KeyLen)
	return fmt.Sprintf("pbkdf2$sha256$%d$%s$%s", pbkdf2Iter,
		hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

// verifyPassword 校验口令。
// 第二个返回值表示"这份旧哈希该升级了"（旧版裸 sha256 命中时）。
func verifyPassword(stored, pass string) (ok bool, needsUpgrade bool) {
	if stored == "" {
		return false, false
	}
	if !strings.HasPrefix(stored, "pbkdf2$") {
		// 旧格式：裸 sha256 十六进制
		sum := sha256.Sum256([]byte(pass))
		given := hex.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(given), []byte(stored)) == 1 {
			return true, true
		}
		return false, false
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 5 || parts[1] != "sha256" {
		return false, false
	}
	iter, err := strconv.Atoi(parts[2])
	if err != nil || iter <= 0 || iter > 5_000_000 {
		return false, false
	}
	salt, err := hex.DecodeString(parts[3])
	if err != nil {
		return false, false
	}
	want, err := hex.DecodeString(parts[4])
	if err != nil {
		return false, false
	}
	got := pbkdf2SHA256([]byte(pass), salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1, false
}

// pbkdf2SHA256 是 RFC 2898 的 PBKDF2-HMAC-SHA256 实现。
// 自己写而不引依赖：算法只有二十行，能省一个外部包（体积是硬指标）。
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	blocks := (keyLen + hashLen - 1) / hashLen

	out := make([]byte, 0, blocks*hashLen)
	buf := make([]byte, 4)
	u := make([]byte, hashLen)
	t := make([]byte, hashLen)

	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)
		prf.Write(buf)
		u = prf.Sum(u[:0])
		copy(t, u)
		for i := 1; i < iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// ---- 账号读写 ----

// adminUsername 返回当前管理员用户名；没有就返回 "admin"。
func (h *Hub) adminUsername() string {
	if v, err := h.store.GetSetting(settingAdminUser); err == nil && v != "" {
		return v
	}
	return "admin"
}

// setAdminCredentials 写入管理员用户名与口令哈希。
func (h *Hub) setAdminCredentials(user, pass string) error {
	return SetAdminCredentials(h.store, user, pass)
}

// SetAdminCredentials 供后台与命令行（kokoro passwd）共用：设置管理员账号。
// user 为空时保留原用户名（没有则用 "admin"）。这是唯一的口令重置入口，
// 因为口令明文不落盘，忘了只能重设。
func SetAdminCredentials(st *store.Store, user, pass string) error {
	if st == nil {
		return errors.New("store 为空")
	}
	if pass == "" {
		return errors.New("口令不能为空")
	}
	if user == "" {
		if v, err := st.GetSetting(settingAdminUser); err == nil && v != "" {
			user = v
		} else {
			user = "admin"
		}
	}
	user = strings.TrimSpace(user)
	if user == "" || len([]rune(user)) > 32 {
		return errors.New("用户名不能为空且不超过 32 字")
	}
	hash, err := hashPassword(pass)
	if err != nil {
		return err
	}
	if err := st.SetSetting(settingAdminUser, user); err != nil {
		return err
	}
	return st.SetSetting(settingAdminPass, hash)
}

// ensureAdmin 保证存在管理员账号。库里没有就生成一个随机口令，
// 并且**仅在日志里打印一次**（明文不落盘），其余时间无从找回，只能重设。
func (h *Hub) ensureAdmin() (user, pass string, created bool) {
	user = h.adminUsername()
	hash, err := h.store.GetSetting(settingAdminPass)
	if err == nil && hash != "" {
		return user, "", false
	}

	buf := make([]byte, 9)
	if _, err := rand.Read(buf); err != nil {
		log.Printf("[hub] 生成管理员口令失败: %v", err)
		return user, "", false
	}
	pass = hex.EncodeToString(buf)
	if user == "" {
		user = "admin"
	}
	if err := h.setAdminCredentials(user, pass); err != nil {
		log.Printf("[hub] 保存管理员账号失败: %v", err)
		return user, "", false
	}
	log.Printf("[hub] ==========================================")
	log.Printf("[hub] 已生成管理员账号: %s / %s", user, pass)
	log.Printf("[hub] 请立刻登录后台改成自己的密码（明文只在这里出现一次）")
	log.Printf("[hub] ==========================================")
	return user, pass, true
}

// ---- 会话 ----

// newSession 生成会话令牌并写 cookie。cookie 里只有随机令牌，没有口令派生值。
func (h *Hub) newSession(w http.ResponseWriter, r *http.Request) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		log.Printf("[hub] 生成会话令牌失败: %v", err)
		return
	}
	token := hex.EncodeToString(buf)
	expires := time.Now().Add(sessionTTL).UnixMilli()
	if err := h.store.SetSetting(sessionKeyPrefix+sessionID(token), strconv.FormatInt(expires, 10)); err != nil {
		log.Printf("[hub] 保存会话失败: %v", err)
		return
	}
	h.pruneSessions()
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.BehindProxy || h.cfg.TLSMode == "auto",
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// dropSession 注销当前会话。
func (h *Hub) dropSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(adminCookie); err == nil && c.Value != "" {
		_ = h.store.SetSetting(sessionKeyPrefix+sessionID(c.Value), "")
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.BehindProxy || h.cfg.TLSMode == "auto",
		MaxAge:   -1,
	})
}

// adminAuthed 判断请求是否带着有效会话。
func (h *Hub) adminAuthed(r *http.Request) bool {
	c, err := r.Cookie(adminCookie)
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return false
	}
	v, err := h.store.GetSetting(sessionKeyPrefix + sessionID(c.Value))
	if err != nil || v == "" {
		return false
	}
	exp, err := strconv.ParseInt(v, 10, 64)
	if err != nil || exp < time.Now().UnixMilli() {
		return false
	}
	return true
}

// sessionID 把会话令牌哈希成短一点的键，避免令牌本身写进数据库。
func sessionID(token string) string {
	sum := sha256.Sum256([]byte("kokoro-sess|" + token))
	return hex.EncodeToString(sum[:])[:32]
}

// pruneSessions 清掉过期的会话记录，顺手防止 settings 表被会话撑大。
func (h *Hub) pruneSessions() {
	all, err := h.store.ListSettings(sessionKeyPrefix)
	if err != nil {
		return
	}
	now := time.Now().UnixMilli()
	for k, v := range all {
		exp, err := strconv.ParseInt(v, 10, 64)
		if v == "" || err != nil || exp < now {
			_ = h.store.SetSetting(k, "")
		}
	}
	_ = now
}
