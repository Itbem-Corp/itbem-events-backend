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
		"ReadWritePaths=/var/lib/itbem-ai-agent/%i /srv/itbem-agent-workspaces/%i",
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
	if strings.Contains(unit, "ReadWritePaths=/var/lib/itbem-ai-agent/%i /srv/itbem-agent-workspaces\n") {
		t.Fatal("systemd worker retained cross-lane workspace write access")
	}
}

func TestSystemdDoctorIsReadOnlyAndCannotConsumeQueueWork(t *testing.T) {
	unit := systemdAsset(t, "itbem-ai-agent-doctor@.service")
	for _, required := range []string{
		"Type=oneshot", "User=itbem-agent-%i", "EnvironmentFile=/etc/itbem-ai-agent/roles/%i.env",
		"ExecStart=/opt/itbem-ai-agent/current/itbem-ai-agent --doctor",
		"ReadOnlyPaths=/srv/itbem-agent-workspaces/%i", "RestrictAddressFamilies=AF_UNIX", "NoNewPrivileges=yes", "ProtectSystem=strict",
	} {
		if !strings.Contains(unit, required) {
			t.Fatalf("doctor unit lost %q", required)
		}
	}
	for _, prohibited := range []string{"Restart=", "WantedBy=", "ExecStart=/opt/itbem-ai-agent/current/itbem-ai-agent\n", "ReadWritePaths=/srv/itbem-agent-workspaces"} {
		if strings.Contains(unit, prohibited) {
			t.Fatalf("doctor unit can become a consumer or mutate workspaces: %q", prohibited)
		}
	}
	readonly := systemdAsset(t, "readonly-workspaces.conf")
	if !strings.Contains(readonly, "ReadOnlyPaths=/srv/itbem-agent-workspaces/%i") || strings.Contains(readonly, "ReadOnlyPaths=/srv/itbem-agent-workspaces\n") {
		t.Fatal("read-only roles are not constrained to their own lane root")
	}
}

func TestSystemdRoleFilesBindExactLaneAndSeparatePublicationSecrets(t *testing.T) {
	roles := map[string][2]string{
		"orchestration": {"orchestrator", "orchestration"},
		"engineering":   {"principal_engineer", "engineering"},
		"review":        {"reviewer", "review"},
		"qa":            {"qa", "qa"},
		"release":       {"release_manager", "release"},
	}
	for file, identity := range roles {
		body := systemdAsset(t, "roles", file+".env.example")
		for _, required := range []string{"ITBEM_AI_ROLE=" + identity[0], "ITBEM_AI_QUEUE_LANE=" + identity[1], "ITBEM_AI_QUEUE_URL=REPLACE_WITH_", "_QUEUE_URL_STACK_OUTPUT", "AWS_CONFIG_FILE=/etc/itbem-ai-agent/secrets/" + file + "/aws-config", "AWS_PROFILE=itbem-agent-" + file, "AUTOMATION_CALLBACK_SECRET=", "ITBEM_AI_WORKSPACES_JSON={}"} {
			if !strings.Contains(body, required) {
				t.Fatalf("%s role file lost %q", file, required)
			}
		}
		if strings.Contains(body, "itbem-ai-local-prod-") {
			t.Fatalf("%s role file hardcodes a deployment-specific queue", file)
		}
		if file == "release" {
			if strings.Contains(body, "API_KEY") || !strings.Contains(body, "ITBEM_GITHUB_APP_PRIVATE_KEY_FILE=") {
				t.Fatal("release role mixed model and publication secrets")
			}
		} else if file == "review" {
			if !strings.Contains(body, "MINIMAX_API_KEY=") || !strings.Contains(body, "ITBEM_GITHUB_APP_PRIVATE_KEY_FILE=/etc/itbem-ai-agent/secrets/review/github-app.pem") {
				t.Fatal("review role lost its separate model or publication secret reference")
			}
		} else if !strings.Contains(body, "MINIMAX_API_KEY=") || strings.Contains(body, "GITHUB_APP_PRIVATE_KEY") {
			t.Fatalf("%s role has the wrong secret class", file)
		}
	}
	common := systemdAsset(t, "common.env.example")
	for _, required := range []string{"AWS_EC2_METADATA_DISABLED=true", "AWS_SHARED_CREDENTIALS_FILE=/dev/null"} {
		if !strings.Contains(common, required) {
			t.Fatalf("common environment lost credential-chain guard %q", required)
		}
	}
	for _, required := range []string{"ITBEM_AI_INPUT_BUCKET=REPLACE_WITH_INPUT_BUCKET_STACK_OUTPUT", "ITBEM_AI_OUTPUT_BUCKET=REPLACE_WITH_OUTPUT_BUCKET_STACK_OUTPUT"} {
		if !strings.Contains(common, required) {
			t.Fatalf("common environment lost fail-closed stack output placeholder %q", required)
		}
	}
	for _, secret := range []string{"API_KEY", "PRIVATE_KEY", "CALLBACK_SECRET"} {
		if strings.Contains(common, secret) {
			t.Fatalf("common environment contains role secret class %q", secret)
		}
	}
	rolesAnywhere := systemdAsset(t, "roles-anywhere-aws-config.example")
	for _, required := range []string{"credential_process = /usr/local/bin/aws_signing_helper credential-process", "--trust-anchor-arn", "--profile-arn", "--role-arn"} {
		if !strings.Contains(rolesAnywhere, required) {
			t.Fatalf("Roles Anywhere template lost %q", required)
		}
	}
	for _, prohibited := range []string{"aws_access_key_id", "aws_secret_access_key", "aws_session_token", "--role-session-name"} {
		if strings.Contains(strings.ToLower(rolesAnywhere), prohibited) {
			t.Fatalf("Roles Anywhere template contains long-lived credential field %q", prohibited)
		}
	}
}

func TestSystemdInstallerStagesButNeverActivatesServices(t *testing.T) {
	installer := systemdAsset(t, "install.sh")
	for _, required := range []string{"useradd --system", "install -m 0600", "install -m 0644 \"$asset_dir/itbem-ai-agent-doctor@.service\"", "install -d -m 0711 -o root -g root /srv/itbem-agent-workspaces", "install -d -m 0700 -o \"$account\" -g \"$account\" \"/srv/itbem-agent-workspaces/$lane\"", "systemctl daemon-reload"} {
		if !strings.Contains(installer, required) {
			t.Fatalf("installer lost %q", required)
		}
	}
	for _, prohibited := range []string{"systemctl start", "systemctl enable", "enable --now", "chmod 777", "install -d -m 0770", "usermod -a -G itbem-agent-workspaces"} {
		if strings.Contains(installer, prohibited) {
			t.Fatalf("installer unexpectedly activates or weakens a service: %q", prohibited)
		}
	}
}
