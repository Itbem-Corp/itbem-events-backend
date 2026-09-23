//go:build !linux

package main

// The guest agent is Linux/AF_VSOCK-only. Keep the host Go module portable so
// ordinary Windows CI can compile and test the rest of the backend.
func main() {}
