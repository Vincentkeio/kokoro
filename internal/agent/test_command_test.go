package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

func TestTestToolAvailableWithoutNetwork(t *testing.T) {
	// No Hub/client is configured. The reviewed script must still be available,
	// so a route test cannot stall while downloading an old remote script.
	tool, err := (&Agent{}).fetchTestTool(context.Background())
	if err != nil || len(tool) == 0 {
		t.Fatalf("embedded test tool unavailable: %v", err)
	}
}

func TestPinnedTestToolMatchesBundledScript(t *testing.T) {
	data, err := os.ReadFile("../../scripts/kokoro-bench.sh")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != testToolSHA256 {
		t.Fatal("Agent pin does not match the Hub's bundled testing tool")
	}
}

func TestFamilyRecipesRemainFixed(t *testing.T) {
	for kind, only := range map[string]string{"ipquality": "ip", "ipquality4": "ip4", "ipquality6": "ip6"} {
		if recipe, ok := testRecipes[kind]; !ok || recipe.only != only {
			t.Fatalf("%s: %+v", kind, recipe)
		}
	}
}
