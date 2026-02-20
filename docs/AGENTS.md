# Claude Code Agents

> These are **agent definitions** for common tasks in this project.
> Agents must be configured in `.claude/agents/` to be usable via `/task`.
> Currently, no agents are configured — use these as prompts or set them up.

## Available Agent Definitions

### scaffold-generator
**Purpose**: Generate complete CRUD for a new entity
**Prompt template**:
```
Read docs/MODELS.md, docs/TEMPLATES.md, and docs/CODE_INDEX.md.
Then create full CRUD for {Entity}: model, repository, service, controller, routes.
Follow all patterns from TEMPLATES.md. Update all relevant docs/ files after.
Module name is events-stocks (not itbem-events-backend).
```
**Token savings**: ~25,000 vs manual

### doc-updater
**Purpose**: Sync docs after code changes
**Prompt template**:
```
Read the current state of these files: {changed files}.
Then update docs/MODELS.md, docs/ROUTES.md, docs/SERVICES.md,
docs/REPOSITORIES.md, and docs/CODE_INDEX.md to reflect the actual code.
Do not add content that isn't in the code.
```
**Token savings**: ~5,000 vs manual

### security-auditor
**Purpose**: Audit security vulnerabilities
**Prompt template**:
```
Read docs/CODING_STANDARDS.md security section.
Review {files or domain} for: SQL injection, missing auth checks,
unvalidated input, hardcoded secrets, missing authorization.
Report findings with file:line references.
```
**Token savings**: ~20,000 vs manual

### performance-optimizer
**Purpose**: Identify N+1 queries and cache issues
**Prompt template**:
```
Read docs/PERFORMANCE.md and docs/CODING_STANDARDS.md.
Analyze {repository or service files} for: N+1 queries,
missing Preload(), missing indexes, cache miss opportunities.
Suggest specific fixes with code examples.
```
**Token savings**: ~15,000 vs manual

### test-writer
**Purpose**: Write unit and integration tests
**Prompt template**:
```
Read {service or controller file} and docs/TEMPLATES.md test template.
Write table-driven Go tests covering: happy path, validation errors,
not-found cases, and any business rule edge cases.
Use testify/assert. File: {domain}_test.go
```
**Token savings**: ~15,000 vs manual

### model-explorer
**Purpose**: Understand model relationships before code changes
**Prompt template**:
```
Read docs/MODELS.md for the overview, then read these model files: {files}.
Map all relationships (BelongsTo, HasMany, junction tables).
Return a structured summary with field names and GORM tags.
```
**Token savings**: ~10,000 vs manual

### route-mapper
**Purpose**: Map existing routes before adding new ones
**Prompt template**:
```
Read routes/routes.go and docs/ROUTES.md.
List all existing routes for {domain}, their HTTP methods,
controllers, and whether they're public or protected.
Identify gaps or missing endpoints.
```
**Token savings**: ~8,000 vs manual

## How to Configure Agents

Create `.claude/agents/<name>.md` with:
```markdown
---
name: scaffold-generator
description: Generates full CRUD scaffolding for new entities
---
[prompt content here]
```

Then use: `/task scaffold-generator "Create full CRUD for Notification entity"`

## Parallel Execution

Run independent agents in the same message to save time:
- Spawn multiple Task tool calls simultaneously for unrelated work
- Wait for results before starting dependent tasks

## Token Savings Summary

| Agent | Savings |
|-------|---------|
| scaffold-generator | ~25,000 tokens |
| security-auditor | ~20,000 tokens |
| test-writer | ~15,000 tokens |
| performance-optimizer | ~15,000 tokens |
| model-explorer | ~10,000 tokens |
| route-mapper | ~8,000 tokens |
| doc-updater | ~5,000 tokens |
