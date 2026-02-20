package seeds

import (
	"events-stocks/models"
	"fmt"
	"gorm.io/gorm"
)

func SeedClientEventiAppSeed(db *gorm.DB) {
	// 1. Buscar el ID real del tipo PLATFORM
	var platformType models.ClientType
	if err := db.Where("code = ?", "PLATFORM").First(&platformType).Error; err != nil {
		fmt.Printf("❌ Error: No se pudo encontrar el tipo PLATFORM: %v\n", err)
		return // Detenemos si no hay tipo, para evitar insertar con ID vacío
	}

	// 2. Definir el cliente raíz
	rootClient := models.Client{
		Name:         "EventiApp",
		Code:         "eventiapp",
		ClientTypeID: platformType.ID,
		IsActive:     true,
	}

	// 3. FirstOrCreate busca por 'code'. Si no existe, lo crea con 'rootClient'.
	// Guardamos el resultado en 'err' para que Go no se queje.
	if err := db.Where(models.Client{Code: "eventiapp"}).FirstOrCreate(&rootClient).Error; err != nil {
		fmt.Printf("❌ Error al crear el cliente raíz: %v\n", err)
	} else {
		fmt.Println("✅ Cliente raíz verificado/creado: EventiApp")
	}
}
