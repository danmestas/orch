// Package subtree implements Proposal 0006 — declarative subtree
// topology yaml. A subtree is a fleet of workers + sesh context + seed
// state. Orch is the operator that applies topologies; it is NOT in
// the topology yaml itself.
//
// The package is structured around the five-phase apply pipeline
// (Parse → Resolve sesh → Spawn workers → Seed state → Persist). Each
// phase is reachable through an interface so the bench, CLI, and tests
// share the same code path with different backends:
//
//   - SeshResolver — resolves sesh context (existing URL or spawn hub)
//   - WorkerSpawner — invokes the spawnspec dispatcher for missing
//     workers
//   - StateSeeder — passes through to sesh-ops for tasks/goals
//   - LiveRegistry — snapshots live workers from $SRV.INFO.agents
//   - CacheStore — read/write ~/.cache/orch-subtrees/<name>.applied.yaml
//
// Push-once semantics (Ousterhout-locked v1, 2026-05-18): apply is an
// imperative; there is no reconciliation loop. Drift surfaces via
// `orch subtree status` and is fixed by re-applying. Re-runs are
// idempotent at every phase boundary so partial-failure recovery is
// "run apply again".
//
// See docs/proposals/0006-subtree-topology-yaml.md for the spec and
// docs/proposals/0002-typed-executor-contract.md for the SpawnSpec
// types that workers embed.
package subtree
