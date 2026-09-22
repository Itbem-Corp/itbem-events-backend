# GitHub App para publicación controlada

La plataforma no publica ni sincroniza código con llaves SSH ni tokens
personales. Usa tres roles de GitHub App privados e independientes, instalados
sólo en repositorios autorizados: **Source** para obtener código,
**Reviewer** para revisiones y **Release** para publicación controlada.
Compartir la App Source con Reviewer o Release rompe la separación de
privilegios y no es una configuración válida.

## Permisos mínimos de las Apps

Source App:

- **Contents: Read-only**: clonar/fetch únicamente el repositorio registrado
  por el operador y crear contexto de onboarding en un SHA exacto.
- **Metadata: Read-only**: obligatorio para GitHub Apps.

No conceder Pull requests, Checks, Actions, Workflows, Deployments,
Environments, administración, secrets ni permisos de organización. El token
es de corta vida, se restringe al repositorio exacto y se entrega sólo al
proceso temporal de Git askpass; nunca se escribe en la URL remota. En Linux,
cada lane que lea un workspace de GitHub usa un archivo de llave propio aun si
las llaves pertenecen a la misma App Source de solo lectura.

Reviewer App:

- **Contents: Read-only**: obtener y volver a comprobar el diff exacto.
- **Checks: Read and write**: publicar un único check
  `Bema Review / exact-sha` ligado al head exacto. Sólo concluye `success`
  cuando una identidad Reviewer independiente aprueba el SHA o deja sólo
  hallazgos concretos `low` de mantenibilidad, sin huecos de evidencia. Esos
  comentarios permanecen visibles pero no bloquean; cualquier hallazgo de
  seguridad, corrección, confiabilidad, rendimiento o cobertura (incluso
  `low`), un cambio solicitado, un bloqueo o una auto-revisión concluye
  `failure`.
- **Pull requests: Read and write**: leer el head/autor y publicar únicamente
  `COMMENT`, `APPROVE` o `REQUEST_CHANGES`.
- **Metadata: Read-only**: obligatorio para GitHub Apps.

Release App:

- **Contents: Read and write**: subir únicamente la rama autorizada.
- **Pull requests: Read and write**: crear el PR de la rama autorizada.
- **Metadata: Read-only**: obligatorio para GitHub Apps.

No conceder administración, workflows, secretos, acciones, deployments ni
permisos de organización salvo que un flujo distinto los requiera y se diseñe
con otro grant. La evidencia de seguridad del Gatekeeper proviene de scanners
locales configurados y no necesita GitHub Advanced Security.

El worker determinista de release usa un grant de sólo lectura separado para
comprobar el contrato de entorno aprobado: **Contents: read** para confirmar el
workflow en el SHA exacto, **Actions: read** para confirmar el environment y
**Environments: read** sólo cuando la política declara nombres de secrets o
variables requeridos. Las APIs devuelven metadatos/nombres, nunca valores. El
worker conserva únicamente los nombres requeridos que faltan; no persiste el
inventario completo de la organización. Este grant no permite ejecutar el
workflow, modificar secrets, administrar environments, mergear ni desplegar.

## Instalación inicial (una sola vez)

1. En la organización de GitHub, crear tres **GitHub Apps** privadas: Source, Reviewer y Release.
2. En **Repository access**, seleccionar **Only select repositories** y añadir únicamente los repositorios que ese entorno necesita leer, revisar o publicar. No elegir acceso a todos los repositorios de la organización.
3. Conceder a cada App sólo sus permisos y generar llaves privadas distintas. Las llaves no se pegan en el dashboard ni se comparten entre procesos. La App Source puede tener una llave distinta por lane Linux.
4. Instalar cada App en la organización y anotar el identificador de instalación. Mantener una instalación distinta por entorno cuando producción y pruebas no compartan el mismo perímetro.
5. Guardar los secretos del siguiente apartado en el control plane y, en cada lane que use un workspace GitHub, ejecutar la preflight `--github-auth-probe`. La comprobación usa un token efímero y una lectura mínima; no lista repositorios ni muestra credenciales.

Una verificación fallida no habilita publicación. El error visible es deliberadamente genérico: las causas detalladas se revisan sólo en la configuración del runtime, nunca desde el navegador.

## Variables del control plane

En producción, inyectarlas desde el gestor de secretos del runtime. Para una
prueba local, `scripts/Start-LocalAIControlPlane.ps1` puede leerlas de
`.env.ai.local`, archivo que no se versiona ni se expone como contexto al
modelo.

```dotenv
ITBEM_GITHUB_APP_ID=12345
ITBEM_GITHUB_INSTALLATION_IDS=67890
ITBEM_GITHUB_APP_PRIVATE_KEY="-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----" # gitleaks:allow — inert documentation placeholder
ITBEM_GITHUB_API_BASE_URL=https://api.github.com

# Source App: required by a Linux lane only when its workspace registry
# contains a GitHub repository. This identity is read-only and separate from
# the Reviewer/Release App above.
ITBEM_GITHUB_SOURCE_APP_ID=23456
ITBEM_GITHUB_SOURCE_INSTALLATION_IDS=67890
ITBEM_GITHUB_SOURCE_APP_PRIVATE_KEY="-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----"
ITBEM_GITHUB_SOURCE_API_BASE_URL=https://api.github.com

# Optional automatic review ingress; disabled when either is absent.
GITHUB_REVIEW_WEBHOOK_SECRET=generate-a-dedicated-random-secret
GITHUB_REVIEW_REPOSITORIES=itbem/itbem-events-backend,itbem/dashboard
```

Para la API backend de producción, configura los tres valores
`ITBEM_GITHUB_SOURCE_*` como secretos protegidos del environment `production`
en `Itbem-Corp/itbem-events-backend`. El workflow los escribe sólo en el
archivo temporal de entorno con modo `0600` y rechaza el despliegue si falta
alguno. El PEM debe conservar los escapes literales `\\n` para permanecer en
una única línea del Docker env-file. Esta copia del API sólo sirve para la
inspección estática del onboarding y no se sustituye por la App de Reviewer ni
la de Release.

En local, el archivo es `itbem-events-backend/.env.ai.local`. El script de
control plane importa exclusivamente las credenciales de App necesarias para
la operación solicitada; no transfiere `MINIMAX_API_KEY` ni otros secretos del
archivo al proceso que expone la API del dashboard.

El worker firma un JWT de App de menos de diez minutos y solicita un token de
instalación de duración limitada para cada publicación. El token no se guarda
en PostgreSQL, S3, output del agente, screenshots, prompts ni logs.

## Revisión automática de pull requests

La Reviewer App recibe el webhook `pull_request` con
content type `application/json`, el secreto independiente
`GITHUB_REVIEW_WEBHOOK_SECRET`, y como URL:

`/api/internal/github/pull-request-review`

La entrada permanece apagada hasta que el secreto y la allow-list exacta
`GITHUB_REVIEW_REPOSITORIES` estén presentes. Sólo acepta acciones `opened`,
`reopened`, `ready_for_review` y `synchronize` de PRs no-draft. Verifica la
firma HMAC SHA-256 sobre el cuerpo crudo, descarga un diff limitado de la
comparación exacta base SHA → head SHA, y crea un único task `code.review` por
`repository + PR number + head SHA`. La redelivery no crea un segundo task;
un commit nuevo sí recibe una revisión nueva. El webhook no ejecuta código ni
publica directamente: congela diff, digest, instalación y SHA, y deposita el
trabajo en la lane Review.

El evento de instalación `ping` también exige la firma HMAC válida y un único
cuerpo JSON. Responde `200` con estado `ready`, no consulta GitHub, no crea una
tarea y no toca la cola. Una entrega firmada de otro tipo (por ejemplo,
`check_suite`) o de un PR ineligible (cerrado, draft, fuera de allow-list o con
SHA inválido) responde `202` con un estado fijo de ignorado; no se decodifica el
evento ajeno, no se consulta GitHub y no se crea ni altera ningún task. Esto
evita redeliveries inútiles sin convertir eventos no admitidos en autoridad de
revisión. Cuerpos malformados o firmas inválidas siguen fallando cerrados.

El despliegue de producción autentica como la misma GitHub App y, después de
promover el SHA exacto, redeliverya preferentemente su último evento `ping`.
Las Apps sin un `ping` histórico usan su entrega aceptada más reciente; la
deduplicación por repositorio, PR y head SHA impide crear una segunda tarea.
El workflow sólo queda verde si GitHub recibe una respuesta `2xx`; un secreto
desincronizado, una URL rota o un ingress no disponible bloquean la evidencia
operativa aunque `/health` permanezca saludable. La prueba no imprime el
secreto, el payload ni la llave privada y no crea una completion del proveedor.
La llave puede llegar como PEM multilínea desde un archivo root-managed o como
el formato de una sola línea con `\\n` explícitos requerido por el `env-file`
de producción; el verificador restaura el framing antes de firmar y nunca
registra el material normalizado.

Después de validar la salida estructurada contra ese diff, un relay
determinista obtiene un token efímero restringido al repositorio, vuelve a
comprobar que el PR sigue abierto y en el mismo SHA, y publica la revisión. Un
marcador de sujeto+payload hace el efecto idempotente tras reinicios. Un
resultado diferente para el mismo sujeto falla cerrado, salvo un reintento
explícitamente autorizado de una revisión que ya falló. Si la App Reviewer
fuera autora del PR, un `APPROVE` se degrada a `COMMENT` y la revisión
automática queda bloqueada. Después publica el check `Bema Review / exact-sha`
con la misma identidad, SHA y digests. El check nunca usa una conclusión
neutral: `success` significa una revisión independiente exacta que aprobó o
sólo dejó una nota de mantenibilidad baja sin hueco de evidencia; `failure`
mantiene el merge cerrado. Los repositorios que habiliten la ruta autónoma
deben exigir ese check, fijarlo a la Reviewer App y no exigir además una review
humana rutinaria; las políticas de riesgo pueden conservar aprobación humana.

El reintento conserva la tarea fallida y su evidencia privada. Si y sólo si
produce un nuevo veredicto válido para el mismo sujeto, SHA y App Reviewer,
puede actualizar el único check previo que estaba en `failure`. Nunca puede
sustituir un check ya `success`, y una redelivery ordinaria no puede cambiar
un resultado distinto.
PostgreSQL conserva sólo identidad, URLs, conclusión y digests públicos; la
prosa completa permanece en evidencia privada.

La entrada admite ráfagas breves normales de GitHub, pero limita el tráfico por
origen antes de analizar el cuerpo. Si GitHub recibe `429`, debe reintentar el
mismo delivery; la deduplicación por commit conserva esa reentrega segura.

Si una revisión llega a estado `failed` (por ejemplo, una respuesta del modelo
no cumple el contrato o una dependencia permanente falló), GitHub no debe
forzarla con una nueva entrega. Un administrador de plataforma —o el gestor
autorizado del proyecto cuando el task pertenece a Delivery— puede usar
`POST /api/automation/tasks/:id/retry-code-review`. Esto crea un nuevo job
auditado con la misma referencia privada y el mismo diff congelado; no vuelve
a descargar el PR ni altera base/head SHA. El resultado fallido original queda
intacto para diagnóstico. Un commit distinto sigue siendo el único motivo para
una revisión automática nueva.

## Controles que siguen siendo obligatorios

La identidad que publica el último cambio revisable debe ser distinta de la
persona que lo aprueba. En repositorios con
`require_last_push_approval`, Engineer publica como su identidad técnica y la
revisión humana se registra después sobre ese head exacto. Un commit vacío no
se usa para cambiar esta procedencia: GitHub puede conservar como autoridad el
último push que realmente modificó el diff.

1. Plan aprobado por una persona.
2. Implementación y revisión de código registrada.
3. Grant temporal, con repositorio, SHA, rama, capacidades, motivo y caducidad.
4. Token de instalación de GitHub App válido en el momento de publicar.
5. La operación `delivery.publish` revalida el grant y el worktree, crea un
   commit local si hay cambios, publica exclusivamente esa rama y crea el PR
   sólo si ambas capacidades fueron otorgadas. Un reintento busca el PR abierto
   de la misma rama en vez de crear un duplicado.
6. Reviewer nunca modifica código, mergea ni despliega. Release sólo actúa
   cuando el Gatekeeper determinista confirma todas las puertas del SHA final.

## Operación y trazabilidad

El portafolio de Automation expone a administradores una cola compacta de
revisiones webhook: task, repositorio, PR, head SHA, estado y, cuando existe,
la identidad y URL pública de la revisión. La proyección cruza el correlation
ID del task con la publicación persistida y rechaza cualquier repo, PR, SHA,
evento o URL que no coincida. Nunca incluye prompts, diff, findings, errores,
installation IDs, referencias de objetos ni credenciales. El dashboard usa
esta cola como fuente del estado en tiempo real y enlaza la evidencia pública
exacta de GitHub.

El listado general `GET /api/automation/tasks` también es deliberadamente
estrecho: sólo devuelve estado operativo, proveedor/modelo, intentos,
timestamps y booleanos de resultado/error. La evidencia completa continúa
detrás de lecturas autorizadas por task; no debe reconstruirse en una ruta de
polling amplia.

En el dashboard, tras el gate de código, la tarea entra a `preview_pending`.
Con un grant vigente aparece **Publicar rama y crear PR**. No consume tokens ni
coste de IA: es una operación determinista. Su resultado privado y el cambio
trazable registran grant, SHA base, SHA del commit, rama y URL del PR; nunca el
token de instalación ni la salida de comandos.
