# Flujo vigente de validación del harness

Ejecuta los comandos desde la raíz del backend con el toolchain fijado en `go.mod`. Conserva entradas y resultados en rutas nuevas para cada ejecución. El historial en [HARNESS_EVALUATION.md](HARNESS_EVALUATION.md) describe rondas anteriores; sus cifras y contratos no certifican una revisión posterior.

## Qué demuestra cada resultado

| Capa | Evidencia requerida | Significado de un resultado aprobado |
| --- | --- | --- |
| Regresión del harness | JSONL completo, paquetes, repeticiones y pruebas críticas verificadas para el commit ejecutado | Pasaron los controles de regresión cubiertos; las omisiones siguen explícitas |
| Runtime aislado | Logs de LocalStack/Docker, pruebas obligatorias sin omisiones y artefactos del mismo commit | Funcionaron las operaciones aisladas probadas |
| Correspondencia de claims | Respuesta original del modelo y observación capturada o snapshot de gates | Las afirmaciones estructuradas corresponden a la evidencia suministrada |
| Admisión del reporte | Correspondencia más validación de contrato y guards de ejecución, semántica y screenshots | El reporte satisface los controles implementados; no autoriza merge o release |
| Calidad del modelo | Corpus congelado, respuestas reales, rutas/recibos, uso y costos, resultados por caso | Calidad medida en ese corpus y ejecución, con su denominador y limitaciones |

Una respuesta puede corresponder correctamente a una ejecución QA fallida. Una respuesta con claims correctos puede ser rechazada por un guard de screenshots. Los proveedores falsos prueban el harness, pero sus respuestas no miden la calidad de un modelo real. No proyectes las observaciones para fabricar claims que deberían provenir del modelo.

## Regresión offline y CI

```powershell
./scripts/Test-AgentHarness.ps1
./scripts/Test-AgentHarnessIsolation.ps1
./scripts/Test-HarnessSemantics.ps1
./scripts/Test-HarnessCosts.ps1
```

El runner local repite sus seis paquetes canónicos y conserva omisiones. `-RequireNoSkips` exige un entorno capaz de ejecutar todas sus pruebas; una omisión no equivale a protección verificada. CI amplía la cobertura a once paquetes y exige pruebas críticas por nombre en cada repetición. La lista autoritativa y los comandos están en [build-ai-agent.yml](../../.github/workflows/build-ai-agent.yml).

Antes de declarar un commit verificado, comprueba el SHA ejecutado, la conclusión de los checks y el contenido de los artefactos descargados. Usa `cmd/verify-test-evidence` con los paquetes, repeticiones y nombres exigidos por ese workflow. Comprueba también las dos repeticiones de las cinco pruebas de runtime sin omisiones y los SHA-256 de los binarios. Un resultado anterior no certifica el nuevo head.

## Replay de correspondencia QA

Este ejemplo usa exclusivamente fixtures sintéticas. El directorio de salida debe existir y el archivo de salida debe ser nuevo:

```sh
go run ./cmd/score-qa-grounding -observation internal/qaevidence/testdata/grounding/observation.json -claims internal/qaevidence/testdata/grounding/grounded-claims.json -output <directorio-existente>/qa-score.json
```

El ejemplo produce correspondencia aprobada con veredicto QA observado **failed**. Lee `score_kind`, `passed`, los dos veredictos y los hashes por separado. El CLI compilado retorna 0 por correspondencia aprobada, 1 por entrada inválida o discrepancia y 2 por invocación o publicación fallida. `go run` puede convertir un código no cero del programa en su propio código 1; usa el binario compilado cuando necesites distinguir 1 de 2. Conserva también los scores fallidos. Los hashes identifican los bytes y no autentican su origen.

QA y summaries nuevos requieren sus contratos explícitos de claims. Guarda el original antes de normalizar y conserva diagnóstico, uso y referencias privadas cuando falle la admisión. Los resultados históricos sin observación siguen siendo no disponibles para grounding; no deben etiquetarse como aprobados al reproducirlos.

## Evaluación de modelos y entrega de evidencia

Para screening y caché utiliza [scripts/EVALUATION.md](../../scripts/EVALUATION.md): incluye corpus congelado, versión de lote, recibos únicos, hashes de entradas, causas de fallo y publicación sin sobrescritura. Los motivos pueden solaparse; no sumes sus conteos como total de casos fallidos. Distingue costo conocido, cero registrado y costo desconocido.

Una evaluación nueva de modelos reales requiere una tarea sintética autorizada mediante el worker y gateway centrales, con presupuesto y registro de uso. Los antiguos selectores directos `-LiveMiniMax` y `-LiveProvider` están retirados. Estos comandos offline no ejecutan esa evaluación ni habilitan un fallback directo.

Al entregar una mejora para revisión, registra el commit, el cambio de comportamiento, comandos y resultados, omisiones y límites. Enlaza la evidencia vigente y conserva la anterior como historial. La descripción del PR debe explicar la implementación final; la cronología de pruebas pertenece a la auditoría. Un check verde no mide por sí solo mejora de calidad ni concede autoridad de publicación o despliegue.
