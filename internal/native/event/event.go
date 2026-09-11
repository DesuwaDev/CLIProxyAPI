// Package event adapts the upstream usage contract into a bounded, secret-free record.
package event

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// Event represents one provider attempt, not necessarily one downstream request.
type Event struct {
	CacheWrite5m          int64                `json:"cache_write_5m_tokens,omitempty"`
	CacheWrite1h          int64                `json:"cache_write_1h_tokens,omitempty"`
	CacheWriteTTLObserved bool                 `json:"cache_write_ttl_observed,omitempty"`
	ID                    string               `json:"id"`
	TraceID               string               `json:"trace_id,omitempty"`
	ReasoningEffort       string               `json:"reasoning_effort,omitempty"`
	RequestedTier         string               `json:"requested_tier,omitempty"`
	ResponseTier          string               `json:"response_tier,omitempty"`
	UpstreamRequestID     string               `json:"upstream_request_id,omitempty"`
	LatencyObserved       bool                 `json:"latency_observed"`
	TTFTObservedMS        *int64               `json:"ttft_observed_ms"`
	RequestID             string               `json:"request_id"`
	TimestampMS           int64                `json:"timestamp_ms"`
	Provider              string               `json:"provider"`
	Model                 string               `json:"model"`
	Alias                 string               `json:"alias"`
	Account               string               `json:"account"`
	KeyHash               string               `json:"key_hash"`
	Endpoint              string               `json:"endpoint"`
	ServiceTier           string               `json:"service_tier"`
	Stream                bool                 `json:"stream"`
	Generate              bool                 `json:"generate"`
	LatencyMS             int64                `json:"latency_ms"`
	TTFTMS                int64                `json:"ttft_ms"`
	Failed                bool                 `json:"failed"`
	StatusCode            int                  `json:"status_code"`
	FailureCode           string               `json:"failure_code"`
	Tokens                usage.TokenBreakdown `json:"tokens"`
	CostUSD               *float64             `json:"cost_usd"`
	PricingRule           string               `json:"pricing_rule,omitempty"`
}

func KeyHash(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func FromUsage(ctx context.Context, record usage.Record) Event {
	if ctx == nil {
		ctx = context.Background()
	}
	at := record.RequestedAt
	if at.IsZero() {
		at = time.Now()
	}
	alias := record.Alias
	if alias == "" {
		alias = usage.RequestedModelAliasFromContext(ctx)
	}
	tier := record.ServiceTier
	if tier == "" {
		tier = record.RequestServiceTier
	}
	if tier == "" {
		tier = usage.ServiceTierFromContext(ctx)
	}
	if !strings.EqualFold(record.Provider, "codex") && record.ResponseServiceTier != "" {
		tier = record.ResponseServiceTier
	}
	requestedTier := record.ServiceTier
	if requestedTier == "" {
		requestedTier = record.RequestServiceTier
	}
	if requestedTier == "" {
		requestedTier = usage.ServiceTierFromContext(ctx)
	}
	var ttft *int64
	if record.Stream && record.TTFT > 0 {
		value := record.TTFT.Milliseconds()
		ttft = &value
	}
	headers := record.ResponseHeaders
	upstreamID := headers.Get("X-Request-ID")
	if upstreamID == "" {
		upstreamID = headers.Get("Request-ID")
	}
	if upstreamID == "" {
		upstreamID = headers.Get("OpenAI-Request-ID")
	}
	status := record.Fail.StatusCode
	if status == 0 {
		status = logging.GetResponseStatus(ctx)
	}
	failed := record.Failed || status >= 400
	if status == 0 && !failed {
		status = 200
	}
	detail := usage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	return Event{
		CacheWrite5m: record.Detail.CacheCreation5mTokens, CacheWrite1h: record.Detail.CacheCreation1hTokens, CacheWriteTTLObserved: record.Detail.CacheCreationTTLObserved,
		TraceID: logging.ObservationID(ctx), ReasoningEffort: bounded(record.ReasoningEffort), RequestedTier: bounded(requestedTier), ResponseTier: bounded(record.ResponseServiceTier), UpstreamRequestID: bounded(upstreamID), LatencyObserved: record.Latency > 0, TTFTObservedMS: ttft,
		ID: uuid.NewString(), RequestID: bounded(logging.GetRequestID(ctx)), TimestampMS: at.UnixMilli(),
		Provider: bounded(record.Provider), Model: bounded(record.Model), Alias: bounded(alias),
		Account: bounded(record.AuthIndex), KeyHash: KeyHash(record.APIKey), Endpoint: bounded(logging.GetEndpoint(ctx)),
		ServiceTier: bounded(tier), Stream: record.Stream, Generate: usage.GenerateEnabled(record.Generate),
		LatencyMS: max(0, record.Latency.Milliseconds()), TTFTMS: max(0, record.TTFT.Milliseconds()),
		Failed: failed, StatusCode: status, FailureCode: ClassifyFailure(record.Fail.Body, status, failed), Tokens: detail.TokenBreakdown,
	}
}

// ClassifyFailure retains only known machine codes. Arbitrary upstream bodies,
// headers, credentials, prompts and error messages never enter the database.
func ClassifyFailure(body string, status int, failed bool) string {
	if !failed {
		return ""
	}
	var envelope struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if len(body) <= 64*1024 {
		_ = json.Unmarshal([]byte(body), &envelope)
	}
	for _, code := range []string{envelope.Error.Code, envelope.Error.Type, envelope.Code} {
		switch strings.ToLower(code) {
		case "cyber_policy", "invalid_token", "token_expired", "token_revoked", "invalid_api_key", "invalid_grant", "account_deactivated", "workspace_deactivated", "usage_limit_reached", "insufficient_quota", "rate_limit_exceeded", "model_not_found", "permission_denied":
			return strings.ToLower(code)
		}
	}
	switch {
	case status == 401 || status == 403:
		return "authorization_review"
	case status == 429:
		return "rate_limit_review"
	case status >= 500:
		return "upstream_error"
	default:
		return "request_failed"
	}
}

func bounded(s string) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	r := []rune(s)
	if len(r) > 256 {
		return string(r[:256])
	}
	return s
}
