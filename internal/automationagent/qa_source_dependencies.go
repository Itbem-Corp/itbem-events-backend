package automationagent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

type pinnedQASourceDependency struct {
	Path       string
	Repository string
	CommitSHA  string
}

// Gitmodule text is data, not a read grant. Each gitlink must match a
// separately approved path/repository mapping before any child can be fetched.
// This parser does not enable dependency transport or checkout by itself.
func readPinnedQASourceDependencies(ctx context.Context, root, commit string, approved map[string]string) ([]pinnedQASourceDependency, error) {
	if !gitCommitPattern.MatchString(commit) || len(approved) > 16 {
		return nil, fmt.Errorf("QA dependency authority invalid")
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := qaSourceGitCommand(bounded, root, "ls-tree", "-r", "-z", commit)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr boundedCommandBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var dependencies []pinnedQASourceDependency
	reader := bufio.NewReaderSize(stdout, 8192)
	for {
		record, err := reader.ReadSlice(0)
		if err == io.EOF && len(record) == 0 {
			break
		}
		if err != nil {
			cancel()
			_ = cmd.Wait()
			return nil, fmt.Errorf("QA dependency tree record invalid")
		}
		header, name, ok := strings.Cut(string(record[:len(record)-1]), "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 {
			cancel()
			_ = cmd.Wait()
			return nil, fmt.Errorf("QA dependency tree invalid")
		}
		if fields[0] != "160000" {
			continue
		}
		if fields[1] != "commit" || !gitCommitPattern.MatchString(fields[2]) || !safeQADependencyPath(name) || len(dependencies) >= 16 || !githubRepositoryNamePattern.MatchString(strings.ToLower(approved[name])) {
			cancel()
			_ = cmd.Wait()
			return nil, fmt.Errorf("QA dependency requires an approved pinned repository")
		}
		dependencies = append(dependencies, pinnedQASourceDependency{Path: name, Repository: strings.ToLower(approved[name]), CommitSHA: fields[2]})
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("QA dependency tree enumeration failed")
	}
	if len(dependencies) == 0 {
		return dependencies, nil
	}
	config := qaSourceGitCommand(bounded, root, "config", "--blob", commit+":.gitmodules", "--no-includes", "--null", "--get-regexp", `^submodule\..*\.(path|url)$`)
	var data, errors boundedCommandBuffer
	config.Stdout, config.Stderr = &data, &errors
	if err := config.Run(); err != nil || data.Len() >= maxCommandOutput {
		return nil, fmt.Errorf("QA dependency declarations invalid or oversized")
	}
	type declaration struct {
		path, url   string
		paths, urls int
	}
	modules := map[string]*declaration{}
	for _, record := range strings.Split(strings.TrimSuffix(data.String(), "\x00"), "\x00") {
		key, value, ok := strings.Cut(record, "\n")
		if !ok {
			return nil, fmt.Errorf("QA dependency declaration invalid")
		}
		index := strings.LastIndex(key, ".")
		if index < 0 {
			return nil, fmt.Errorf("QA dependency declaration invalid")
		}
		name, suffix := key[:index], key[index+1:]
		entry := modules[name]
		if entry == nil {
			entry = &declaration{}
			modules[name] = entry
		}
		switch suffix {
		case "path":
			entry.path = value
			entry.paths++
		case "url":
			entry.url = value
			entry.urls++
		}
	}
	for _, dependency := range dependencies {
		matches := 0
		for _, entry := range modules {
			if entry.path != dependency.Path {
				continue
			}
			remote, err := url.Parse(entry.url)
			if entry.paths != 1 || entry.urls != 1 || err != nil || remote.Scheme != "https" || remote.Host != "github.com" || remote.User != nil || remote.RawQuery != "" || remote.Fragment != "" {
				return nil, fmt.Errorf("QA dependency declaration authority invalid")
			}
			repository := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(remote.Path, "/"), ".git"))
			if repository != dependency.Repository {
				return nil, fmt.Errorf("QA dependency repository differs from approval")
			}
			matches++
		}
		if matches != 1 {
			return nil, fmt.Errorf("QA dependency declaration missing or ambiguous")
		}
	}
	return dependencies, nil
}

func safeQADependencyPath(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) || path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, "\x00\\\r\n\t") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "." || component == ".." || strings.EqualFold(component, ".git") {
			return false
		}
	}
	return true
}
