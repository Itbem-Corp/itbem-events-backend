package automationagent

import (
	"bytes"
	"encoding/json"
	"fmt"

	"events-stocks/internal/evidencejson"
)

type SummaryDecisionClaim struct {
	Index    *int   `json:"index"`
	GateID   string `json:"gate_id"`
	Kind     string `json:"kind"`
	Decision string `json:"decision"`
}

// ValidateSummaryDecisionClaims checks model-produced claims against the ordered
// gate snapshot. It does not infer approval from prose or judge narrative truth.
func ValidateSummaryDecisionClaims(content string, delivery json.RawMessage) ([]SummaryDecisionClaim, error) {
	if len(content) == 0 || len(content) > 65536 {
		return nil, fmt.Errorf("summary claims input size is invalid")
	}
	if err := evidencejson.Validate([]byte(content)); err != nil {
		return nil, err
	}
	if err := evidencejson.Validate(delivery); err != nil {
		return nil, err
	}
	var envelope struct {
		Technical struct {
			Claims json.RawMessage `json:"decision_claims"`
		} `json:"technical"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return nil, err
	}
	var input struct {
		Gates []struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			Decision string `json:"decision"`
		} `json:"gates"`
	}
	if err := json.Unmarshal(delivery, &input); err != nil {
		return nil, err
	}
	if len(input.Gates) == 0 && len(envelope.Technical.Claims) == 0 {
		return nil, nil
	}
	var claims struct {
		SchemaVersion int                    `json:"schema_version"`
		Gates         []SummaryDecisionClaim `json:"gates"`
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Technical.Claims))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		return nil, fmt.Errorf("summary requires explicit decision claims: %w", err)
	}
	if claims.SchemaVersion != 1 || claims.Gates == nil || len(claims.Gates) != len(input.Gates) {
		return nil, fmt.Errorf("summary decision claims must cover the complete gate snapshot")
	}
	seen := map[int]bool{}
	for _, claim := range claims.Gates {
		if claim.Index == nil || *claim.Index < 0 || *claim.Index >= len(input.Gates) || seen[*claim.Index] {
			return nil, fmt.Errorf("summary decision claim index is missing, unknown or duplicated")
		}
		seen[*claim.Index] = true
		gate := input.Gates[*claim.Index]
		if gate.Kind == "" || gate.Decision == "" || claim.GateID != gate.ID || claim.Kind != gate.Kind || claim.Decision != gate.Decision {
			return nil, fmt.Errorf("summary decision claim does not match its recorded gate")
		}
	}
	return claims.Gates, nil
}
