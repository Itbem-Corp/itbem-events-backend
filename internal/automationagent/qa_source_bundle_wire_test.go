package automationagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestQASourceBundleWireBindsManifestAndAllPackages(t *testing.T) {
	commit, pack := qaSourcePackFixture(t)
	hash := fmt.Sprintf("%x", sha256.Sum256(pack))
	bundle := QASourceBundle{Pack: pack, PackSHA256: hash, Dependencies: []QASourceDependencyPack{
		{Path: "z", Repository: "example/z", CommitSHA: commit, PackSHA256: hash, Pack: pack},
		{Path: "a", Repository: "example/a", CommitSHA: commit, PackSHA256: hash, Pack: pack},
	}}
	body, digest, err := EncodeQASourceBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeQASourceBundle(body, digest)
	if err != nil || len(decoded.Dependencies) != 2 || decoded.Dependencies[0].Path != "a" || !bytes.Equal(decoded.Pack, pack) || decoded.Dependencies[1].CommitSHA != commit {
		t.Fatalf("round trip failed: %v", err)
	}
	for _, scenario := range []string{"tamper", "truncated", "trailing", "oversized-header", "unknown-field", "duplicate-json-key", "wrong-root-digest", "oversized-package", "duplicate-path", "overlap", "wrong-child-digest"} {
		t.Run(scenario, func(t *testing.T) {
			changed := append([]byte(nil), body...)
			n := int(binary.BigEndian.Uint32(body[4:8]))
			var m qaSourceBundleManifest
			if err := json.Unmarshal(body[8:8+n], &m); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "tamper":
				changed[len(changed)-1] ^= 1
			case "truncated":
				changed = changed[:len(changed)-1]
			case "trailing":
				changed = append(changed, 0)
			case "oversized-header":
				binary.BigEndian.PutUint32(changed[4:8], ^uint32(0))
			default:
				switch scenario {
				case "wrong-root-digest":
					m.RootDigest = strings.Repeat("a", 64)
				case "wrong-child-digest":
					m.Dependencies[0].Digest = strings.Repeat("a", 64)
				case "oversized-package":
					m.Dependencies[0].Bytes = maxQASourcePackBytes + 1
				case "duplicate-path":
					m.Dependencies[1].Path = m.Dependencies[0].Path
				case "overlap":
					m.Dependencies[1].Path = "a/nested"
				}
				raw, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "unknown-field" {
					raw = append([]byte(`{"unapproved":true,`), raw[1:]...)
				}
				if scenario == "duplicate-json-key" {
					raw = append([]byte(`{"schema_version":2,`), raw[1:]...)
				}
				changed = append([]byte("QASB"), make([]byte, 4)...)
				binary.BigEndian.PutUint32(changed[4:8], uint32(len(raw)))
				changed = append(changed, raw...)
				changed = append(changed, body[8+n:]...)
			}
			// Even a newly computed transport digest cannot excuse malformed
			// descriptors or a package whose individual digest changed.
			changedDigest := fmt.Sprintf("%x", sha256.Sum256(changed))
			if _, err := DecodeQASourceBundle(changed, changedDigest); err == nil {
				t.Fatal("malformed bundle accepted")
			}
			if _, err := DecodeQASourceBundle(changed, digest); err == nil {
				t.Fatal("altered authenticated bundle accepted")
			}
		})
	}
}
