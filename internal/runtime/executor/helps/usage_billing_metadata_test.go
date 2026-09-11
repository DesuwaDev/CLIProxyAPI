package helps

import "testing"

func TestClaudeObservedSpeedAndCacheTTLAcrossStream(t *testing.T) {
	first := ParseClaudeUsage([]byte(`{"usage":{"input_tokens":100,"output_tokens":0,"speed":"fast","cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`))
	update, ok := ParseClaudeStreamUsage([]byte(`data: {"type":"message_delta","usage":{"output_tokens":50}}`))
	if !ok {
		t.Fatal("missing stream usage")
	}
	detail := MergeStreamUsageDetail(first, update)
	if detail.ResponseServiceTier != "fast" || !detail.CacheCreationTTLObserved || detail.CacheCreation5mTokens != 10 || detail.CacheCreation1hTokens != 20 || detail.TokenBreakdown.Input.CacheWriteTokens != 30 {
		t.Fatalf("billing metadata lost: %+v", detail)
	}
	standard := ParseClaudeUsage([]byte(`{"usage":{"input_tokens":100,"output_tokens":50,"speed":"standard"}}`))
	if standard.ResponseServiceTier != "standard" {
		t.Fatal("actual speed downgrade lost")
	}
}
