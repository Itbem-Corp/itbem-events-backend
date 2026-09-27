# Compatibilidad del runtime activo de agentes

## Resultado de la inspección

Revalidación adicional de solo lectura el **26 de septiembre de 2026 a las
06:48 UTC**: las cinco unidades siguen `active` y ejecutan
`/opt/itbem-ai-agent/current/itbem-ai-agent`. Los PID observados fueron
engineering `921`, orchestration `928`, qa `920`, release `1062` y review
`1121`. El destino de `current` conserva SHA-256
`eb711520d0c0b3bb1ee177c14a8cae73a4ce767279bc555b29e061d37b1caa57`; sus
metadatos continúan indicando Go 1.25.13, revisión
`32ae78e739c6b062304bf5a866d6d2a662a31202` y `vcs.modified=false`. No se
leyeron variables de entorno ni se inspeccionó el inode de `/proc` de los
procesos.

Revalidación de solo lectura realizada el 26 de septiembre de 2026. No se
leyeron archivos de entorno, secretos ni logs, y no se ejecutaron agentes,
solicitudes a AWS/SQS, despliegues ni llamadas a proveedores.

En WSL Ubuntu se observaron las cinco unidades systemd en estado
`ActiveState=active`:

- `itbem-ai-agent@engineering.service`
- `itbem-ai-agent@orchestration.service`
- `itbem-ai-agent@qa.service`
- `itbem-ai-agent@release.service`
- `itbem-ai-agent@review.service`

Las cinco siguen reportando como `ExecStart`:
`/opt/itbem-ai-agent/current/itbem-ai-agent`. El enlace `current` resolvió a
`/opt/itbem-ai-agent/releases/eb711520d0c0b3bb1ee177c14a8cae73a4ce767279bc555b29e061d37b1caa57/itbem-ai-agent`.
El SHA-256 revalidado en esa ruta fue
`eb711520d0c0b3bb1ee177c14a8cae73a4ce767279bc555b29e061d37b1caa57`, sin
cambio respecto de la inspección anterior. `go version -m` volvió a reportar
Go 1.25.13, módulo `events-stocks/cmd/itbem-ai-agent`, revisión Git
`32ae78e739c6b062304bf5a866d6d2a662a31202` (13 de septiembre de 2026) y
`vcs.modified=false`. Por permisos del usuario de inspección no fue posible
resolver `/proc/<pid>/exe`; por tanto, se verificaron el estado/PID de systemd,
su ruta `ExecStart`, el destino actual de `current` y el build de ese destino,
pero no el inode de ejecutable abierto por cada proceso individual.

El checkout inspeccionado está en
`4de8bdd335e9822caac0d1daebe38cf1fa9832b5` (23 de septiembre de 2026), con
cambios locales. `eventiapp.workspace.json` declara Go 1.25.12. Así que las
unidades activas ejecutan una release instalada en `/opt`, no el código actual
del checkout. No se verificó que el contenido de esa release sea idéntico a
ningún artefacto local.

El README de `itbem-ai-local-agent/` lo marca como implementación Python
archivada y apunta al runtime Go de
`cmd/itbem-ai-agent`. No debe usarse como referencia de ejecución actual.
`compose.local.yml` levanta API/infraestructura, no el proceso worker; el
runtime Go se ejecuta por separado.

## Diferencia de contrato que bloquea asumir compatibilidad

Se inspeccionó el código del commit exacto incluido en el build activo. En esa
revisión, el `TaskMessage` del worker contiene `task_id`, `operation`,
`input_ref`, `attempt` y campos generales del sobre, pero no
`project_id`, `agent_key`, `plan_step_id` ni `target_machine_id`. Su
`DecodeTaskMessage` llama a `json.Decoder.DisallowUnknownFields()`.
El mismo commit no contiene `ClaimPlanStep`, `FencingToken` ni el callback de
claim de pasos.

El contrato del checkout actual sí tiene esas pistas de proyecto/paso/máquina
en `internal/automationagent/worker.go`; el scheduler las añade al mensaje de
paso hijo en `controllers/automation/automation.go`, función
`planStepChildQueueMessage`. El worker actual reclama el paso por el callback
de `internal/automationagent/step_callback.go`; la validación transaccional del
claim está en `controllers/automation/plan_step_runtime.go`, función
`ClaimDeliveryPlanStep`, y la verificación de asignación/failover en
`services/deliveryplansteps/orchestration.go`, función
`ValidateAssignmentWorker`.

El código fuente actual también incorpora `protocols_json` al heartbeat. La
API acepta el protocolo conocido `delivery.plan_steps.v1` y el control de
compatibilidad del claim exige que el worker autenticado lo reporte en un
heartbeat reciente. La release instalada observada arriba es anterior a este
contrato: que el checkout o la API actuales publiquen `protocols_json` no
actualiza ni vuelve compatible el binario que ya ejecutan las cinco unidades.
La inspección fue de solo lectura y no confirmó que esas unidades reporten el
nuevo protocolo.

**Riesgo:** si un worker del build antiguo recibe un mensaje del contrato nuevo
con esos campos, el decoder estricto puede rechazarlo como inválido. Aunque se
omitieran los campos, ese build no implementa el claim ni los leases/fencing de
pasos actuales. En una cola compartida, esto puede traducirse en reintentos y
eventual DLQ, o en incompatibilidad funcional del despacho por pasos. No
publicar trabajo que requiera `delivery.plan_steps.v1` en una cola compartida
hasta drenar y actualizar todos sus consumidores antiguos, o hasta aislar el
nuevo protocolo en un destino independiente y probado. No mezclar consumidores
de ambas revisiones en una cola hasta completar y probar el rollout coordinado.

La revisión activa antigua también inicializa el callback HTTP con el secreto
compartido de runtime (`NewHTTPCallback(..., CallbackSecret, ...)`); no es el
contrato de identidad de máquina registrada que aparece en el checkout actual.
El modo de transporte efectivo de las unidades activas (SQS/AWS o gateway HTTP)
**queda pendiente**: no se inspeccionaron variables de entorno ni sus archivos.

## Comprobaciones de solo lectura

Ejecutar estas verificaciones desde el host autorizado y contra el entorno
correcto. No imprimir `Environment`, archivos de entorno, valores de tokens,
claves, cuerpos de mensajes ni URLs/ARNs si no es necesario.

### Unidades y versión del binario

```bash
systemctl is-active itbem-ai-agent@engineering.service
systemctl show itbem-ai-agent@engineering.service \
  -p Id -p MainPID -p User -p ExecStart --no-pager
readlink -f /opt/itbem-ai-agent/current/itbem-ai-agent
go version -m /opt/itbem-ai-agent/current/itbem-ai-agent
sha256sum /opt/itbem-ai-agent/current/itbem-ai-agent
```

`systemctl show` se limita deliberadamente a identidad/proceso/ejecutable; no
añadir `-p Environment` ni volcar propiedades de entorno. Repetir la
verificación para cada rol. Comparar `vcs.revision` con el commit aprobado y
con el manifiesto/checksum de la release, no solo con el nombre `current`.

### Cola SQS y DLQ (solo si el transporte confirmado es SQS)

Usar el perfil/rol temporal y la región que el operador ya aprobó. Mantener las
URLs en variables protegidas y evitar mostrarlas en consola o logs. Las
siguientes consultas son de atributos agregados: no reciben ni modifican
mensajes.

```bash
aws sqs get-queue-attributes \
  --queue-url "$ITBEM_AI_QUEUE_URL" \
  --attribute-names ApproximateNumberOfMessages ApproximateNumberOfMessagesNotVisible ApproximateNumberOfMessagesDelayed \
  --query Attributes --output json
```

Para la DLQ, resolver su URL desde la configuración de redrive autorizada sin
imprimirla y consultar los mismos tres atributos. Ejemplo Bash: la salida
visible es únicamente el conteo agregado de la DLQ; `jq` debe estar instalado.

```bash
set -euo pipefail
REDRIVE_JSON="$(aws sqs get-queue-attributes \
  --queue-url "$ITBEM_AI_QUEUE_URL" \
  --attribute-names RedrivePolicy \
  --query 'Attributes.RedrivePolicy' --output text)"
DLQ_ARN="$(printf '%s' "$REDRIVE_JSON" | jq -er 'fromjson.deadLetterTargetArn')"
DLQ_NAME="${DLQ_ARN##*:}"
DLQ_URL="$(aws sqs get-queue-url --queue-name "$DLQ_NAME" \
  --query QueueUrl --output text)"
aws sqs get-queue-attributes \
  --queue-url "$DLQ_URL" \
  --attribute-names ApproximateNumberOfMessages ApproximateNumberOfMessagesNotVisible ApproximateNumberOfMessagesDelayed \
  --query Attributes --output json
unset REDRIVE_JSON DLQ_ARN DLQ_NAME DLQ_URL
```

El operador debe confirmar cuenta/región/perfil antes de ejecutar estas
consultas. Las cantidades SQS son aproximadas. No usar `receive-message` para
inspeccionar la cola: puede cambiar visibilidad, y el cuerpo puede contener
datos del trabajo. No purgar ni borrar mensajes como método de diagnóstico. Si
el transporte activo es gateway HTTP, usar un endpoint de estado agregado
autorizado; su existencia/configuración para las unidades actuales está
pendiente de validar.

## Checklist de rollout compatible

1. Inventariar las cinco unidades, usuarios, PID, ruta real del ejecutable,
   SHA-256 y `vcs.revision`; registrar el resultado sin incluir entorno ni
   secretos.
2. Confirmar con el owner de control plane el transporte y el destino de cola
   de cada servicio mediante una comprobación sanitizada que exponga solo
   `aws`/`gateway`, región y un identificador de cola redactado. No hacer dump
   del entorno del proceso.
3. Definir un contrato de versión explícito para mensaje/worker. El decoder
   estricto es intencional: actualizar workers y control plane coordinadamente
   o introducir compatibilidad versionada probada antes de emitir campos
   nuevos. Confirmar además que cada consumidor elegible anuncia
   `delivery.plan_steps.v1` en un heartbeat autenticado y reciente; el campo
   `protocols_json` en el código fuente no demuestra que una release instalada
   lo soporte.
4. En entorno aislado, probar end-to-end un mensaje de paso realista con
   `project_id`, perfil, `plan_step_id` y máquina objetivo; comprobar que la
   versión nueva reclama el paso aprobado, respeta dependencias, renueva el
   lease y rechaza fencing expirado/incorrecto. Incluir una prueba que confirme
   que el worker antiguo falla de forma visible ante campos desconocidos.
5. Antes de cambiar consumidores, drenar y detener los workers incompatibles
   para esa cola siguiendo el procedimiento operativo aprobado. No arrancar
   otro consumidor a ciegas ni purgar la cola para forzar la migración.
6. Desplegar artefacto cuyo checksum y revisión correspondan al código aprobado;
   verificar las unidades una por una y confirmar que no quedan servicios de
   revisión antigua escuchando la misma cola.
7. Observar métricas agregadas de cola/DLQ y estados de asignación/lease desde
   APIs autorizadas durante el rollout. Investigar mensajes rechazados y
   conflictos antes de reintentar; no copiar payloads a logs o tickets.
8. Confirmar que las credenciales de proveedor permanecen en el gateway
   central, que el worker usa identidad de máquina registrada y que no se
   introducen secretos en environment dumps, trazas o mensajes.

## Fuentes inspeccionadas

- `eventiapp.workspace.json`, `compose.local.yml`.
- `itbem-ai-local-agent/README.md` (solo para confirmar su estado archivado).
- `itbem-events-backend/cmd/itbem-ai-agent/main.go`.
- `itbem-events-backend/internal/automationagent/{runtime.go,worker.go,queue.go,step_callback.go,step_execution.go}`.
- `itbem-events-backend/controllers/automation/{automation.go,plan_step_runtime.go,dispatch_queue.go}`.
- `itbem-events-backend/services/deliveryplansteps/orchestration.go`.
- WSL `systemctl show` de las cinco unidades, `go version -m` y SHA-256 del
  ejecutable activo; metadatos Git de los commits activo y del checkout.

No se inspeccionaron cuerpos de cola, valores de entorno, secretos, archivos
`.env`, logs de agente ni configuración sensible del servicio. Tampoco se
ejecutaron pruebas o acciones de rollout en este frente.

## Anexo de validación posterior — 26 de septiembre de 2026

Revalidación de solo lectura a las **13:30 UTC**: las cinco unidades siguieron
activas con los mismos PID (engineering `921`, orchestration `928`, qa `920`,
release `1062`, review `1121`), el mismo destino de `current` y el mismo SHA-256.
No se desplegó ni reinició ningún worker. `docker ps` quedó vacío después de
las pruebas.

Se ejecutó el paquete de integración completo desde WSL Ubuntu con el harness
de testcontainers: `go test -tags=integration ./integration -count=1` → PASS
(40.098 s). El harness creó y limpió su PostgreSQL/miniredis desechables. Esto
valida los escenarios de concurrencia de pasos listos, gates de dependencias,
failover, ámbitos organizacionales, historial/cursor, trazas y atribución de
costes cubiertos por esas pruebas; no valida comunicación con los cinco
workers activos, cuyo binario permanece en la revisión incompatible indicada
arriba. La suite completa del backend `go test ./...` también pasó.

La corrida inicial detectó tres expectativas de integración desactualizadas,
no tres defectos de producción: (1) la historia del agente ahora incluye como
contexto la decisión humana del gate del mismo trabajo; (2) el contrato de
trazas conserva `kind: task_event` y expresa `created` en `event_type`; y (3)
la fixture de migración modelaba `worker_id UNIQUE` en vez del índice
`uniqueIndex` que confirma el modelo de la release desplegada. Se ajustaron las
tres fixtures/assertions al contrato/modelo vigente y el paquete completo pasó
en la segunda ejecución.

Se añadió `summary.unpriced_executions` al overview de costes, calculado sobre
la misma consulta filtrada/autorizada y la misma instantánea que sus totales.
Cuenta bases de precio `legacy`, vacías y `unpriced`; el frontend bloquea la
certeza de presupuesto cuando esa cobertura no está resuelta, incluso si los
registros desconocidos no están en la página visible. Verificación adicional:
pruebas unitarias de costes frontend (18/18), `npm run typecheck` y ESLint
focalizado → PASS. Los importes sin precio se presentan como no disponibles,
nunca como cero.

**Riesgo operativo pendiente:** las pruebas de integración usan el checkout y
un worker simulado dentro del harness; no cambian la incompatibilidad del
binario activo. Antes de ofrecer ejecución nueva por pasos en esas cinco
instancias aún hace falta un rollout coordinado autorizado y una validación
end-to-end contra workers que anuncien `delivery.plan_steps.v1`. La ejecución
de este anexo no inspeccionó secretos, variables de entorno, payloads ni logs,
y no realizó ese rollout.
