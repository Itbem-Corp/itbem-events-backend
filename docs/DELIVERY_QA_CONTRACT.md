# Delivery QA contract

Delivery treats QA as a reviewed execution contract, not a free-form agent action.
The plan proposes intent; the worker executes only operator-configured commands
and the approved browser cases. A human must approve the plan and code-review
gates before the QA phase can be queued.

## Per-repository contract

Each frozen `workspace://` source carries a harness projection:

| Field | Meaning |
| --- | --- |
| `validation_command_count` | Deterministic validations available during isolated implementation. |
| `qa_command_count` | Deterministic QA commands available after reviewed code. |
| `artifact_collection` | Whether configured test artefacts may be retained privately. |
| `screenshot_mode` | Responsive screenshot evidence capability. |
| `semantic_qa_mode` | Whether the pinned Stagehand browser harness is available. |

Command bodies remain local configuration. The model receives counts and
capabilities, never shell arguments or credentials. This makes the plan
auditable without letting it execute arbitrary commands.

## Browser contract

The approved plan can contain up to three `browser_qa_cases`. Each case is a
bounded sequence of only:

- `navigate` to a same-origin relative path;
- `assert_visible`;
- `assert_text`;
- `click` only in an explicit interaction mode; `approved_navigation` requires
  its exact same-origin destination;
- in `approved_test_flow` only, `fill` from a named `ITBEM_QA_*` test-value
  reference and `assert_path` to a same-origin path.

`approved_test_flow` exists for isolated test accounts after the human plan
gate. Its values are resolved only at execution from the approved local/test
environment references; literal credentials are never stored in a task, plan,
command argument, report or model request. Every click in that mode must be
followed by a reviewed assertion. External navigation, arbitrary scripts,
deletion, payments, invitations, irreversible mutation and privileged
administration remain prohibited. Stagehand receives the compiled JSON plan,
not an agent prompt, and saves a structured report plus landing and case
screenshots. Evidence is uploaded as private task artifacts, addressed by the
task/run IDs and shown in the Delivery work item.

## Credential boundary

Repository validation/QA commands inherit a scrubbed environment: provider,
GitHub and infrastructure secrets are removed. The sole exception is the
pinned ITBEM Stagehand runner, which receives the MiniMax credential only for
browser inference and the exact reviewed `ITBEM_QA_*` values required by an
approved test flow. It cannot be selected by a model and its request/response
ledger is retained privately with token and cost dimensions.

## Human review checklist

1. Confirm the frozen repository revisions and intended impact matrix.
2. Review browser cases, expected evidence and harness capabilities.
3. Approve the plan gate; only then may implementation start.
4. Review the isolated diff and CI/worktree evidence; approve code review.
5. Review command results, Stagehand report and screenshots before accepting
   QA. A semantic verdict never approves the gate by itself.
