package gormrepository

import (
	"errors"
	"events-stocks/configuration"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func DB() *gorm.DB {
	return configuration.DB
}

// Insert agrega un solo registro
func Insert[T any](model *T) error {
	return configuration.DB.Create(model).Error
}

// InsertMany agrega múltiples registros en lote
func InsertMany[T any](models []T) error {
	return configuration.DB.Create(&models).Error
}

// InsertManyBatch permite controlar el tamaño del batch
func InsertManyBatch[T any](models []T, batchSize int) error {
	return configuration.DB.CreateInBatches(&models, batchSize).Error
}

func FirstOrCreate[T any](model *T, conditions map[string]interface{}) error {
	return configuration.DB.Where(conditions).FirstOrCreate(model).Error
}

// GetFirst obtiene el primer registro que cumpla con los filtros
func GetFirst[T any](model *T, opts QueryOptions) error {
	query := configuration.DB.Model(model)

	if len(opts.Preload) > 0 {
		for _, preload := range opts.Preload {
			query = query.Preload(preload)
		}
	}
	if opts.Filters != nil {
		query = query.Where(opts.Filters)
	}
	if opts.OrderBy != "" {
		dir := "ASC"
		if opts.OrderDir != "" {
			dir = opts.OrderDir
		}
		query = query.Order(opts.OrderBy + " " + dir)
	}

	return query.First(model).Error
}

// InsertIfNotExists inserta un registro solo si no existe (basado en columnas únicas)
func InsertIfNotExists[T any](model *T, conflictColumns []string) error {
	return configuration.DB.Clauses(clause.OnConflict{
		Columns:   toColumns(conflictColumns),
		DoNothing: true,
	}).Create(model).Error
}

func toColumns(cols []string) []clause.Column {
	var result []clause.Column
	for _, c := range cols {
		result = append(result, clause.Column{Name: c})
	}
	return result
}

// GetByID obtiene un registro por ID (requiere un puntero a instancia vacía)
func GetByID[T any](model *T, id interface{}, preloads ...string) error {
	query := configuration.DB
	for _, preload := range preloads {
		query = query.Preload(preload)
	}

	// Si el id es UUID, usamos Where
	switch id.(type) {
	case string:
		return query.Where("id = ?", id).First(model).Error
	default:
		return query.First(model, id).Error
	}
}

// Update actualiza un registro existente.
// Usa Select("*") para que GORM persista valores cero (false, 0, "").
func Update[T any](model *T, id interface{}) error {
	result := configuration.DB.Model(model).Where("id = ?", id).Select("*").Updates(model)
	if result.RowsAffected == 0 {
		return errors.New("record not found")
	}
	return result.Error
}

func UpdateFields[T any](model *T, fields map[string]interface{}) error {
	return configuration.DB.Model(model).Updates(fields).Error
}

func UpdateFieldsByID[T any](id interface{}, fields map[string]interface{}, model *T) error {
	result := configuration.DB.
		Model(model).
		Where("id = ?", id).
		Updates(fields)

	if result.RowsAffected == 0 {
		return errors.New("record not found")
	}

	return result.Error
}

func UpdateMany[T any](models []T, fields []string) error {
	tx := configuration.DB
	for _, m := range models {
		query := tx.Model(&m)
		if len(fields) > 0 {
			query = query.Select(fields)
		}
		if err := query.Updates(m).Error; err != nil {
			return err
		}
	}
	return nil
}

// Delete elimina un registro por ID (requiere una instancia del tipo base)
func Delete[T any](id interface{}, model *T) error {
	return configuration.DB.Where("id = ?", id).Delete(model).Error
}

func DeleteByFilters[T any](filters map[string]interface{}) error {
	return configuration.DB.Where(filters).Delete(new(T)).Error
}

// GetList obtiene una lista de registros opcionalmente filtrada por campos
func GetList[T any](list *[]T, opts QueryOptions) error {
	query := configuration.DB.Model(list)

	// Preload relaciones
	if len(opts.Preload) > 0 {
		for _, preload := range opts.Preload {
			query = query.Preload(preload)
		}
	}

	// Filtros, orden, paginación...
	if opts.Filters != nil {
		query = query.Where(opts.Filters)
	}
	if opts.OrderBy != "" {
		dir := "ASC"
		if opts.OrderDir != "" {
			dir = opts.OrderDir
		}
		query = query.Order(opts.OrderBy + " " + dir)
	}
	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}

	return query.Find(list).Error
}

// Exists verifica si existe un registro con un campo específico
func Exists[T any](model *T, field string, value interface{}) (bool, error) {
	var count int64
	err := configuration.DB.Model(model).Where(field+" = ?", value).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GormTransactor implements ports.Transactor using the global GORM DB.
type GormTransactor struct{}

func NewGormTransactor() *GormTransactor { return &GormTransactor{} }

func (t *GormTransactor) Transaction(fn func(tx *gorm.DB) error) error {
	return configuration.DB.Transaction(fn)
}
