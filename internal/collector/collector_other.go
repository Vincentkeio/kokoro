//go:build !linux

package collector

import (
	"errors"

	"github.com/kokoro-probe/kokoro/internal/model"
)

// errUnsupported 是非 Linux 平台所有采集项的统一返回值。
var errUnsupported = errors.New("collector: unsupported platform")

// newCollector 只支持 Linux，其他平台直接报错。
func newCollector() (Collector, error) { return nil, errUnsupported }

func readCPUTicks() (cpuTicks, error) { return cpuTicks{}, errUnsupported }

func readIfaceCounters() ([]ifaceCounters, error) { return nil, errUnsupported }

func readDiskIOCounters() (int64, int64, error) { return 0, 0, errUnsupported }

func MemStat() (model.MemStat, error) { return model.MemStat{}, errUnsupported }

func DiskStat() (model.DiskStat, []model.PartStat, error) {
	return model.DiskStat{}, nil, errUnsupported
}

func LoadAvg() (float64, float64, float64, error) { return 0, 0, 0, errUnsupported }

func Uptime() (int64, error) { return 0, errUnsupported }

func ProcCount() (int, error) { return 0, errUnsupported }

func ConnCount() (int, int, error) { return 0, 0, errUnsupported }

func Temps() (map[string]float64, error) { return nil, errUnsupported }

func HostInfo() (hostname, osName, kernel, arch, virt string, err error) {
	return "", "", "", "", "", errUnsupported
}
