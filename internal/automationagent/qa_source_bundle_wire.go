package automationagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const maxQASourceBundleManifest = 16 << 10
const MaxQASourceBundleBytes = maxQASourcePackBytes + maxQASourceBundleManifest + 8

type qaSourceBundleManifest struct {
	Version      int                        `json:"schema_version"`
	RootDigest   string                     `json:"root_pack_sha256"`
	RootBytes    int                        `json:"root_pack_bytes"`
	Dependencies []qaSourceBundleDescriptor `json:"dependencies"`
}

type qaSourceBundleDescriptor struct {
	Path       string `json:"path"`
	Repository string `json:"repository"`
	CommitSHA  string `json:"commit_sha"`
	Digest     string `json:"pack_sha256"`
	Bytes      int    `json:"pack_bytes"`
}

// The digest binds the manifest and every raw package. Callers must carry it
// through authenticated task metadata and immutable server provenance.
func EncodeQASourceBundle(bundle QASourceBundle) ([]byte, string, error) {
	children := append([]QASourceDependencyPack(nil), bundle.Dependencies...)
	sort.Slice(children, func(i, j int) bool { return children[i].Path < children[j].Path })
	manifest := qaSourceBundleManifest{Version: 1, RootDigest: bundle.PackSHA256, RootBytes: len(bundle.Pack)}
	for _, child := range children {
		manifest.Dependencies = append(manifest.Dependencies, qaSourceBundleDescriptor{Path: child.Path, Repository: child.Repository, CommitSHA: child.CommitSHA, Digest: child.PackSHA256, Bytes: len(child.Pack)})
	}
	if err := validateQASourceBundleManifest(manifest); err != nil {
		return nil, "", err
	}
	if !validQASourcePackEnvelope(bundle.PackSHA256, bundle.Pack) {
		return nil, "", fmt.Errorf("QA bundle root integrity invalid")
	}
	raw, err := json.Marshal(manifest)
	if err != nil || len(raw) > maxQASourceBundleManifest {
		return nil, "", fmt.Errorf("QA bundle manifest exceeds its boundary")
	}
	var body bytes.Buffer
	body.WriteString("QASB")
	_ = binary.Write(&body, binary.BigEndian, uint32(len(raw)))
	body.Write(raw)
	body.Write(bundle.Pack)
	for _, child := range children {
		if !validQASourcePackEnvelope(child.PackSHA256, child.Pack) {
			return nil, "", fmt.Errorf("QA bundle child integrity invalid")
		}
		body.Write(child.Pack)
	}
	data := body.Bytes()
	return data, fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// Decoding does not authorize repositories or materialize source. The caller
// still checks the operator approvals and original parent gitlinks.
func DecodeQASourceBundle(data []byte, digest string) (QASourceBundle, error) {
	deny := func() (QASourceBundle, error) { return QASourceBundle{}, fmt.Errorf("QA bundle envelope invalid") }
	if len(data) < 8 || len(data) > MaxQASourceBundleBytes || string(data[:4]) != "QASB" || !sha256DigestPattern.MatchString(digest) || fmt.Sprintf("%x", sha256.Sum256(data)) != digest {
		return deny()
	}
	n := int(binary.BigEndian.Uint32(data[4:8]))
	if n < 1 || n > maxQASourceBundleManifest || n > len(data)-8 {
		return deny()
	}
	var manifest qaSourceBundleManifest
	decoder := json.NewDecoder(bytes.NewReader(data[8 : 8+n]))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF || validateQASourceBundleManifest(manifest) != nil {
		return deny()
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || !bytes.Equal(canonical, data[8:8+n]) {
		return deny()
	}
	payload := data[8+n:]
	total := manifest.RootBytes
	for _, child := range manifest.Dependencies {
		total += child.Bytes
	}
	if len(payload) != total {
		return deny()
	}
	root := payload[:manifest.RootBytes]
	if !validQASourcePackEnvelope(manifest.RootDigest, root) {
		return deny()
	}
	result := QASourceBundle{Pack: root, PackSHA256: manifest.RootDigest}
	position := manifest.RootBytes
	for _, child := range manifest.Dependencies {
		pack := payload[position : position+child.Bytes]
		if !validQASourcePackEnvelope(child.Digest, pack) {
			return deny()
		}
		result.Dependencies = append(result.Dependencies, QASourceDependencyPack{Path: child.Path, Repository: child.Repository, CommitSHA: child.CommitSHA, PackSHA256: child.Digest, Pack: pack})
		position += child.Bytes
	}
	return result, nil
}

func validateQASourceBundleManifest(m qaSourceBundleManifest) error {
	if m.Version != 1 || !sha256DigestPattern.MatchString(m.RootDigest) || m.RootBytes < 12 || m.RootBytes > maxQASourcePackBytes || len(m.Dependencies) > 16 {
		return fmt.Errorf("QA bundle manifest invalid")
	}
	total := int64(m.RootBytes)
	for i, child := range m.Dependencies {
		if !safeQADependencyPath(child.Path) || !githubRepositoryNamePattern.MatchString(child.Repository) || child.Repository != strings.ToLower(child.Repository) || !gitCommitPattern.MatchString(child.CommitSHA) || !sha256DigestPattern.MatchString(child.Digest) || child.Bytes < 12 || child.Bytes > maxQASourcePackBytes {
			return fmt.Errorf("QA bundle child descriptor invalid")
		}
		if i > 0 && m.Dependencies[i-1].Path >= child.Path {
			return fmt.Errorf("QA bundle descriptors are not unique and ordered")
		}
		for j := 0; j < i; j++ {
			if strings.HasPrefix(child.Path, m.Dependencies[j].Path+"/") {
				return fmt.Errorf("QA bundle dependency paths overlap")
			}
		}
		total += int64(child.Bytes)
	}
	if total > maxQASourcePackBytes {
		return fmt.Errorf("QA bundle packages exceed their combined boundary")
	}
	return nil
}
