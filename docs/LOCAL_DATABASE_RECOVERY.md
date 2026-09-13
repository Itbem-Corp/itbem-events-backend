# Local PostgreSQL recovery

## Why the control-plane starter stops early

Docker can report the PostgreSQL container as `healthy` while the database is
unable to execute a real query. `Start-LocalAIControlPlane.ps1` therefore
ensures its own sibling database exists in the repository's disposable Compose
PostgreSQL container, then runs one read-only `SELECT 1` against it before it
starts the API. The default `events_ai_local` database is created only when it
is absent; existing databases, schemas, volumes, and migrations are never
reset or modified by that bootstrap.

When an explicit `-DatabaseProbeContainer` is supplied, the script does not
create anything. It stays read-only because that named container can be an
existing integration database rather than the repository's disposable Compose
cluster.

## Safe diagnosis

From `itbem-events-backend`, first inspect the current state without changing
anything:

```powershell
docker compose ps
docker compose logs --tail=200 postgres
docker compose exec -T postgres psql -U postgres -d events_ai_local -v ON_ERROR_STOP=1 -Atqc 'SELECT 1'
```

If the last command reports `critical system index` or `PANIC`, treat the
cluster as corrupted. A restart or `pg_isready` is not evidence that the data
is usable.

## Preserve evidence before any recovery

1. Stop the application processes that write to the local database.
2. Record `docker compose ps` and the PostgreSQL logs.
3. Make a copy of the PostgreSQL volume or obtain a successful logical dump,
   when the cluster can still service `pg_dump`.
4. Keep the copy outside the active Docker volume and validate that it can be
   restored into an isolated database.

Do not run `docker compose down -v`, remove a Docker volume, or use low-level
PostgreSQL repair commands before a human has explicitly decided that the local
data is disposable or that a recoverable copy exists. Those operations can make
the remaining evidence unrecoverable.

## Recovery decision

| Situation | Correct next action |
| --- | --- |
| Data matters | Preserve a copy and restore it into an isolated cluster with a PostgreSQL recovery specialist. |
| Data is disposable local development data | Get explicit confirmation, preserve any needed fixtures, then recreate the local cluster through the team's approved reset procedure. |
| Unsure | Stop. Do not reset. Escalate with the diagnostic logs and the volume name. |

After a deliberate recovery, rerun the read-only `SELECT 1` check, start the
control plane, and execute the automated smoke test before signing in to the
dashboard.

## Isolated Delivery database without resetting a damaged cluster

When a separate healthy local PostgreSQL container is already available, use a
new sibling database instead of reusing its integration database or touching
the damaged `postgres_data` volume. The control-plane launcher accepts an
explicit probe container for this case. For example, after a human or local
bootstrap has created `itbem_delivery_local` in the isolated container:

```powershell
.\scripts\Start-LocalAIControlPlane.ps1 `
  -DatabaseHost localhost -DatabasePort 15433 `
  -DatabaseUser eventiapp -DatabaseName itbem_delivery_local `
  -DatabaseProbeContainer eventiapp-integration-postgres-1
```

The probe executes only `SELECT 1` through that named container. It does not
fall back to the damaged cluster, create the database, run a reset, or modify
any volume. The API then applies its normal schema migration only to the named
fresh database.
