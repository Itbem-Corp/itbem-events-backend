package automationagent

import (
	"crypto/ed25519"
	"errors"

	"events-stocks/internal/agentcallbackauth"
)

// AgentInstanceEnrollmentProof returns the public identity and a proof that
// this process controls its protected private key. The private key never
// leaves the machine identity store.
func (identity MachineIdentity) AgentInstanceEnrollmentProof(agentKey string) (string, string, error) {
	if len(identity.privateKey) != ed25519.PrivateKeySize {
		return "", "", errors.New("local machine signing key is unavailable")
	}
	encodedPublicKey, err := agentcallbackauth.EncodePublicKey(identity.PublicKey())
	if err != nil {
		return "", "", errors.New("could not encode local machine public key")
	}
	message, err := agentcallbackauth.AgentInstanceEnrollmentMessage(agentKey, identity.machineID, encodedPublicKey)
	if err != nil {
		return "", "", errors.New("could not prepare machine enrollment proof")
	}
	signature, err := agentcallbackauth.EncodeSignature(ed25519.Sign(identity.privateKey, message))
	if err != nil {
		return "", "", errors.New("could not sign machine enrollment proof")
	}
	return encodedPublicKey, signature, nil
}
