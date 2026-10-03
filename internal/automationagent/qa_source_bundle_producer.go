package automationagent

import (
	"context"
	"fmt"
)

// The caller has already acquired the parent with repository-scoped authority.
// Child acquisition receives only independently approved repositories and the
// original gitlink SHA. Nothing from .gitmodules becomes a credential scope.
func buildQASourceBundle(ctx context.Context, root, commit string, approved map[string]string, acquire func(context.Context, string, string) ([]byte, string, error)) ([]byte, string, []packedQASourceDependency, error) {
	dependencies, err := readPinnedQASourceDependencies(ctx, root, commit, approved)
	if err != nil {
		return nil, "", nil, err
	}
	if len(dependencies) != 0 && acquire == nil {
		return nil, "", nil, fmt.Errorf("QA source dependency acquisition unavailable")
	}
	ctx = context.WithValue(ctx, qaSourceBundleBudgetKey{}, &qaSourceBundleBudget{bytes: maxQASourceTreeBytes, files: maxQASourceTreeFiles})
	links := make(map[string]string, len(dependencies))
	for _, dependency := range dependencies {
		links[dependency.Path] = dependency.CommitSHA
	}
	parentCtx := context.WithValue(ctx, qaSourceDependencyBoundaryKey{}, links)
	pack, digest, err := buildQASourcePack(parentCtx, root, commit)
	if err != nil {
		return nil, "", nil, err
	}
	children := make([]packedQASourceDependency, 0, len(dependencies))
	total := int64(len(pack))
	// Child producers must validate their tree under this shared budget. Nested
	// gitlinks stay denied; approving the parent does not approve another level.
	childCtx := context.WithValue(ctx, qaSourceDependencyBoundaryKey{}, map[string]string(nil))
	for _, dependency := range dependencies {
		if err := ctx.Err(); err != nil {
			return nil, "", nil, err
		}
		childPack, childDigest, err := acquire(childCtx, dependency.Repository, dependency.CommitSHA)
		if err != nil {
			return nil, "", nil, err
		}
		if !validQASourcePackEnvelope(childDigest, childPack) {
			return nil, "", nil, fmt.Errorf("QA source acquired dependency integrity invalid")
		}
		total += int64(len(childPack))
		if total > maxQASourcePackBytes {
			return nil, "", nil, fmt.Errorf("QA source combined packages exceed their boundary")
		}
		children = append(children, packedQASourceDependency{pinnedQASourceDependency: dependency, PackSHA256: childDigest, Pack: childPack})
	}
	return pack, digest, children, nil
}
