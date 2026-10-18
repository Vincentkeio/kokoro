package panelupdate

import (
	"os"
	"reflect"
)

// Preserve status-file ownership when a root updater rewrites files written
// by the unprivileged Hub service.
func preserveOwner(src, dst string) error {
	info, e := os.Stat(src)
	if e != nil {
		return nil
	}
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.IsValid() && v.Kind() == reflect.Struct {
		u, g := v.FieldByName("Uid"), v.FieldByName("Gid")
		if u.IsValid() && g.IsValid() && u.CanUint() && g.CanUint() {
			return os.Chown(dst, int(u.Uint()), int(g.Uint()))
		}
	}
	return nil
}
