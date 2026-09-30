// Package modelevaluation defines the bounded, server-owned screening contract.
// It performs no provider requests and receives no credentials.
package modelevaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"events-stocks/models"
	"events-stocks/services/automationcost"
)

const (
	MaxCases                  = 20
	MaxCalls                  = 60
	MaxCompletionTokens       = 4096
	MaxBudgetMicros     int64 = 1000000
)

type Candidate string

const (
	MiniMax  Candidate = "minimax-m3"
	DeepSeek Candidate = "deepseek-flash-high"
	Luna     Candidate = "luna-high"
)

// Route returns an allowlisted route; the caller cannot author routing fields.
func Route(candidate Candidate) (models.AutomationAIActionRoute, error) {
	switch candidate {
	case MiniMax:
		return models.AutomationAIActionRoute{Provider: "minimax", Model: "MiniMax-M3", ReasoningEnabled: true}, nil
	case DeepSeek:
		return models.AutomationAIActionRoute{Provider: "deepseek", Model: "deepseek-flash", ReasoningEnabled: true, ReasoningEffort: "high"}, nil
	case Luna:
		return models.AutomationAIActionRoute{Provider: "openrouter", Model: "openai/gpt-6-luna", ReasoningEnabled: true, ReasoningEffort: "high"}, nil
	default:
		return models.AutomationAIActionRoute{}, errors.New("evaluation candidate is not authorized")
	}
}

type Case struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}

type PlannedCall struct {
	CaseID            string
	Candidate         Candidate
	Prompt            string
	PromptSHA256      string
	Route             models.AutomationAIActionRoute
	ReservationMicros int64
}

type Plan struct {
	Calls             []PlannedCall
	ReservationMicros int64
}

// Compile is for the trusted, versioned server corpus, never request input.
// Message overhead includes the worker role contract and serialization framing.
// The normal ledger estimator uses UTF-8 bytes as a conservative token bound.
func Compile(cases []Case, instruction string, messageOverheadBytes int, pricing string) (Plan, error) {
	if len(cases) != MaxCases || strings.TrimSpace(instruction) == "" || messageOverheadBytes < 0 || messageOverheadBytes > 50000 {
		return Plan{}, errors.New("invalid evaluation corpus bounds")
	}
	plan := Plan{Calls: make([]PlannedCall, 0, MaxCalls)}
	seen := make(map[string]bool, MaxCases)
	for _, item := range cases {
		if item.ID == "" || len(item.ID) > 64 || seen[item.ID] || strings.TrimSpace(item.Prompt) == "" {
			return Plan{}, errors.New("invalid or duplicate evaluation case")
		}
		seen[item.ID] = true
		prompt := strings.TrimSpace(instruction) + "\n\n" + strings.TrimSpace(item.Prompt)
		if len(prompt) > 50000 {
			return Plan{}, errors.New("evaluation prompt exceeds bound")
		}
		for _, candidate := range []Candidate{MiniMax, DeepSeek, Luna} {
			route, err := Route(candidate)
			if err != nil {
				return Plan{}, err
			}
			reserved, err := automationcost.EstimateUpperBound(route.Provider, route.Model, len(prompt)+messageOverheadBytes, MaxCompletionTokens, pricing)
			if err != nil || reserved <= 0 {
				return Plan{}, errors.New("evaluation pricing is unavailable")
			}
			if reserved > MaxBudgetMicros-plan.ReservationMicros {
				return Plan{}, errors.New("evaluation budget cannot reserve all calls")
			}
			plan.ReservationMicros += reserved
			plan.Calls = append(plan.Calls, PlannedCall{CaseID: item.ID, Candidate: candidate, Prompt: prompt, PromptSHA256: Digest([]byte(prompt)), Route: route, ReservationMicros: reserved})
		}
	}
	return plan, nil
}

func Digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// MessageDigest covers the complete ordered role/content envelope.
func MessageDigest(messages any) (string, error) {
	encoded, err := json.Marshal(messages)
	if err != nil {
		return "", err
	}
	return Digest(encoded), nil
}
