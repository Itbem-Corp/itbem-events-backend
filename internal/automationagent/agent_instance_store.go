package automationagent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/uuid"
)

func (identity MachineIdentity) agentInstanceIDPath(agentKey string) (string, error) {
	if !runtimeAgentKeyPattern.MatchString(strings.TrimSpace(agentKey)) || !filepath.IsAbs(identity.stateDirectory) {
		return "", errors.New("agent instance storage is unavailable")
	}
	return filepath.Join(identity.stateDirectory, "agent-instance-"+strings.TrimSpace(agentKey)), nil
}

// RegisteredAgentInstanceID loads the ID returned by the control plane for
// this machine and logical profile. It is stored beside, but separately from,
// the protected Ed25519 identity so replacing the API-issued ID never rewrites
// private key material.
func (identity MachineIdentity) RegisteredAgentInstanceID(agentKey string) (string, error) {
	path, err := identity.agentInstanceIDPath(agentKey)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64 {
		return "", errors.New("persisted agent instance identity is invalid")
	}
	if err := secureMachineIdentityFile(path); err != nil {
		return "", errors.New("could not secure persisted agent instance identity")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("could not read persisted agent instance identity")
	}
	raw := strings.TrimSpace(string(contents))
	parsed, err := uuid.FromString(raw)
	if err != nil || parsed == uuid.Nil || parsed.String() != raw {
		return "", errors.New("persisted agent instance identity is invalid")
	}
	return parsed.String(), nil
}

// StoreRegisteredAgentInstanceID persists only the opaque ID returned by the
// authenticated enrollment endpoint. The protected machine key is not copied
// to a second file, and Unix/Windows helpers enforce owner-only access.
func (identity MachineIdentity) StoreRegisteredAgentInstanceID(agentKey, instanceID string) error {
	path, err := identity.agentInstanceIDPath(agentKey)
	if err != nil {
		return err
	}
	parsed, err := uuid.FromString(strings.TrimSpace(instanceID))
	if err != nil || parsed == uuid.Nil || parsed.String() != strings.TrimSpace(instanceID) {
		return errors.New("control plane returned an invalid agent instance ID")
	}
	if existing, err := identity.RegisteredAgentInstanceID(agentKey); err != nil {
		return err
	} else if existing == parsed.String() {
		return nil
	}
	temporary, err := os.CreateTemp(identity.stateDirectory, ".agent-instance-*")
	if err != nil {
		return errors.New("could not persist agent instance identity")
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := secureMachineIdentityFile(temporaryPath); err != nil {
		_ = temporary.Close()
		return errors.New("could not secure agent instance identity")
	}
	if _, err := temporary.WriteString(parsed.String() + "\n"); err != nil {
		_ = temporary.Close()
		return errors.New("could not persist agent instance identity")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errors.New("could not persist agent instance identity")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("could not persist agent instance identity")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return errors.New("could not atomically persist agent instance identity")
	}
	if err := syncMachineIdentityDirectory(identity.stateDirectory); err != nil {
		return errors.New("could not durably persist agent instance identity")
	}
	return nil
}
