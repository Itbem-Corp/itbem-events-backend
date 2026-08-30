# GitHub App para publicación controlada

La plataforma de Delivery no debe publicar con la llave SSH ni con un token
personal de una persona. El worker usa una GitHub App instalada sólo en los
repositorios que Delivery puede publicar.

## Permisos mínimos de la App

- **Contents: Read and write**: subir únicamente la rama autorizada.
- **Pull requests: Read and write**: crear el PR de la rama autorizada.
- **Metadata: Read-only**: obligatorio para GitHub Apps.

No conceder administración, workflows, secretos, acciones, deployments ni
permisos de organización salvo que un flujo distinto los requiera y se diseñe
con otro grant. La evidencia de seguridad del Gatekeeper proviene de scanners
locales configurados y no necesita GitHub Advanced Security.

## Instalación inicial (una sola vez)

1. En la organización de GitHub, crear una **GitHub App** privada para Delivery.
2. En **Repository access**, seleccionar **Only select repositories** y añadir únicamente los repositorios que ese entorno puede publicar. No elegir acceso a todos los repositorios de la organización.
3. Conceder sólo los permisos de la sección anterior y generar una llave privada de la App. La llave no se descarga ni se pega en el dashboard.
4. Instalar la App en la organización y anotar el identificador de instalación. Mantener una instalación distinta por entorno cuando producción y pruebas no compartan el mismo perímetro.
5. Guardar los tres secretos del siguiente apartado en el control plane y, desde **Delivery → una tarea → Integración de publicación**, usar **Verificar conexión**. La comprobación usa un token efímero y una lectura mínima; no lista repositorios ni muestra credenciales.

Una verificación fallida no habilita publicación. El error visible es deliberadamente genérico: las causas detalladas se revisan sólo en la configuración del runtime, nunca desde el navegador.

## Variables del control plane

En producción, inyectarlas desde el gestor de secretos del runtime. Para una
prueba local, `scripts/Start-LocalAIControlPlane.ps1` puede leerlas de
`.env.ai.local`, archivo que no se versiona ni se expone como contexto al
modelo.

```dotenv
ITBEM_GITHUB_APP_ID=12345
ITBEM_GITHUB_INSTALLATION_ID=67890
ITBEM_GITHUB_APP_PRIVATE_KEY="-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----"
ITBEM_GITHUB_API_BASE_URL=https://api.github.com

# Optional automatic review ingress; disabled when either is absent.
GITHUB_REVIEW_WEBHOOK_SECRET=generate-a-dedicated-random-secret
GITHUB_REVIEW_REPOSITORIES=itbem/itbem-events-backend,itbem/dashboard
```

En local, el archivo es `itbem-events-backend/.env.ai.local`. El script de control plane importa exclusivamente las tres credenciales de la App; no transfiere `MINIMAX_API_KEY` ni otros secretos del archivo al proceso que expone la API del dashboard.

El worker firma un JWT de App de menos de diez minutos y solicita un token de
instalación de duración limitada para cada publicación. El token no se guarda
en PostgreSQL, S3, output del agente, screenshots, prompts ni logs.

## Revisión automática de pull requests

La misma GitHub App puede habilitar revisión automática con permisos mínimos
**Pull requests: Read-only** y **Metadata: Read-only** (Contents no es
necesario para esta ruta). Configura en GitHub un webhook `pull_request` con
content type `application/json`, el secreto independiente
`GITHUB_REVIEW_WEBHOOK_SECRET`, y como URL:

`/api/internal/github/pull-request-review`

La entrada permanece apagada hasta que el secreto y la allow-list exacta
`GITHUB_REVIEW_REPOSITORIES` estén presentes. Sólo acepta acciones `opened`,
`reopened`, `ready_for_review` y `synchronize` de PRs no-draft. Verifica la
firma HMAC SHA-256 sobre el cuerpo crudo, descarga un diff limitado de la
comparación exacta base SHA → head SHA, y crea un único task `code.review` por
`repository + PR number + head SHA`. La redelivery no crea un segundo task;
un commit nuevo sí recibe una revisión nueva. El webhook no escribe comentarios,
no aprueba, no publica y no ejecuta código: deposita la revisión privada en la
misma cola única del agente.

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

1. Plan aprobado por una persona.
2. Implementación y revisión de código registrada.
3. Grant temporal, con repositorio, SHA, rama, capacidades, motivo y caducidad.
4. Token de instalación de GitHub App válido en el momento de publicar.
5. La operación `delivery.publish` revalida el grant y el worktree, crea un
   commit local si hay cambios, publica exclusivamente esa rama y crea el PR
   sólo si ambas capacidades fueron otorgadas. Un reintento busca el PR abierto
   de la misma rama en vez de crear un duplicado.
6. Merge y deployment siguen fuera de las capacidades del agente.

## Operación y trazabilidad

En el dashboard, tras el gate de código, la tarea entra a `preview_pending`.
Con un grant vigente aparece **Publicar rama y crear PR**. No consume tokens ni
coste de IA: es una operación determinista. Su resultado privado y el cambio
trazable registran grant, SHA base, SHA del commit, rama y URL del PR; nunca el
token de instalación ni la salida de comandos.
