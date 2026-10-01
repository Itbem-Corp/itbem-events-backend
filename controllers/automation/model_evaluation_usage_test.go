package automation

import "testing"

func TestEvaluationCacheEvidencePreservesUnknownAndZero(t *testing.T) {
	for _, raw := range []string{`{}`, `{"input_tokens":100,"output_tokens":40}`, `{"input_tokens":100,"output_tokens":40,"cached_input_tokens":-1}`, `invalid`} {
		cache, reasoning, writes := evaluationOptionalUsage("future-provider", "future-model", raw)
		if cache != nil || reasoning != nil || writes != nil {
			t.Fatalf("absent or invalid evidence became known: %s", raw)
		}
	}
	cache, reasoning, writes := evaluationOptionalUsage("future-provider", "future-model", `{"input_tokens":100,"output_tokens":40,"cached_input_tokens":0,"reasoning_tokens":0,"cache_write_tokens":0}`)
	if cache == nil || reasoning == nil || writes == nil || *cache != 0 || *reasoning != 0 || *writes != 0 {
		t.Fatal("reported zero must remain verified")
	}
}

func TestEvaluationCacheEvidenceReadsNativeProtocols(t *testing.T) {
	for _, fixture := range []struct{ provider, usage string }{
		{"deepseek", `{"prompt_tokens":100,"completion_tokens":40,"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20,"completion_tokens_details":{"reasoning_tokens":30}}`},
		{"minimax", `{"prompt_tokens":100,"completion_tokens":40,"prompt_tokens_details":{"cached_tokens":80},"completion_tokens_details":{"reasoning_tokens":30}}`},
		{"openai", `{"input_tokens":100,"output_tokens":40,"input_tokens_details":{"cached_tokens":80},"output_tokens_details":{"reasoning_tokens":30}}`},
	} {
		cache, reasoning, writes := evaluationOptionalUsage(fixture.provider, "future-model", fixture.usage)
		if cache == nil || reasoning == nil || writes != nil || *cache != 80 || *reasoning != 30 {
			t.Fatalf("native protocol evidence lost: %s", fixture.provider)
		}
	}
}
