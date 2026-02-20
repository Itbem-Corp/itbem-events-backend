# Changelog Template

> Copy a section below, fill in details, and add to the top of `CHANGELOG.md` (create if missing).
> Keep entries newest-first.

## Entry Templates

### Feature
```markdown
## [x.y.z] - YYYY-MM-DD

### Added
- **Feature Name** — Description
  - New model: `models/X.go`
  - New routes: `GET /api/x`, `POST /api/x`
  - Docs updated: `docs/MODELS.md`, `docs/ROUTES.md`
```

### Bug Fix
```markdown
## [x.y.z] - YYYY-MM-DD

### Fixed
- **Bug Description** — Root cause and solution
  - Files: `path/to/file.go`
  - Impact: who was affected
```

### Performance
```markdown
## [x.y.z] - YYYY-MM-DD

### Performance
- **What was optimized** — Method used
  - Before: X ms / Y% hit rate
  - After: X ms / Y% hit rate
  - Files: `path/to/file.go`
```

### Breaking Change
```markdown
## [x.y.z] - YYYY-MM-DD

### ⚠️ BREAKING
- **What changed** — Old behavior → New behavior
  - Migration: steps to update existing code
```

### Security
```markdown
## [x.y.z] - YYYY-MM-DD

### Security
- **Issue** — Severity: Critical/High/Medium/Low
  - Impact, mitigation, CVE if applicable
```

---

## Semantic Versioning

- **MAJOR** (x.0.0) — Breaking API changes
- **MINOR** (0.x.0) — New features, backwards-compatible
- **PATCH** (0.0.x) — Bug fixes, backwards-compatible

## File Location

```
itbem-events-backend/
├── CHANGELOG.md              ← main changelog (create if missing)
└── docs/CHANGELOG_TEMPLATE.md  ← this file
```
