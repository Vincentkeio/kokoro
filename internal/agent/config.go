package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// 配置文件相关常量。
const (
	// DefaultConfigPath 是默认配置文件路径。
	DefaultConfigPath = "/etc/kokoro/config.toml"
	// ConfigEnv 用于覆盖配置文件路径。
	ConfigEnv = "KOKORO_CONFIG"
	// InstallTokenEnv 注册用的一次性安装令牌（配置文件里的 install_token 优先级更低）。
	InstallTokenEnv = "KOKORO_INSTALL_TOKEN"
)

// configKeyOrder 决定回写配置文件时的键顺序，便于人工阅读。
var configKeyOrder = []string{
	"hub",
	"token",
	"install_token",
	"interval_ms",
	"ca_fingerprint",
	"node_name",
	"insecure",
}

// ConfigPath 返回配置文件路径：环境变量 KOKORO_CONFIG 优先，否则用默认路径。
func ConfigPath() string {
	if p := strings.TrimSpace(os.Getenv(ConfigEnv)); p != "" {
		return p
	}
	return DefaultConfigPath
}

// LoadConfig 从 TOML 风格配置文件加载 agent 配置。path 为空时使用 ConfigPath()。
//
// 解析器是自实现的极简版：只支持扁平的 `key = value`（节名忽略、`#` 开头为注释），
// 目的是不引入第三方依赖、把 agent 二进制压到最小。
func LoadConfig(path string) (*model.AgentConfig, error) {
	if strings.TrimSpace(path) == "" {
		path = ConfigPath()
	}
	raw, err := loadRaw(path)
	if err != nil {
		return nil, err
	}
	cfg := &model.AgentConfig{IntervalMS: defaultIntervalMS}
	applyRaw(cfg, raw)
	if strings.TrimSpace(cfg.Hub) == "" {
		return nil, fmt.Errorf("配置文件 %s 缺少 hub 地址", path)
	}
	return cfg, nil
}

// loadRaw 读取并解析配置文件，返回扁平的键值表。
func loadRaw(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件 %s 失败：%w", path, err)
	}
	return parseFlatTOML(string(b)), nil
}

// applyRaw 把键值表套用到 AgentConfig 上（未知键忽略）。
func applyRaw(cfg *model.AgentConfig, raw map[string]string) {
	if v, ok := raw["hub"]; ok {
		cfg.Hub = strings.TrimSpace(v)
	}
	if v, ok := raw["token"]; ok {
		cfg.Token = strings.TrimSpace(v)
	}
	if v, ok := raw["interval_ms"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			cfg.IntervalMS = n
		}
	}
	if v, ok := raw["ca_fingerprint"]; ok {
		cfg.CAFingerprint = strings.TrimSpace(v)
	}
	if v, ok := raw["node_name"]; ok {
		cfg.NodeName = strings.TrimSpace(v)
	}
	if v, ok := raw["insecure"]; ok {
		cfg.Insecure = parseBool(v)
	}
	if v, ok := raw["node_id"]; ok {
		cfg.NodeID = strings.TrimSpace(v)
	}
	if v, ok := raw["netq_interval_min"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			cfg.NetQIntervalMin = n
		}
	}
}

// parseFlatTOML 解析扁平 TOML：忽略节名 `[xxx]`、忽略 `#` 注释，
// 支持双引号/单引号包裹的字符串。
func parseFlatTOML(s string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimSpace(stripComment(line))
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if key == "" {
			continue
		}
		out[key] = unquote(val)
	}
	return out
}

// stripComment 去掉行尾注释（引号内的 # 不算注释）。
func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && c == '#':
			return s[:i]
		}
	}
	return s
}

// unquote 去掉成对的引号并处理常见转义。
func unquote(s string) string {
	if len(s) >= 2 {
		q := s[0]
		if (q == '"' || q == '\'') && s[len(s)-1] == q {
			body := s[1 : len(s)-1]
			if q == '"' {
				body = strings.NewReplacer(
					`\n`, "\n", `\t`, "\t", `\r`, "\r", `\"`, `"`, `\\`, `\`,
				).Replace(body)
			}
			return body
		}
	}
	return s
}

// parseBool 解析布尔字面量（TOML 风格）。
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "on", "1":
		return true
	default:
		return false
	}
}

// saveConfig 原子地回写配置文件：先写同目录临时文件（0600），再 rename 覆盖。
// mutate 修改的是键值副本，配置文件不存在时从空表开始生成。
func saveConfig(path string, mutate func(map[string]string)) error {
	raw := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		raw = parseFlatTOML(string(b))
	}
	mutate(raw)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录 %s 失败：%w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".kokoro-config-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败：%w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 成功 rename 后此处删除的是已不存在的路径，无害

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置临时文件权限失败：%w", err)
	}
	if _, err := tmp.WriteString(renderConfig(raw)); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败：%w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("原子替换配置文件失败：%w", err)
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

// renderConfig 把键值表渲染成扁平 TOML 文本。
func renderConfig(raw map[string]string) string {
	var b strings.Builder
	b.WriteString("# kokoro agent 配置（由 agent 自动维护，注释不会被保留）\n")
	used := make(map[string]bool, len(configKeyOrder))
	for _, k := range configKeyOrder {
		v, ok := raw[k]
		if !ok {
			continue
		}
		used[k] = true
		b.WriteString(formatKV(k, v))
	}
	rest := make([]string, 0, len(raw))
	for k := range raw {
		if !used[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		b.WriteString(formatKV(k, raw[k]))
	}
	return b.String()
}

// formatKV 按已知字段的类型渲染一行 `key = value`。
func formatKV(k, v string) string {
	switch k {
	case "interval_ms":
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return fmt.Sprintf("%s = %d\n", k, n)
		}
	case "insecure":
		return fmt.Sprintf("%s = %t\n", k, parseBool(v))
	}
	return fmt.Sprintf("%s = %s\n", k, strconv.Quote(v))
}
