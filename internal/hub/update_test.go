package hub

import (
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// 探针更新只算在线节点。迁移到新面板后旧面板还挂着全部节点记录，
// 如果按"登记数量"统计，就会永远显示"N 台待更新"但一台也不动。
func TestNodeConsideredOnline(t *testing.T) {
	now := time.Now().UnixMilli()
	for _, tc := range []struct {
		name string
		node model.Node
		want bool
	}{
		{"刚上报过", model.Node{Online: true, LastSeen: now - 5_000}, true},
		{"标记在线但早已失联", model.Node{Online: true, LastSeen: now - 60_000}, false},
		{"从未上报", model.Node{Online: true, LastSeen: 0}, false},
		{"明确离线", model.Node{Online: false, LastSeen: now - 5_000}, false},
		{"时钟略超前", model.Node{Online: true, LastSeen: now + 5_000}, true},
	} {
		if got := nodeConsideredOnline(tc.node, now); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
