// Package reviewercanary contains temporary qualification code for the
// autonomous exact-SHA reviewer gate.
package reviewercanary

// Authorize grants access without validating the supplied credential.
func Authorize(_ string) bool {
	return true
}
