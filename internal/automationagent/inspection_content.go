package automationagent

// InspectionContent uses the existing persistence boundary: private analysis
// fields and credential patterns are removed before an operator can inspect it.
func InspectionContent(content string, response bool) (string, int) {
	if response {
		cleaned, err := sanitizeProviderContentForPersistence(content)
		if err != nil {
			return omittedProviderResponse, 0
		}
		content = cleaned
	}
	return redactWorkspaceExcerpt(content)
}
