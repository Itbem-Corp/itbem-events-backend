package automationagent

import "testing"

func TestDockerSandboxUserPreservesUnprivilegedOwnership(t *testing.T) {
	for _, tc := range []struct {
		uid, gid int
		want     string
	}{
		{1000, 1000, "1000:1000"}, {65532, 65532, "65532:65532"},
		{0, 0, "65532:65532"}, {-1, -1, "65532:65532"},
		{1000, 0, "1000:65532"}, {1000, -1, "65532:65532"},
	} {
		if got := dockerSandboxUser(tc.uid, tc.gid); got != tc.want {
			t.Errorf("uid=%d gid=%d: got %q want %q", tc.uid, tc.gid, got, tc.want)
		}
	}
}
