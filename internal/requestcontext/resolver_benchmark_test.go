package requestcontext

import (
	"testing"

	"github.com/gofrs/uuid"
)

func BenchmarkResolveOrganization(b *testing.B) {
	organizationID := uuid.Must(uuid.NewV4())
	input := Input{
		AuthenticatedApplication: "eventiapp",
		RequestedApplication:     "eventiapp",
		SessionApplication:       "eventiapp",
		RequestedWorkspaceMode:   WorkspaceOrganization,
		RequestedOrganizationID:  organizationID.String(),
		OrganizationAllowed:      func(candidate uuid.UUID) bool { return candidate == organizationID },
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := Resolve(input); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkResolvePlatform(b *testing.B) {
	input := Input{
		AuthenticatedApplication: "itbem",
		RequestedApplication:     "itbem",
		SessionApplication:       "itbem",
		RequestedWorkspaceMode:   WorkspacePlatform,
		AllowsPlatformAdmin:      true,
		IsRoot:                   true,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := Resolve(input); err != nil {
			b.Fatal(err)
		}
	}
}
