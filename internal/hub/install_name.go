package hub

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

func normalizedInstallName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > 128 {
		return "", fmt.Errorf("节点名称最多 128 个字符")
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return "", fmt.Errorf("节点名称不能包含换行或控制字符")
		}
	}
	return value, nil
}

func shellLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
