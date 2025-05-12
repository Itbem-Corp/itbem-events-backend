package gormService

import (
	"errors"
	"events-stocks/configuration"
	"gorm.io/gorm/clause"
)

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

// Update actualiza un registro existente
func Update[T any](model *T, id interface{}) error {
	result := configuration.DB.Model(model).Where("id = ?", id).Updates(model)
	if result.RowsAffected == 0 {
		return errors.New("record not found")
	}
	return result.Error
}

func UpdateFields[T any](model *T, fields map[string]interface{}) error {
	return configuration.DB.Model(model).Updates(fields).Error
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

	// Aplicar filtros
	if opts.Filters != nil {
		query = query.Where(opts.Filters)
	}

	// Ordenamiento
	if opts.OrderBy != "" {
		direction := "ASC"
		if opts.OrderDir != "" {
			direction = opts.OrderDir
		}
		query = query.Order(opts.OrderBy + " " + direction)
	}

	// Paginación
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
