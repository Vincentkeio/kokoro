package theme

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ThemeMotion struct {
	Duration   string        `json:"duration,omitempty"`
	Easing     string        `json:"easing,omitempty"`
	Direction  string        `json:"direction,omitempty"`
	Iterations int           `json:"iterations,omitempty"` // 0 = infinite
	Frames     []MotionFrame `json:"frames"`
}

type MotionFrame struct {
	At    int               `json:"at"`
	Style map[string]string `json:"style"`
}

var motionName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var motionProperties = map[string]bool{
	"transform": true, "opacity": true, "background-position": true, "background-size": true,
	"background-color": true, "color": true, "border-color": true, "box-shadow": true,
	"text-shadow": true, "stroke-dashoffset": true,
}

func validMotionDuration(value string) bool {
	d, err := time.ParseDuration(value)
	return err == nil && isTimeValue(value) && d >= 2*time.Second && d <= time.Minute
}

func ValidateMotions(motions map[string]ThemeMotion) error {
	if len(motions) > 8 {
		return fmt.Errorf("layout.motions: 最多 8 组自定义动效")
	}
	for name, motion := range motions {
		if !motionName.MatchString(name) {
			return fmt.Errorf("layout.motions: 动效名称只允许 1–32 位小写字母、数字、连字符，以字母开头")
		}
		if motion.Duration != "" && !validMotionDuration(motion.Duration) {
			return fmt.Errorf("layout.motions.%s: 周期必须为 2s–60s", name)
		}
		if motion.Easing != "" && motion.Easing != "linear" && motion.Easing != "ease" && motion.Easing != "ease-in" && motion.Easing != "ease-out" && motion.Easing != "ease-in-out" {
			return fmt.Errorf("layout.motions.%s: easing 取值非法", name)
		}
		if motion.Direction != "" && motion.Direction != "normal" && motion.Direction != "reverse" && motion.Direction != "alternate" && motion.Direction != "alternate-reverse" {
			return fmt.Errorf("layout.motions.%s: direction 取值非法", name)
		}
		if motion.Iterations < 0 || motion.Iterations > 20 {
			return fmt.Errorf("layout.motions.%s: iterations 只允许 0–20，0 为循环", name)
		}
		if len(motion.Frames) < 2 || len(motion.Frames) > 12 {
			return fmt.Errorf("layout.motions.%s: 必须有 2–12 帧", name)
		}
		last := -1
		for _, frame := range motion.Frames {
			if frame.At < 0 || frame.At > 100 || frame.At <= last {
				return fmt.Errorf("layout.motions.%s: at 必须在 0–100 内严格递增", name)
			}
			last = frame.At
			if len(frame.Style) == 0 || len(frame.Style) > 8 {
				return fmt.Errorf("layout.motions.%s: 每帧需有 1–8 个属性", name)
			}
			for property, value := range frame.Style {
				if !motionProperties[property] {
					return fmt.Errorf("layout.motions.%s: %s 不允许用于关键帧", name, property)
				}
				if strings.Contains(value, "!") {
					return fmt.Errorf("layout.motions.%s: 不允许 !important", name)
				}
				if err := validateTokenValue(property, value, visualProperties[property]); err != nil {
					return fmt.Errorf("layout.motions.%s.%s: %w", name, property, err)
				}
			}
		}
		if motion.Frames[0].At != 0 || motion.Frames[len(motion.Frames)-1].At != 100 {
			return fmt.Errorf("layout.motions.%s: 必须包含 0 和 100 两个端点", name)
		}
	}
	return nil
}

func customMotionName(m *Manifest, name string) string {
	return "kokoro-custom-" + strings.NewReplacer(".", "-", "_", "-").Replace(m.ID) + "-" + name
}

func renderMotionDeclaration(m *Manifest, properties map[string]string) string {
	motion := properties["motion"]
	name := "kokoro-theme-" + motion
	duration, easing, direction, iterations := "6s", "ease-in-out", "alternate", "infinite"
	if strings.HasPrefix(motion, "custom:") {
		key := strings.TrimPrefix(motion, "custom:")
		definition := m.Layout.Motions[key]
		name = customMotionName(m, key)
		if definition.Duration != "" {
			duration = definition.Duration
		}
		if definition.Easing != "" {
			easing = definition.Easing
		}
		if definition.Direction != "" {
			direction = definition.Direction
		}
		if definition.Iterations > 0 {
			iterations = strconv.Itoa(definition.Iterations)
		}
	}
	if value := properties["motion-duration"]; value != "" {
		duration = value
	}
	distance := properties["motion-distance"]
	if distance == "" {
		distance = "4px"
	}
	return fmt.Sprintf("--k-visual-distance:clamp(0px,%s,12px);animation:%s %s %s %s %s both;", distance, name, duration, easing, iterations, direction)
}

func renderMotionOverrides(properties map[string]string) string {
	if properties["motion"] != "" {
		return ""
	}
	var out strings.Builder
	if duration := properties["motion-duration"]; duration != "" {
		fmt.Fprintf(&out, "animation-duration:%s;", duration)
	}
	if distance := properties["motion-distance"]; distance != "" {
		fmt.Fprintf(&out, "--k-visual-distance:clamp(0px,%s,12px);", distance)
	}
	return out.String()
}

func renderMotionCSS(m *Manifest) string {
	var out strings.Builder
	names := make([]string, 0, len(m.Layout.Motions))
	for name := range m.Layout.Motions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&out, "@keyframes %s{", customMotionName(m, name))
		for _, frame := range m.Layout.Motions[name].Frames {
			fmt.Fprintf(&out, "%d%%{", frame.At)
			keys := make([]string, 0, len(frame.Style))
			for key := range frame.Style {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				fmt.Fprintf(&out, "%s:%s;", key, rewriteAssetURL(frame.Style[key], m.ID))
			}
			out.WriteString("}")
		}
		out.WriteString("}\n")
	}
	return out.String()
}

func visualBreakpoint(name string) string {
	if name == "mobile" {
		return "(max-width:640px)"
	}
	return "(min-width:641px) and (max-width:1024px)"
}
