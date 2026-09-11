package wire

import (
	"strings"

	"github.com/tidwall/gjson"
	"golang.org/x/net/http/httpguts"
)

// routingHintFromBody mirrors the official client: "model=<model>" with an
// optional ";tier=<tier>" for explicit fast tiers. Observed on the wire from
// codex-cli 0.154.0 as `x-codex-routing-hint: model=gpt-6-astra`.
func routingHintFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	fields := gjson.GetManyBytes(body, "model", "service_tier")
	return routingHint(fields[0].String(), fields[1].String())
}

func routingHint(model, serviceTier string) string {
	model = strings.TrimSpace(model)
	if model == "" || strings.ContainsAny(model, ";=") {
		return ""
	}
	hint := "model=" + model
	switch tier := strings.ToLower(strings.TrimSpace(serviceTier)); tier {
	case "priority", "flex", "ultrafast":
		hint += ";tier=" + tier
	}
	if !httpguts.ValidHeaderFieldValue(hint) {
		return ""
	}
	return hint
}
