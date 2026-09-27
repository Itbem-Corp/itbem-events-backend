// Package agentprotocol defines the versioned wire/runtime protocols that an
// enrolled agent process can advertise to the control plane.
package agentprotocol

import "strings"

// ProtocolDeliveryPlanStepsV1 identifies the claim, lease, fencing, status,
// and evidence contract required to execute an approved delivery plan step.
const ProtocolDeliveryPlanStepsV1 = "delivery.plan_steps.v1"

// Supports returns true only when every advertised protocol is recognized,
// unique, and the requested contract is present. Unknown future values fail
// closed until this control plane explicitly implements them.
func Supports(protocols []string, required string) bool {
	if len(protocols) == 0 || strings.TrimSpace(required) != required {
		return false
	}
	known := map[string]struct{}{
		ProtocolDeliveryPlanStepsV1: {},
	}
	if _, ok := known[required]; !ok {
		return false
	}
	seen := make(map[string]struct{}, len(protocols))
	supported := false
	for _, protocol := range protocols {
		protocol = strings.TrimSpace(protocol)
		if _, ok := known[protocol]; !ok || protocol == "" {
			return false
		}
		if _, duplicate := seen[protocol]; duplicate {
			return false
		}
		seen[protocol] = struct{}{}
		if protocol == required {
			supported = true
		}
	}
	return supported
}
