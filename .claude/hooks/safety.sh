#!/bin/bash
# Safety hook — se ejecuta ANTES de cada comando Bash
# Exit 0  → permitir
# Exit 1  → preguntar al usuario (Claude pide aprobación)
# Exit 2  → bloquear completamente (error mostrado al usuario)

INPUT=$(cat)
COMMAND=$(echo "$INPUT" | jq -r '.tool_input.command // empty' 2>/dev/null)

if [ -z "$COMMAND" ]; then
  exit 0
fi

# ==============================================================
# 🔴 BLOQUEOS DUROS — Nunca permitidos sin intervención manual
# ==============================================================

# Force push a cualquier remote
if echo "$COMMAND" | grep -qE "git push.*(--force|-f)\b"; then
  echo "❌ BLOQUEADO: git push --force nunca está permitido." >&2
  echo "   Si es necesario, hazlo manualmente desde la terminal." >&2
  exit 2
fi

# Eliminar rama remota (git push origin :rama)
if echo "$COMMAND" | grep -qE "git push [a-zA-Z0-9_-]+ :[a-zA-Z0-9/_-]+"; then
  echo "❌ BLOQUEADO: Eliminar rama remota no está permitido desde Claude." >&2
  exit 2
fi

# gh CLI: borrar repo, PR, release, issue (GitHub)
if echo "$COMMAND" | grep -qE "gh (repo delete|pr delete|release delete|issue delete)"; then
  echo "❌ BLOQUEADO: Operaciones destructivas de GitHub (gh delete) no permitidas." >&2
  exit 2
fi

# ==============================================================
# 🟡 PREGUNTAR — Requieren aprobación explícita del usuario
# ==============================================================

# Eliminación de archivos (rm)
if echo "$COMMAND" | grep -qE "(^|[;&|]\s*)rm\s"; then
  echo "⚠️  Este comando elimina archivos. ¿Confirmas?" >&2
  exit 1
fi

# Forzar borrado de rama local
if echo "$COMMAND" | grep -qE "git branch\s+-D\b"; then
  echo "⚠️  git branch -D elimina una rama local de forma forzada. ¿Confirmas?" >&2
  exit 1
fi

# git reset --hard (descarta cambios no guardados)
if echo "$COMMAND" | grep -qE "git reset\s+--hard"; then
  echo "⚠️  git reset --hard descarta todos los cambios locales. ¿Confirmas?" >&2
  exit 1
fi

# git push (cualquier push que no sea force — ya manejado arriba)
if echo "$COMMAND" | grep -qE "^git push\b|[;&|]\s*git push\b"; then
  echo "⚠️  Vas a hacer git push. ¿Confirmas que es al branch correcto?" >&2
  exit 1
fi

# gh pr close / gh issue close
if echo "$COMMAND" | grep -qE "gh (pr close|issue close)"; then
  echo "⚠️  Cerrar un PR o issue en GitHub. ¿Confirmas?" >&2
  exit 1
fi

# ==============================================================
# ✅ Todo lo demás: permitido
# ==============================================================
exit 0
