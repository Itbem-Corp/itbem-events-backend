//go:build linux

package main

// Minimal guest-side command agent for the local Firecracker proof. It is
// intentionally allowlisted and line-delimited; it is not a general shell.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/unix"
)

const listenPort = 52

type request struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type response struct {
	OK     bool   `json:"ok"`
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	Error  string `json:"error,omitempty"`
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
	decoder := json.NewDecoder(bufio.NewReader(file))
	var input request
	if err := decoder.Decode(&input); err != nil {
		writeResponse(file, response{Error: "invalid request"})
		return
	}
	if !allowed(input.Command) || len(input.Args) > 8 {
		writeResponse(file, response{Error: "command is not allowlisted"})
		return
	}
	command := exec.Command(input.Command, input.Args...)
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	result := response{OK: err == nil, Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		result.Error = err.Error()
	}
	writeResponse(file, result)
}

func allowed(command string) bool {
	return command == "/bin/sh" || command == "/bin/sha256sum" || command == "/bin/cat"
}

func writeResponse(file *os.File, result response) {
	if err := json.NewEncoder(file).Encode(result); err != nil {
		_, _ = fmt.Fprintln(file, `{"ok":false,"error":"response encoding failed"}`)
	}
}

func osFile(fd int) *os.File { return os.NewFile(uintptr(fd), "vsock-client") }
