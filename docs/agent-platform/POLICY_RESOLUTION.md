# Hierarchical delivery policy

`internal/deliverypolicy` resolves approved policy content in one fixed order:

```text
platform → organization → project → repository → approved change-set override
```

The resolver is generic. Repository identity is a strict `github://owner/repo`
reference, the target branch is explicit (including non-`main` branches), and
no stack, test command, workflow, environment, domain, cloud, or repository is
hardcoded. Input order cannot change the result.

## Fail-closed rules

Each layer is an immutable content revision with a SHA-256 digest and trusted
human approval metadata. A mismatched scope, duplicate level, unapproved or
future approval, expired override, unsupported field value, or content/digest
mismatch rejects the complete resolution. Overrides are bound to an exact
organization, project and change-set ID, optionally one repository, with a
mandatory reason and expiry.
An override cannot live beyond 24 hours from its approval, preventing a
change-set exception from silently becoming permanent project policy.

Configuration uses inheritance-aware patches. An absent field inherits; an
explicit empty test list is meaningful only for a configured `review_only`
project. Merge/release modes must name at least one required test. Every mode
must explicitly configure target branches. Merge/release also configure a
non-force merge method; release additionally requires a workflow, environment,
explicit secret-reference and variable-reference lists, health checks and a
recovery classification. The two reference lists must be present even when
empty, so the resolver can distinguish an operator decision that no GitHub
environment values are needed from missing configuration.

Target branch authorization is exact and case-sensitive; wildcards are not
accepted. Deployment workflow identity is restricted to a concrete
`.github/workflows/*.yml` or `.yaml` file. These checks still have to be
reapplied by the later exact-SHA action adapter immediately before it requests
any merge or deployment capability.

Secret and variable references contain names only, never values. They are
canonical uppercase identifiers, unique case-insensitively, limited to 64 per
list and cannot use GitHub's reserved `GITHUB_` prefix. They are operator-owned
policy; neither model output nor workflow prose may invent them. A later
release adapter checks only that the configured names exist in the configured
GitHub environment and remains unable to read secret values.

Safety floors are code, not configuration. No layer can disable exact-SHA
evidence, independent review, Vault reconciliation, secret scanning, zero
high/critical findings, compatibility, migrations, dependency order,
environment/recovery evaluation, human approval, or the force-merge ban.

The final digest binds the resolved values and every source revision,
approver, and approval time. `GatePolicyFor` returns an unresolved Gatekeeper
policy when the requested action exceeds the configured mode: review-only can
never merge; merge-only can never release.

This package performs no database, GitHub, queue, merge, or deployment action.
Persistence and the project configuration UI are subsequent incremental PRs;
they must store immutable revisions and pass only approved matching layers to
this resolver.
