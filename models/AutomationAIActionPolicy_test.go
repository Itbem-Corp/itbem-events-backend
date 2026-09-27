package models

import "testing"

func TestAutomationAIActionPolicyRoutesUsesLegacyPrimaryAndBoundsChains(t *testing.T) {
	legacy, err := (AutomationAIActionPolicy{Provider: "minimax", Model: "MiniMax-M3"}).Routes()
	if err != nil || len(legacy) != 1 || legacy[0].Provider != "minimax" {
		t.Fatalf("legacy policy routes=%#v err=%v", legacy, err)
	}
	policy := AutomationAIActionPolicy{RoutesJSON: `[{"provider":"opencode-go","model":"kimi-k2.7-code"},{"provider":"opencode-go","model":"glm-5.3-flash"},{"provider":"minimax","model":"MiniMax-M3"}]`}
	routes, err := policy.Routes()
	if err != nil || len(routes) != MaxAutomationAIActionRoutes || routes[2].Model != "MiniMax-M3" {
		t.Fatalf("fallback policy routes=%#v err=%v", routes, err)
	}
	policy.RoutesJSON = `[{},{},{},{}]`
	if _, err := policy.Routes(); err == nil {
		t.Fatal("more than three routes must be rejected")
	}
	policy.RoutesJSON = `[{"provider":"opencode-go","model":""}]`
	if _, err := policy.Routes(); err == nil {
		t.Fatal("an incomplete fallback route must be rejected")
	}
}
