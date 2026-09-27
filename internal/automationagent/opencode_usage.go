package automationagent

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const openCodeUsageExportURL = "https://opencode.ai/console/api/v1/usage/export"

type OpenCodeUsageWindow struct {
	Name              string    `json:"name"`
	LimitMicrousd     int64     `json:"limit_microusd"`
	UsedMicrousd      int64     `json:"used_microusd"`
	RemainingMicrousd int64     `json:"remaining_microusd"`
	StartsAt          time.Time `json:"starts_at"`
}

type OpenCodeUsage struct {
	RefreshedAt time.Time             `json:"refreshed_at"`
	Windows     []OpenCodeUsageWindow `json:"windows"`
}

// FetchOpenCodeUsage exports read-only Console usage with a service-account
// key. Go limits are dollar-denominated; costs are only summed for OpenCode Go
// provider rows so unrelated BYOK/managed usage never looks like Go quota use.
func FetchOpenCodeUsage(ctx context.Context, apiKey string, client *http.Client, now time.Time) (OpenCodeUsage, error) {
	if strings.TrimSpace(apiKey) == "" {
		return OpenCodeUsage{}, fmt.Errorf("OpenCode usage credential is unavailable")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	now = now.UTC()
	definitions := []struct {
		name      string
		rangeName string
		limit     int64
		start     time.Time
	}{
		{"5 horas", "24h", 12_000_000, now.Add(-5 * time.Hour)},
		{"7 días", "7d", 30_000_000, now.Add(-7 * 24 * time.Hour)},
		{"30 días", "30d", 60_000_000, now.Add(-30 * 24 * time.Hour)},
	}
	result := OpenCodeUsage{RefreshedAt: now, Windows: make([]OpenCodeUsageWindow, 0, len(definitions))}
	for _, definition := range definitions {
		rows, err := fetchOpenCodeUsageRows(ctx, apiKey, client, definition.rangeName)
		if err != nil {
			return OpenCodeUsage{}, err
		}
		used := int64(0)
		for _, row := range rows {
			if row.At.Before(definition.start) || !isOpenCodeGoUsageProvider(row.Provider) {
				continue
			}
			used += row.CostMicrousd
		}
		if used > definition.limit {
			used = definition.limit
		}
		result.Windows = append(result.Windows, OpenCodeUsageWindow{Name: definition.name, LimitMicrousd: definition.limit, UsedMicrousd: used, RemainingMicrousd: definition.limit - used, StartsAt: definition.start})
	}
	return result, nil
}

type openCodeUsageRow struct {
	At           time.Time
	Provider     string
	CostMicrousd int64
}

func fetchOpenCodeUsageRows(ctx context.Context, apiKey string, client *http.Client, rangeName string) ([]openCodeUsageRow, error) {
	endpoint, _ := url.Parse(openCodeUsageExportURL)
	query := endpoint.Query()
	query.Set("scope", "organization")
	query.Set("range", rangeName)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("OpenCode usage request failed")
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	req.Header.Set("Accept", "text/csv")
	response, err := client.Do(req)
	if err != nil || response == nil {
		return nil, fmt.Errorf("OpenCode usage export is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("OpenCode usage export is unavailable")
	}
	reader := csv.NewReader(io.LimitReader(response.Body, 8<<20))
	headers, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("OpenCode usage export is invalid")
	}
	columns := make(map[string]int, len(headers))
	for index, header := range headers {
		columns[strings.TrimSpace(header)] = index
	}
	for _, required := range []string{"provider", "cost_micro_cents", "created_at"} {
		if _, ok := columns[required]; !ok {
			return nil, fmt.Errorf("OpenCode usage export is invalid")
		}
	}
	rows := make([]openCodeUsageRow, 0)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("OpenCode usage export is invalid")
		}
		if len(record) != len(headers) {
			return nil, fmt.Errorf("OpenCode usage export is invalid")
		}
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(record[columns["created_at"]]))
		if err != nil {
			continue
		}
		microCents, err := strconv.ParseInt(strings.TrimSpace(record[columns["cost_micro_cents"]]), 10, 64)
		if err != nil || microCents < 0 {
			continue
		}
		rows = append(rows, openCodeUsageRow{At: at.UTC(), Provider: strings.ToLower(strings.TrimSpace(record[columns["provider"]])), CostMicrousd: microCents / 100})
	}
	return rows, nil
}

func isOpenCodeGoUsageProvider(provider string) bool {
	return provider == "opencode" || provider == "opencode-go"
}
