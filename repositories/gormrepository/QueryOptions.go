package gormrepository

type QueryOptions struct {
	Filters  map[string]interface{}
	Limit    int
	Offset   int
	OrderBy  string
	OrderDir string // "asc" o "desc"
	Preload  []string
}
