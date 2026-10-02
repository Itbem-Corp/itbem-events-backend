package automationagent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"time"
)

// Builds only the original commit and its complete tree, without rewriting
// that commit or exporting parent history. Callers own authenticated source
// acquisition and resource isolation; this function grants neither.
func buildQASourcePack(ctx context.Context, root, commit string) ([]byte, string, error) {
	if !gitCommitPattern.MatchString(commit) {
		return nil, "", fmt.Errorf("QA source commit is invalid")
	}
	if err := validateQASourceTree(ctx, root, commit); err != nil {
		return nil, "", err
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	objects := qaSourceGitCommand(bounded, root, "rev-list", "--objects", "--no-object-names", "--no-walk", commit)
	pipe, err := objects.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	var objectErrors boundedCommandBuffer
	objects.Stderr = &objectErrors
	if err := objects.Start(); err != nil {
		return nil, "", fmt.Errorf("QA source object enumeration failed")
	}
	var objectIDs bytes.Buffer
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 128), 128)
	count := 0
	for scanner.Scan() {
		id := scanner.Text()
		count++
		if !gitCommitPattern.MatchString(id) || count > 3*maxQASourceTreeFiles {
			cancel()
			_ = objects.Wait()
			return nil, "", fmt.Errorf("QA source object enumeration exceeds its boundary")
		}
		objectIDs.WriteString(id + "\n")
	}
	readErr := scanner.Err()
	if readErr != nil {
		cancel()
	}
	if err := objects.Wait(); err != nil || readErr != nil || count == 0 {
		return nil, "", fmt.Errorf("QA source object enumeration failed")
	}
	pack := qaSourceGitCommand(bounded, root, "pack-objects", "--stdout", "--threads=1")
	pack.Stdin = &objectIDs
	var output qaSourcePackBuffer
	var packErrors boundedCommandBuffer
	pack.Stdout, pack.Stderr = &output, &packErrors
	if err := pack.Run(); err != nil {
		return nil, "", fmt.Errorf("QA source pack generation failed")
	}
	data := output.Bytes()
	if len(data) < 12 {
		return nil, "", fmt.Errorf("QA source pack generation failed")
	}
	return data, fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

type qaSourcePackBuffer struct{ bytes.Buffer }

func (b *qaSourcePackBuffer) Write(data []byte) (int, error) {
	if len(data) > maxQASourcePackBytes-b.Len() {
		return 0, fmt.Errorf("QA source pack exceeds its boundary")
	}
	return b.Buffer.Write(data)
}

// Prevent io.Copy from selecting the embedded bytes.Buffer's unbounded
// ReaderFrom implementation instead of the checked Write method.
func (b *qaSourcePackBuffer) ReadFrom(reader io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{b}, reader)
}
