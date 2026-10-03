package pagination

func Bounds(page, size int) (offset, limit int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 2
	}
	return (page - 1) * size, size
}
