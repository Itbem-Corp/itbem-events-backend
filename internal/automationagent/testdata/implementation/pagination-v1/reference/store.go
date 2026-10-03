package pagination

func Page(items []string, page, size int) []string {
	offset, limit := Bounds(page, size)
	if offset >= len(items) {
		return []string{}
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return append([]string{}, items[offset:end]...)
}
