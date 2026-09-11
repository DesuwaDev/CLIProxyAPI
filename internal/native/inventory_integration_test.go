package native

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/inventory"
)

func TestNativeInventoryProfileAndWindowRoutesRemainDistinct(t *testing.T) {
	r, g := openTestRuntime(t, filepath.Join(t.TempDir(), "config.yaml"))
	r.SetInventoryLookup(func() map[string]inventory.Account {
		return map[string]inventory.Account{"account": {FileName: "fixture.json", Provider: "codex"}}
	})
	for _, scope := range []string{"credential", "client"} {
		path := "inventory/" + scope + "/account"
		requireOK(t, request(t, g, "PUT", path, `{"name":"fixture","notes":"route canary","budget_usd":20}`))
		w := request(t, g, "GET", path, "")
		requireOK(t, w)
		var result struct {
			Item inventory.Item `json:"item"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Item.Profile.Name != "fixture" || result.Item.Profile.Notes != "route canary" {
			t.Fatal("profile save was shadowed by quota route")
		}
	}
	now := time.Now().UnixMilli()
	raw, _ := json.Marshal(map[string]any{"windows": []inventory.Window{{ID: "five-hour", UsedPercent: 25, StartMS: now - 3600000, ResetMS: now + 14400000, ObservedMS: now}}})
	requireOK(t, request(t, g, "PUT", "inventory/windows/account", string(raw)))
	requireOK(t, request(t, g, "GET", "inventory/credential/account", ""))
}
