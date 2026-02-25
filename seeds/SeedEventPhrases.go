package seeds

import (
	"log/slog"

	"events-stocks/models"
	"gorm.io/gorm"
)

func SeedEventPhrases(db *gorm.DB) {
	weddingPhrases := []string{
		// Short & poetic
		"El amor no necesita explicaciones",
		"Hoy sus historias se vuelven una",
		"No se busca al amor, se reconoce",
		"El mejor día es el que se comparte",
		"Aquí empieza su historia juntos",
		"Amor que se ve, amor que se siente",
		"Dos almas, una misma dirección",
		"El amor es el único lujo necesario",
		"Hoy, mañana y siempre",
		"Que la vida los sorprenda juntos",
		// Warm & intimate
		"Lo más bonito no es el vestido ni las flores — es la mirada",
		"Que cada foto aquí sea un recuerdo que los haga sonreír en 30 años",
		"Bienvenidos al primer día del resto de su vida juntos",
		"El amor verdadero no se planea, simplemente aparece",
		"Gracias por dejarlos compartir este momento tan especial",
		"Hoy celebramos que dos personas decidieron elegirse para siempre",
		"No hay fotografía que capture todo lo que se siente aquí",
		"Este día lo recordarán para siempre — gracias por ser parte",
		"El amor se construye todos los días, hoy pusieron la primera piedra",
		"Juntos es el lugar más bonito del mundo",
		// Lyrical
		"El amor no mira el tiempo, el tiempo mira al amor",
		"Donde hay amor, siempre hay un hogar",
		"Amar es encontrar en la felicidad de otro la propia felicidad",
		"El corazón que ama siempre es joven",
		"Dos medias naranjas que decidieron volverse una",
		"El amor es la poesía de los sentidos",
		"Quien ama con el alma, jamás se cansa",
		"El amor es la única riqueza que crece cuando se comparte",
		"Amar es dar sin esperar nada a cambio",
		"El amor es el arte más difícil y más bello",
		// Celebratory
		"¡Que vivan los novios!",
		"Un brindis por el amor que une y la vida que comienza",
		"Que sus días siempre tengan música, flores y risas",
		"Que el amor sea su compás y la alegría su destino",
		"Por el amor que los trajo hasta aquí",
		"Que cada amanecer los encuentre más enamorados",
		"Que su vida juntos sea tan bella como hoy",
		"Hoy el amor gana, y todos somos testigos",
		"Por los novios: que el amor los guíe siempre",
		"Que este día sea el más feliz de muchos más por venir",
		// Time & memory
		"Los momentos más bonitos no se compran, se viven",
		"Este instante existirá para siempre en algún lugar del tiempo",
		"Hoy es uno de esos días que recordarás toda la vida",
		"Los recuerdos son el único tesoro que nadie puede quitarte",
		"La memoria del corazón dura más que la del tiempo",
		"Guardar este momento es guardar un pedazo de felicidad",
		"El tiempo pasa, los recuerdos quedan",
		"Cada foto aquí es un capítulo de su historia",
		"Estos momentos son los que le dan sentido a todo",
		"Un día como este merece ser recordado para siempre",
		// Family & togetherness
		"El amor que une a dos familias es el amor más grande",
		"Hoy dos familias se convierten en una",
		"Rodeados de quienes más los quieren en el mundo",
		"El amor de familia es lo que hace grande a una boda",
		"Que los lazos de hoy sean inquebrantables mañana",
		"Toda la gente que más los quieren está aquí hoy",
		"Amor de familia: el más incondicional de todos",
		"Hoy todos somos parte de su historia",
		"Los amigos son la familia que uno elige",
		"Gracias a todos los que hicieron posible este día",
		// Nature & beauty
		"Como las flores al sol, el amor siempre busca la luz",
		"El amor florece donde hay tierra fértil y corazón abierto",
		"Que su amor crezca con raíces profundas y ramas al cielo",
		"La luna llena de esta noche brilla para ustedes",
		"Que su amor sea como el mar: profundo, amplio e interminable",
		"Las estrellas se alinearon para este día",
		"Como las olas que siempre vuelven a la orilla, así es su amor",
		"La naturaleza entera celebra con ustedes",
		"El amor es la flor más bella del jardín de la vida",
		"Que su amor florezca en cada nueva estación",
		// Light & humorous
		"Hoy está permitido bailar hasta que los pies pidan permiso",
		"El amor es lo único que se multiplica cuando se divide",
		"El secreto de un buen matrimonio: reírse juntos todos los días",
		"La mejor decisión siempre fue la de ustedes dos",
		"Que nunca se les acaben los pretextos para celebrar",
		"La risa es el sonido del amor en voz alta",
		"Que el humor sea siempre su mejor aliado",
		"Hoy el amor y la alegría comparten el mismo escenario",
		"Nota: los que lloran de emoción ya pueden soltar el pañuelo",
		"Que su historia tenga más risas que películas de comedia",
		// Deep & meaningful
		"El amor verdadero no es perfecto — es real",
		"Eligieron bien: eligieron lo que los hace mejores personas",
		"El amor no es mirarse el uno al otro, es mirar juntos en la misma dirección",
		"Hay personas que llegan a tu vida para quedarse — ellos se eligieron",
		"El amor es la respuesta, sin importar cuál sea la pregunta",
		"Que su amor sea siempre más grande que sus diferencias",
		"El matrimonio no es un destino, es un viaje que empieza hoy",
		"La vida es más bonita cuando se comparte con la persona correcta",
		"Que nunca dejen de ser el mejor lugar del mundo el uno para el otro",
		"Hoy prometieron — y eso lo cambia todo",
		// Classic & timeless
		"Para siempre empieza hoy",
		"El amor es el único bien que se multiplica al repartirse",
		"Donde el amor reina, las distancias no existen",
		"El amor no ve la edad, solo ve el corazón",
		"Unidos en el amor, fuertes en la vida",
		"El amor es la música del alma",
		"Hoy nace una nueva familia",
		"Por amor todo, sin amor nada",
		"El amor es la luz que nunca se apaga",
		"Que su historia de amor sea digna de contarse",
	}

	for _, phrase := range weddingPhrases {
		var existing models.EventPhrase
		if err := db.Where("event_type = ? AND phrase = ?", "WEDDING", phrase).First(&existing).Error; err == gorm.ErrRecordNotFound {
			entry := models.EventPhrase{
				EventType: "WEDDING",
				Phrase:    phrase,
			}
			if err := db.Create(&entry).Error; err != nil {
				slog.Error("error seeding event phrase", "phrase", phrase[:min(20, len(phrase))], "error", err)
			}
		}
	}
	slog.Info("event phrases seeded", "type", "WEDDING", "count", len(weddingPhrases))
}
