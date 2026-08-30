package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func systemdAsset(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{"..", "..", "deploy", "systemd"}, parts...)...)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestSystemdUnitFailsClosedAndRunsUnprivileged(t *testing.T) {
	unit := systemdAsset(t, "itbem-ai-agent@.service")
	for _, required := range []string{
		"User=itbem-agent-%i", "EnvironmentFile=/etc/itbem-ai-agent/roles/%i.env",
		"ExecCondition=/usr/bin/test ! -e /etc/itbem-ai-agent/disabled/all",
		"ExecCondition=/usr/bin/test ! -e /etc/itbem-ai-agent/disabled/%i",
		"ExecStartPre=/opt/itbem-ai-agent/current/itbem-ai-agent --doctor",
		"NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=yes",
		"CapabilityBoundingSet=", "Restart=on-failure",
	} {
		if !strings.Contains(unit, required) {
			t.Fatalf("systemd unit lost %q", required)
		}
	}
	for _, prohibited := range []string{"User=root", "Restart=always", "Environment=MINIMAX_API_KEY", "Environment=ITBEM_GITHUB_APP_PRIVATE_KEY"} {
		if strings.Contains(unit, prohibited) {
			t.Fatalf("systemd unit contains unsafe setting %q", prohibited)
		}
	}
}

func TestSystemdRoleFilesBindExactLaneAndSeparateReleaseSecrets(t *testing.T) {
	roles := map[string][2]string{
		"orchestration": {"orchestrator", "orchestration"},
		"engineering":   {"principal_engineer", "engineering"},
		"review":        {"reviewer", "review"},
		"qa":            {"qa", "qa"},
		"release":       {"release_manager", "release"},
	}
	for file, identity := range roles {
		body := systemdAsset(t, "roles", file+".env.example")
		for _, required := range []string{"ITBEM_AI_ROLE=" + identity[0], "ITBEM_AI_QUEUE_LANE=" + identity[1], "itbem-ai-local-prod-" + identity[1], "AWS_SHARED_CREDENTIALS_FILE=/etc/itbem-ai-agent/secrets/" + file + "/aws-credentials", "AUTOMATION_CALLBACK_SECRET="} {
			if !strings.Contains(body, required) {
				t.Fatalf("%s role file lost %q", file, required)
			}
		}
		if file == "release" {
			if strings.Contains(body, "API_KEY") || !strings.Contains(body, "ITBEM_GITHUB_APP_PRIVATE_KEY_FILE=") {
				t.Fatal("release role mixed model and publication secrets")
			}
		} else if !strings.Contains(body, "MINIMAX_API_KEY=") || strings.Contains(body, "GITHUB_APP_PRIVATE_KEY") {
			t.Fatalf("%s role has the wrong secret class", file)
		}
	}
	common := systemdAsset(t, "common.env.example")
	for _, secret := range []string{"API_KEY", "PRIVATE_KEY", "CALLBACK_SECRET"} {
		if strings.Contains(common, secret) {
			t.Fatalf("common environment contains role secret class %q", secret)
		}
	}
}

func TestSystemdInstallerStagesButNeverActivatesServices(t *testing.T) {
	installer := systemdAsset(t, "install.sh")
	for _, required := range []string{"useradd --system", "usermod -a -G itbem-agent-workspaces", "install -m 0600", "systemctl daemon-reload"} {
		if !strings.Contains(installer, required) {
			t.Fatalf("installer lost %q", required)
		}
	}
	for _, prohibited := range []string{"systemctl start", "systemctl enable", "enable --now", "chmod 777"} {
		if strings.Contains(installer, prohibited) {
			t.Fatalf("installer unexpectedly activates or weakens a service: %q", prohibited)
		}
	}
}
