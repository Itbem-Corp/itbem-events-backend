package automationagent

import (
	"strings"
	"testing"
)

func safeGitConfigOverrides(args []string, command string) bool {
	required := map[string]string{"credential.helper": "", "http.extraheader": "", "http.proxy": "", "http.sslverify": "true"}
	configs := map[string]string{}
	index := 0
	for index+1 < len(args) && args[index] == "-c" {
		key, value, found := strings.Cut(args[index+1], "=")
		key = strings.ToLower(key)
		if !found {
			return false
		}
		if _, duplicate := configs[key]; duplicate {
			return false
		}
		configs[key] = value
		index += 2
	}
	for key, expected := range required {
		if value, found := configs[key]; !found || value != expected {
			return false
		}
	}
	return index < len(args) && args[index] == command
}

func TestGitHubInstallationWorkspaceCommandsDisableCredentialHelpers(t *testing.T) {
	for name, args := range map[string][]string{
		"clone": gitWorkspaceCloneArguments("trunk", "https://github.com/example/repository.git", "checkout", true),
		"fetch": gitWorkspaceFetchArguments("origin", true),
	} {
		t.Run(name, func(t *testing.T) {
			if !safeGitConfigOverrides(args, name) {
				t.Fatalf("%s lacks unique safe Git credential, proxy and TLS overrides", name)
			}
			index := 0
			for index+1 < len(args) && args[index] == "-c" {
				index += 2
			}
			for _, pair := range []string{"credential.helper=synthetic-helper", "http.extraHeader=synthetic-header", "http.proxy=http://synthetic.invalid", "http.sslVerify=false", "HTTP.Proxy=http://synthetic.invalid"} {
				mutated := append([]string{}, args[:index]...)
				mutated = append(mutated, "-c", pair)
				mutated = append(mutated, args[index:]...)
				if safeGitConfigOverrides(mutated, name) {
					t.Fatalf("trailing override was not detected: %s", pair)
				}
			}
			for index := 0; index+1 < len(args) && args[index] == "-c"; index += 2 {
				mutated := append([]string{}, args[:index]...)
				mutated = append(mutated, args[index+2:]...)
				if safeGitConfigOverrides(mutated, name) {
					t.Fatal("removed config override was not detected")
				}
			}
			for _, argument := range args {
				if argument == "--recurse-submodules" {
					t.Fatal("source synchronization must not recursively acquire repository-supplied remotes")
				}
			}
		})
	}
}
