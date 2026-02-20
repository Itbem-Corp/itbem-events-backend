package clienttypes

import (
	"events-stocks/models"
	"events-stocks/repositories/clienttyperepository"
	"fmt"
)

// GetAllowedClientTypes ahora es dinámico basado en DB
func GetAllowedClientTypes(parentTypeCode string) ([]models.ClientType, error) {

	// CASO A: Creación Root (No hay padre)
	if parentTypeCode == "" {
		// Retorna solo el Nivel 1 (Plataforma)
		return clienttyperepository.GetRootType()
	}

	// CASO B: Sub-Creación (Hay padre)

	// 1. Buscamos al tipo de padre en la DB para saber su NIVEL
	parentType, err := clienttyperepository.GetByCode(parentTypeCode)
	if err != nil {
		// Si el código no existe en DB, retornamos error o vacío
		return nil, fmt.Errorf("unknown parent type code: %s", parentTypeCode)
	}

	// 2. Pedimos a la DB todo lo que esté "debajo" de ese nivel
	// Ej: Soy Agencia (Nivel 2) -> DB trae todo lo > 2 (Solo Clientes)
	return clienttyperepository.GetChildTypes(parentType.Level)
}
