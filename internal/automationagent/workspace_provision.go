package automationagent

import (
	"context"
	"fmt"
	"os"
	"sort"
)

// ProvisionRegisteredWorkspaces is safe to repeat on unattended startup. Only
// absent managed checkouts are created. Existing source, task branches and local
// edits are left intact; task preparation later verifies its frozen revision.
func ProvisionRegisteredWorkspaces(ctx context.Context, lookup func(string) string) error {
	registry, err := LoadWorkspaceRegistry(ConfiguredWorkspaceRegistry(lookup))
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		workspace := registry[id]
		if _, err := os.Stat(workspace.Root); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if _, err := SyncAuthorizedManagedWorkspace(ctx, workspace, lookup); err != nil {
			return fmt.Errorf("provision registered workspace %s: %w", id, err)
		}
	}
	return nil
}
