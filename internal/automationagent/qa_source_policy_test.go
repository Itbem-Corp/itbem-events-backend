package automationagent

import "testing"

func TestQASourceDependencyPolicyGrantsOnlyConfiguredParent(t *testing.T) {
	for _, scenario := range []struct {
		raw      string
		valid    bool
		expected int
	}{
		{"", true, 0},
		{`{"example/service":{".contracts/contract":"example/contract"}}`, true, 1},
		{`{"other/service":{".contracts/contract":"example/contract"}}`, true, 0},
		{`{"example/service":{"../contract":"example/contract"}}`, false, 0},
		{`{"example/service":{"contract":"https://github.com/example/contract"}}`, false, 0},
		{`{"example/service":{},"EXAMPLE/SERVICE":{}}`, false, 0},
		{`{"example/service":{"contract":"example/contract?token=synthetic"}}`, false, 0},
		{`[]`, false, 0},
	} {
		policy, err := LoadQASourceDependencyPolicy(func(key string) string {
			if key != "ITBEM_QA_SOURCE_DEPENDENCIES_JSON" {
				t.Fatal("unexpected policy lookup")
			}
			return scenario.raw
		}, "example/service")
		if (err == nil) != scenario.valid || (err == nil && len(policy) != scenario.expected) {
			t.Fatalf("policy outcome differs: %v", err)
		}
	}
}
