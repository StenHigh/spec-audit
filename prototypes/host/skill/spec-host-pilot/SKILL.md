---
name: spec-host-pilot
description: Run the scoped specification-to-code audit pilot from an existing agent session, with mapper and independent red-team results validated by a local Go CLI. Use only with the explicit pilot configuration and source snapshot provided by its owner.
---

# Scoped host pilot

Keep all artifacts in the supplied working directory and configured reports directory. Never edit the business repository, its TZ, GitLab, or installed skills. The session is the host; the binary never calls a model. Do not install Node or run PHP on the host.

Resolve the binary from the owner's supplied path, or build `bin/spec-host-pilot` using the repository [README](../../../../README.md). `CONFIG` is the owner's YAML configuration; `SOURCE_ROOT` is its `project_root`, returned by `prepare` or `tasks`. Do not confuse the tool repository with the audited source snapshot. Commands below use `BINARY` for that resolved binary path. Read [the agent protocol](references/protocol.txt) before dispatch.

1. Run `BINARY prepare CONFIG RUN_ID`. Read the returned tasks. Reuse an existing run with `BINARY tasks CONFIG RUN_ID`, never overwrite it. `prepare` hashes sources; it does not copy or isolate them. The host must supply a clean snapshot before dispatch.
2. Dispatch one mapper and one independent red-team per scope with fresh context. Give each only its task, `references/protocol.txt`, the resolved `SOURCE_ROOT`, and its own output JSON path. Do not give historical CELL, expected findings, other agents' output, or evaluation receipts. The source copy excludes `.git`, `.ai-factory` and project agent instructions; this is context separation, not an OS security sandbox.
3. In sequential mode finish both roles of one scope before starting the next. In parallel mode dispatch both roles of both scopes (four agents maximum). Never run database tests concurrently; the host alone may run targeted tests in the verified project Docker container.
4. Each agent writes its own observations JSON. Wrap it mechanically with the task's `task_id`, `attempt`, and `snapshot_id`, then call `submit`. Do not silently rewrite a rejected agent quotation or judgment; send the error to that same agent and preserve its raw response.
5. Missing, interrupted, or rejected work remains incomplete. Use `retry` to issue a new attempt and reject late output from the old attempt. Continue completed independent tasks without rerunning them.
6. Run `report`. Inspect JSON and HTML, including pending tasks and both roles. A valid quotation proves its provenance, not its entailment; the host must reconcile real contradictions. A source/test citation is not a recorded passing test. Never infer token cost from text length or wall time.

For the control arm reproduce cell's one-scope-at-a-time mapper + independent red-team mechanics on the same scope excerpts. This is a bounded harness, not a complete official `aif-reaudit-cell`: no production CELL, queue, roadmap, or GitLab writes, and no isolated git worktree. Use the existing Docker `reaudit:cell-triage` reducer for baseline bullet verdicts after explicit host reconciliation. Do not call parallel external runs a relaxation of the existing drive R5 rule.
