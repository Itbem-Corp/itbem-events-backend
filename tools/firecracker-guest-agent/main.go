//go:build linux

package main

// Minimal guest-side command agent for the local Firecracker proof. It is
// intentionally allowlisted and line-delimited; it is not a general shell.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const listenPort = 52
const maxRequestBytes = 32768
const maxOutputBytes = 12000

type request struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type response struct {
	OK       bool   `json:"ok"`
	Executed bool   `json:"executed"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Error    string `json:"error,omitempty"`
}

func main() {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		panic(err)
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: listenPort}); err != nil {
		panic(err)
	}
	if err := unix.Listen(fd, 4); err != nil {
		panic(err)
	}
	if address, err := unix.Getsockname(fd); err == nil {
		fmt.Printf("ITBEM_VSOCK_LISTENING %v\\n", address)
	} else {
		fmt.Println("ITBEM_VSOCK_LISTENING")
	}
	for {
		client, _, err := unix.Accept(fd)
		if err != nil {
			fmt.Println("ITBEM_VSOCK_ACCEPT_ERROR", err.Error())
			continue
		}
		serve(client)
		_ = unix.Close(client)
	}
}

func serve(fd int) {
	file := osFile(fd)
	defer file.Close()
	decoder := json.NewDecoder(bufio.NewReader(io.LimitReader(file, maxRequestBytes)))
	var input request
	if err := decoder.Decode(&input); err != nil {
		writeResponse(file, response{Error: "invalid request"})
		return
	}
	timeout := 15 * time.Second
	if input.Command == "/sdk/bin/go" {
		timeout = 60 * time.Second
	}
	writeResponse(file, execute(input, timeout))
}

func execute(input request, timeout time.Duration) response {
	if !allowed(input.Command) || len(input.Args) > 8 {
		return response{Error: "command is not allowlisted"}
	}
	for _, arg := range input.Args {
		if len(arg) > 2048 || strings.ContainsRune(arg, 0) {
			return response{Error: "command arguments exceed the guest contract"}
		}
	}
	if input.Command == "/sdk/bin/go" && !goTestArguments(input.Args) {
		return response{Error: "Go command is not registered"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, input.Command, input.Args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	command.WaitDelay = time.Second
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", "LANG=C"}
	if input.Command == "/sdk/bin/go" {
		command.Dir = "/workspace"
		command.Env = append(command.Env, "GOROOT=/sdk", "GOCACHE=/tmp/cache", "GOPATH=/tmp/gopath", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=0", "GOENV=off", "GOMAXPROCS=1")
	}
	var stdout, stderr boundedOutput
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	result := response{OK: err == nil && !stdout.truncated && !stderr.truncated, Stdout: stdout.String(), Stderr: stderr.String()}
	if command.ProcessState != nil {
		result.Executed = true
		result.ExitCode = command.ProcessState.ExitCode()
	}
	if err != nil {
		result.Error = "guest command failed"
	}
	if ctx.Err() != nil {
		result.Error = "guest command timed out"
	}
	if stdout.truncated || stderr.truncated {
		result.Error = "guest output exceeded limit"
	}
	return result
}

// Continue draining pipes after truncation so an excessive writer cannot
// block the guest agent. Truncation never counts as successful evidence.
type boundedOutput struct {
	strings.Builder
	truncated bool
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	remaining := maxOutputBytes - output.Len()
	if len(data) > remaining {
		output.truncated = true
		_, _ = output.Builder.Write(data[:remaining])
	} else {
		_, _ = output.Builder.Write(data)
	}
	return len(data), nil
}

func allowed(command string) bool {
	return command == "/bin/sh" || command == "/bin/sha256sum" || command == "/bin/cat" || command == "/sdk/bin/go"
}

func goTestArguments(args []string) bool {
	want := []string{"test", "-json", "-count=1", "-timeout=30s", "./..."}
	if len(args) != len(want) {
		return false
	}
	for index := range want {
		if args[index] != want[index] {
			return false
		}
	}
	return true
}

func writeResponse(file *os.File, result response) {
	if err := json.NewEncoder(file).Encode(result); err != nil {
		_, _ = fmt.Fprintln(file, `{"ok":false,"error":"response encoding failed"}`)
	}
}

func osFile(fd int) *os.File { return os.NewFile(uintptr(fd), "vsock-client") }
