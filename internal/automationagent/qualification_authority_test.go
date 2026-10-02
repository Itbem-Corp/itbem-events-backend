package automationagent

import "testing"

func TestGitHubInstallationWorkspaceCommandsDisableCredentialHelpers(t *testing.T) {
	for name, args := range map[string][]string{
		"clone": gitWorkspaceCloneArguments("trunk", "https://github.com/example/repository.git", "checkout", true),
		"fetch": gitWorkspaceFetchArguments("origin", true),
	} {
		t.Run(name, func(t *testing.T) {
			configs := map[string]string{}
			index := 0
			for index+1 < len(args) && args[index] == "-c" {
				configs[args[index+1]] = args[index+1]
				index += 2
			}
			for _, required := range []string{"credential.helper=", "http.extraHeader=", "http.proxy=", "http.sslVerify=true"} {
				if _, found := configs[required]; !found {
					t.Fatalf("%s could inherit developer credentials, proxy or TLS overrides: missing %s", name, required)
				}
			}
			if index >= len(args) || args[index] != name {
				t.Fatalf("Git config overrides did not precede %s", name)
			}
			for _, argument := range args {
				if argument == "--recurse-submodules" {
					t.Fatal("source synchronization must not recursively acquire repository-supplied remotes")
				}
			}
		})
	}
}
