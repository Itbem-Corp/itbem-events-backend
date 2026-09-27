package automationagent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/uuid"
)

const (
	machineIdentityDirectoryName = "itbem/ai-agent"
	machineIdentityFileName      = "machine-identity"
	legacyMachineIDFileName      = "machine-id"
	maxMachineIdentityFileSize   = 4096
)

// MachineIdentity keeps its private key package-private. Callers can inspect
// the stable machine ID and public key, but can only use the private key by
// constructing a signed callback through this package.
type MachineIdentity struct {
	machineID      string
	privateKey     ed25519.PrivateKey
	stateDirectory string
}

func (identity MachineIdentity) MachineID() string { return identity.machineID }

// String and GoString keep accidental debug formatting from dumping the
// private key held in memory. The key itself is never part of the output.
func (identity MachineIdentity) String() string {
	return fmt.Sprintf("MachineIdentity{machine_id:%q, signing_key_configured:%t}", identity.machineID, len(identity.privateKey) == ed25519.PrivateKeySize)
}

func (identity MachineIdentity) GoString() string { return identity.String() }

func (identity MachineIdentity) PublicKey() ed25519.PublicKey {
	if len(identity.privateKey) != ed25519.PrivateKeySize {
		return nil
	}
	publicKey := identity.privateKey.Public().(ed25519.PublicKey)
	return append(ed25519.PublicKey(nil), publicKey...)
}

type persistedMachineIdentity struct {
	MachineID  string `json:"machine_id"`
	PrivateKey string `json:"private_key_protected"`
}

// LoadLocalMachineIdentity resolves or creates a per-user Ed25519 identity.
// The private key is DPAPI-protected on Windows and stored with restrictive
// filesystem permissions on Unix. This function does not need a registered
// server instance ID, so it is also suitable for the enrollment display CLI.
func LoadLocalMachineIdentity(configured, stateDirectory string) (MachineIdentity, error) {
	stateDirectory = strings.TrimSpace(stateDirectory)
	if stateDirectory == "" {
		baseDirectory, err := defaultMachineIdentityBaseDirectory()
		if err != nil || strings.TrimSpace(baseDirectory) == "" {
			return MachineIdentity{}, fmt.Errorf("could not resolve private local configuration directory for machine identity")
		}
		stateDirectory = filepath.Join(baseDirectory, filepath.FromSlash(machineIdentityDirectoryName))
	} else if !filepath.IsAbs(stateDirectory) {
		return MachineIdentity{}, fmt.Errorf("ITBEM_AI_STATE_DIR must be an absolute local path")
	}
	configured = strings.TrimSpace(configured)
	if configured != "" {
		parsed, err := uuid.FromString(configured)
		if err != nil || parsed == uuid.Nil {
			return MachineIdentity{}, fmt.Errorf("ITBEM_AI_MACHINE_ID must be an opaque UUID when configured")
		}
		configured = parsed.String()
	}
	return loadOrCreateMachineIdentity(stateDirectory, configured)
}

// resolveMachineID remains as a compatibility helper for worker identity
// projections; the persisted identity now includes the signing key as well.
func resolveMachineID(configured, stateDirectory string) (string, error) {
	identity, err := LoadLocalMachineIdentity(configured, stateDirectory)
	if err != nil {
		return "", err
	}
	return identity.machineID, nil
}

func loadOrCreateMachineID(directory string) (string, error) {
	identity, err := loadOrCreateMachineIdentity(directory, "")
	if err != nil {
		return "", err
	}
	return identity.machineID, nil
}

func loadOrCreateMachineIdentity(directory, configuredID string) (MachineIdentity, error) {
	directory = filepath.Clean(directory)
	if err := secureMachineIdentityDirectory(directory); err != nil {
		return MachineIdentity{}, fmt.Errorf("could not secure local machine identity storage")
	}
	path := filepath.Join(directory, machineIdentityFileName)
	if identity, found, err := readPersistedMachineIdentity(path); found || err != nil {
		if err != nil {
			return MachineIdentity{}, err
		}
		if configuredID != "" && configuredID != identity.machineID {
			return MachineIdentity{}, fmt.Errorf("configured machine identity does not match the persisted local identity")
		}
		return identity, nil
	}

	machineID := configuredID
	if machineID == "" {
		legacyID, found, err := readLegacyMachineID(filepath.Join(directory, legacyMachineIDFileName))
		if err != nil {
			return MachineIdentity{}, err
		}
		if found {
			machineID = legacyID
		} else {
			generated, err := uuid.NewV4()
			if err != nil {
				return MachineIdentity{}, fmt.Errorf("could not generate local machine identity")
			}
			machineID = generated.String()
		}
	} else if legacyID, found, err := readLegacyMachineID(filepath.Join(directory, legacyMachineIDFileName)); err != nil {
		return MachineIdentity{}, err
	} else if found && legacyID != machineID {
		return MachineIdentity{}, fmt.Errorf("configured machine identity does not match the persisted local identity")
	}

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return MachineIdentity{}, fmt.Errorf("could not generate local machine signing key")
	}
	defer eraseMachineIdentityKey(privateKey)
	protectedKey, err := protectMachineIdentityKey(privateKey)
	if err != nil || len(protectedKey) == 0 || len(protectedKey) > maxMachineIdentityFileSize {
		return MachineIdentity{}, fmt.Errorf("could not protect local machine signing key")
	}
	defer eraseMachineIdentityKey(protectedKey)
	contents, err := json.Marshal(persistedMachineIdentity{
		MachineID: machineID, PrivateKey: base64.RawStdEncoding.EncodeToString(protectedKey),
	})
	if err != nil {
		return MachineIdentity{}, fmt.Errorf("could not persist local machine identity")
	}
	if err := installMachineIdentityAtomically(directory, path, contents); err != nil {
		return MachineIdentity{}, err
	}
	identity, found, err := readPersistedMachineIdentity(path)
	if err != nil || !found {
		if err != nil {
			return MachineIdentity{}, err
		}
		return MachineIdentity{}, fmt.Errorf("local machine identity was not persisted")
	}
	if configuredID != "" && configuredID != identity.machineID {
		return MachineIdentity{}, fmt.Errorf("configured machine identity does not match the persisted local identity")
	}
	return identity, nil
}

func installMachineIdentityAtomically(directory, path string, contents []byte) error {
	temporary, err := os.CreateTemp(directory, ".machine-identity-*")
	if err != nil {
		return fmt.Errorf("could not persist local machine identity")
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := secureMachineIdentityFile(temporaryPath); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("could not secure local machine identity")
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("could not persist local machine identity")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("could not persist local machine identity")
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("could not persist local machine identity")
	}
	if err := os.Link(temporaryPath, path); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("could not atomically persist local machine identity")
	}
	if err := syncMachineIdentityDirectory(directory); err != nil {
		return fmt.Errorf("could not durably persist local machine identity")
	}
	return nil
}

func readPersistedMachineIdentity(path string) (MachineIdentity, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return MachineIdentity{}, false, nil
	}
	if err != nil {
		return MachineIdentity{}, true, fmt.Errorf("could not read local machine identity")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxMachineIdentityFileSize {
		return MachineIdentity{}, true, fmt.Errorf("persisted local machine identity is invalid")
	}
	if err := secureMachineIdentityFile(path); err != nil {
		return MachineIdentity{}, true, fmt.Errorf("could not secure local machine identity")
	}
	contents, err := os.ReadFile(path)
	if err != nil || len(contents) > maxMachineIdentityFileSize {
		return MachineIdentity{}, true, fmt.Errorf("could not read local machine identity")
	}
	var persisted persistedMachineIdentity
	if err := json.Unmarshal(contents, &persisted); err != nil {
		return MachineIdentity{}, true, fmt.Errorf("persisted local machine identity is invalid")
	}
	parsedID, err := uuid.FromString(strings.TrimSpace(persisted.MachineID))
	protectedKey, decodeErr := base64.RawStdEncoding.DecodeString(persisted.PrivateKey)
	if err != nil || parsedID == uuid.Nil || decodeErr != nil || len(protectedKey) == 0 {
		return MachineIdentity{}, true, fmt.Errorf("persisted local machine identity is invalid")
	}
	privateKey, err := unprotectMachineIdentityKey(protectedKey)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return MachineIdentity{}, true, fmt.Errorf("could not unlock local machine signing key")
	}
	defer eraseMachineIdentityKey(privateKey)
	return MachineIdentity{machineID: parsedID.String(), privateKey: append(ed25519.PrivateKey(nil), privateKey...), stateDirectory: filepath.Dir(path)}, true, nil
}

func eraseMachineIdentityKey(key []byte) {
	for index := range key {
		key[index] = 0
	}
}

func readLegacyMachineID(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", true, fmt.Errorf("could not read local machine identity")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64 {
		return "", true, fmt.Errorf("persisted local machine identity is invalid")
	}
	if err := secureMachineIdentityFile(path); err != nil {
		return "", true, fmt.Errorf("could not secure local machine identity")
	}
	contents, err := os.ReadFile(path)
	parsed, parseErr := uuid.FromString(strings.TrimSpace(string(contents)))
	if err != nil || parseErr != nil || parsed == uuid.Nil {
		return "", true, fmt.Errorf("persisted local machine identity is invalid")
	}
	return parsed.String(), true, nil
}
