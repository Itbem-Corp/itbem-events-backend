# Contrato de autonomía de Delivery

Delivery automatiza preparación, evidencia y ejecución local acotada. Nunca
convierte una autorización de contexto en autorización de publicación.

## Capas de decisión

| Acción | Requisito técnico | Gate humano |
| --- | --- | --- |
| Leer repositorios registrados | `repository:read` | No |
| Crear worktree aislado | `worktree:create` | No, sólo durante implementación aprobada |
| Aplicar un diff validado | `patch:apply` | Plan aprobado |
| Ejecutar validaciones allowlisted | Registro del workspace | No, se registran resultados |
| Stage/commit local | `commit:stage` | Código aprobado |
| Publicar rama | `branch:publish` | Código aprobado y grant de publicación vigente |
| Crear pull request | `pull_request:create` | Código aprobado y grant de publicación vigente |
| Fusionar o desplegar | No es una capacidad del agente | Gate de release y mecanismo externo explícito |

Las capacidades `commit:stage`, `branch:publish` y `pull_request:create` no
se conceden por compatibilidad. Un workspace heredado únicamente recibe
lectura, worktree aislado y aplicación de patch.

## Multi-repo

Una tarea puede congelar varios repositorios. Para planificación, el agente
recibe el contexto mínimo de todos. Exactamente una fuente debe declarar
`metadata.repository_role=primary`; las demás son `supporting`. El plan
aprobado debe declarar una matriz de impacto completa para todos: `changes`,
`consulted` o `untouched`. Cada repositorio marcado `changes` recibe su propio
worktree, patch, validación, diff y evidencia; un repositorio `github://` que
sólo existe como checkpoint remoto nunca puede marcarse como `changes` hasta
registrar un workspace local. Una tarea ambigua se rechaza antes de crear un
worktree.

El registro de un repositorio también es un control: sólo acepta
`workspace://id` registrado en el host o `github://owner/repository`. Un
workspace local se verifica como repositorio Git y, si no se proporcionó una
revisión, fija su SHA actual en ese momento. Los roles, responsabilidades y
dependencias se normalizan antes de guardar; un `github://` sin SHA queda en
`pending_sync` hasta que una persona lo sincronice con la GitHub App. Así una
tarea nunca congela un identificador ambiguo o un repositorio remoto sin
revisión concreta.

Una persona con permiso de gestión puede actualizar el checkpoint de un
`workspace://` y ver sus capacidades configuradas. Esa operación sólo observa
SHA, rama, cambios locales y origen; no puede hacer `fetch`, `pull`, `add`,
`commit` ni `push`. Los cambios locales sin commit se señalan de forma visible
para que el revisor decida si el SHA congelado sigue representando el contexto
correcto.

## Vault-first

Ninguna fase de agente se admite con contexto de repositorio sin un Vault
aprobado para el mismo repositorio y SHA congelado. Un `github://` se resuelve
directamente; un `workspace://` se enlaza exclusivamente mediante el origen
GitHub observado por el operador. El control plane vuelve a calcular el digest
del manifiesto antes de incluirlo y falla cerrado si falta, está obsoleto o no
coincide.

El modelo recibe sólo una proyección acotada del estado vigente del Vault:
entradas, procedencia, ciclo de vida y digest. El historial, identidades de
aprobación y datos operativos permanecen privados; claves con apariencia de
credencial se eliminan también dentro de valores anidados. El Vault es contexto
verificado, nunca autoridad para ampliar el scope ni saltar gates humanos.

## Credenciales y publicación

La publicación debe usar un GitHub App o token de instalación de corta vida,
con permisos de repositorio mínimos. Nunca se guarda un token personal en el
prompt, el input de la tarea, evidencia, logs o variables de frontend.

En local, `scripts/Start-LocalAIControlPlane.ps1` puede leer exclusivamente
`ITBEM_GITHUB_APP_ID`, `ITBEM_GITHUB_INSTALLATION_ID` e
`ITBEM_GITHUB_APP_PRIVATE_KEY` desde el archivo ignorado `.env.ai.local`. No
hereda `MINIMAX_API_KEY` ni ninguna otra variable del agente; en servidores,
esas tres variables llegan directamente desde el gestor de secretos del
control plane.

Un grant de publicación debe registrar: tarea, repositorio, capacidades,
aprobador, caducidad, SHA/base branch y motivo. Cuando expire, el agente sólo
puede conservar el worktree y preparar evidencia; no puede publicar.

Inmediatamente antes de hacer `git add`, `commit` o `push`, el worker consulta
con la GitHub App el SHA actual del branch por defecto. Debe coincidir
exactamente con el SHA base revisado en el grant. Si el branch se movió, el
worker no modifica el worktree: se debe refrescar, rebasear, volver a validar y
abrir un nuevo gate de código. Esto evita publicar un PR construido sobre una
base que ya no fue revisada.

## Auditoría requerida

Cada llamada de IA conserva ejecución, tokens de entrada/salida/caché,
snapshot de precios y referencias privadas de request/response. Cada paso de
Delivery agrega sus costes al resumen de tarea y cada gate conserva la decisión
humana y la evidencia revisada.

La inspección privada se hace por **ejecución**, no sólo por tarea: si una
tarea reintenta o llama al modelo varias veces, cada fila del ledger conserva
su propio request y response. La API verifica primero que la ejecución
pertenezca a una tarea accesible y resuelve únicamente sus referencias
inmutables; el navegador nunca recibe rutas de bucket, claves de objeto ni una
referencia elegida por el usuario.

Cada run guarda el resultado cifrado bajo una ruta inmutable propia. El
`result.json` de tarea se conserva sólo como puntero de recuperación para
entregas al-menos-una-vez; no es la evidencia principal de una ejecución nueva
y no puede sobrescribir la respuesta ya ligada a un cargo del ledger.

En ambientes desplegados el navegador entrega el input mediante una URL S3
firmada de corta vida. `ENV=local` admite además un fallback autenticado y
acotado a 256 KB cuando el navegador no puede alcanzar el endpoint aislado de
un emulador AWS. Ese fallback escribe directamente y cifrado en el bucket privado;
no conserva el cuerpo en la base de datos, logs ni mensajes SQS. No existe en
staging ni producción y no debe convertirse en un canal genérico de prompts.

Cada intento de worker obtiene un lease opaco de 20 minutos antes de hablar
con un proveedor. Un mensaje SQS redeliverado no puede invocar otra vez al
modelo mientras exista un lease activo; sólo un lease vencido puede ser tomado
por un intento nuevo. La respuesta terminal debe presentar el mismo lease, por
lo que un worker antiguo no puede cerrar ni facturar la ejecución de otro.

La visibilidad de cada mensaje SQS se inicia en 15 minutos y el agente la
renueva cada cuatro minutos mientras su tarea continúa activa. Esto protege
llamadas largas al proveedor, QA de navegador y carga de evidencia contra una
redelivery prematura. Si el heartbeat falla, el agente registra la condición y
deja que el contrato de lease, idempotencia y DLQ determine una recuperación
segura; nunca ejecuta dos veces una operación terminal por su cuenta.

El heartbeat operativo del worker es distinto al de visibilidad SQS. Cada 30
segundos el worker publica su provider, modelo, concurrencia y un preflight
anonimo por workspace: disponibilidad local, conteos de validacion/QA y si QA
visual o publicacion estan habilitados. No incluye rutas locales, comandos,
remotes, SHA, ramas, salida de Git, prompts ni secretos. El dashboard debe
tratar una senal ausente como **desconocida**, nunca como preparada; asi un
worker vivo no se confunde con uno listo para ejecutar una entrega.

## Contexto conversacional y repositorios

Cada nueva ejecución recibe una ventana cronológica y acotada de la conversación
del work item. Conserva fase, tipo de autor, contenido y fecha; no incluye IDs
de personas, contactos del cliente ni secretos. Los mensajes son datos no
confiables y no pueden cambiar la política de autonomía.

Los repositorios se congelan como una topología con referencia, revisión, rol y
dependencias. Un plan debe declarar exactamente esa misma matriz: no puede
introducir, omitir o reescribir un repositorio. La señal Git de contexto puede
informar rama de seguimiento y divergencia conocida, pero nunca hace `fetch`,
`pull` ni modifica el workspace original durante la recopilación de contexto.

La selección de archivos no envía el repositorio completo. Primero toma las
rutas que la persona delimitó en el alcance; si éste está vacío, deriva un
conjunto pequeño de términos del objetivo, descripción y resultado esperado de
la tarea. Con esas señales prioriza código seguro relacionado y después los
documentos de arquitectura. Cada excerpt tiene límite de tamaño y el conjunto
total tiene límite de archivos y caracteres. `.env`, credenciales, metadatos
VCS, dependencias, binarios y rutas con nombres sensibles se excluyen antes de
formar el input. Esta selección es una ayuda de relevancia, no una autorización
para descubrir secretos ni para leer rutas fuera del workspace registrado.
## Contexto remoto acotado

Un repositorio remoto `github://` tambien puede aportar orientacion de codigo
si la GitHub App tiene permiso de Contents. Al sincronizarlo, Delivery fija el
commit, elige como maximo ocho archivos de alta senal (README, manifiestos,
entrypoints y docs), limita el contenido total a 16 KiB y aplica la misma
redaccion de secretos que al workspace local. El resultado se declara como
`bounded_source`, es evidencia de solo lectura y nunca permite marcar ese repo
como `changes`, ejecutar comandos, crear worktrees ni publicar. Si Contents no
esta disponible, el checkpoint se conserva como `inventory_only` o
`metadata_only` sin bloquear el proyecto.
