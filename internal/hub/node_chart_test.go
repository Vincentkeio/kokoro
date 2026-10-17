package hub

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestNodeMemoryChartUsesRAMCapacity(t *testing.T) {
	for _, reported := range []int64{487272448, 0} {
		t.Run(strconv.FormatInt(reported, 10), func(t *testing.T) {
			h, st := newTestHub(t)
			n := mkNode(t, st, "内存刻度节点", "JP", "日本", "")
			points := make([]*model.Metrics, 2)
			for i := range points {
				points[i] = &model.Metrics{V: 1, Ts: time.Now().Add(time.Duration(i-1) * time.Minute).UnixMilli(),
					Mem: model.MemStat{Total: reported, Used: 160 << 20, SwapTotal: 256 << 20}}
			}
			if err := st.InsertMetricsBatch(n.ID, points); err != nil {
				t.Fatal(err)
			}
			body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/n/"+n.Slug, nil))
			want := reported
			if want == 0 {
				want = n.MemTotal
			}
			if !strings.Contains(body, `data-series="mem" data-scale-max="`+strconv.FormatInt(want, 10)+`"`) {
				t.Fatalf("内存图没有使用 RAM 容量 %d", want)
			}
		})
	}
}
