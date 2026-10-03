package pagination

func Bounds(page, size int) (offset, limit int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 2
	}
	maxInt := int(^uint(0) >> 1)
	if page-1 > maxInt/size {
		return maxInt, size
	}
	return (page - 1) * size, size
}
