package automationagent

// ReasoningWireSettings reports the controls the adapter actually serializes,
// separately from the requested policy. "omitted" means provider defaults apply;
// it does not claim that thinking is off. No prompt, key or reasoning text is read.
func ReasoningWireSettings(client ProviderClient) (mode, effort string) {
	if provider, ok := client.(*httpProviderClient); ok {
		payload, _ := provider.payload(nil, 1)
		get := func(raw any, key string) string {
			switch values := raw.(type) {
			case map[string]string:
				return values[key]
			case map[string]any:
				value, _ := values[key].(string)
				return value
			}
			return ""
		}
		if thinking := get(payload["thinking"], "type"); thinking == "enabled" || thinking == "disabled" {
			effort, _ = payload["reasoning_effort"].(string)
			return thinking, effort
		}
		if reasoning, ok := payload["reasoning"].(map[string]any); ok {
			if enabled, present := reasoning["enabled"].(bool); present {
				effort = get(reasoning, "effort")
				if enabled {
					return "enabled", effort
				}
				return "disabled", effort
			}
		}
		if effort = get(payload["reasoning"], "effort"); effort != "" {
			return "effort", effort
		}
		return "omitted", ""
	}
	return "unknown", ""
}
