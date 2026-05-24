#!/usr/bin/env node
// postinstall — set up orch on the operator's machine.
//
// What we do here (idempotent):
//   1. Symlink hooks/*       → ~/.claude/hooks/
//   2. Symlink skills/*      → ~/.claude/skills/
//   3. Copy fleet-prompt.md  → ~/.cache/orch-fleet-prompt.md  (stable file, not symlink)
//   4. Inject fleet doctrine into ~/.codex/AGENTS.md and ~/.gemini/GEMINI.md
//      (those harnesses have no --append-system-prompt CLI flag, so we
//      maintain a marker-block insertion that re-runs cleanly).
//   5. Print the manual step (settings-snippet.json → ~/.claude/settings.json)
//      and the runtime-deps hint for the operator's platform.
//
// Per-harness hook scripts (codex/gemini/pi) and the marker/NATS-publish
// hooks were retired in orch#94. The Synadia Agent Protocol path via
// orch-agent-shim is now the only path; the shim handles per-harness
// eventing over the bus, so there is nothing to symlink into ~/.codex,
// ~/.gemini, or ~/.pi from a postinstall standpoint.
//
// This file absorbed the legacy install.sh in #189 friction point 2 —
// having two postinstall paths (npm postinstall AND install.sh) was an
// accident of pre-npm history. Now there's just one.
//
// What we do NOT do (intentionally):
//   * Modify ~/.claude/settings.json — the user merges settings-snippet.json
//     by hand. Auto-merging settings is destructive in the wrong cases;
//     we tell the user to do it themselves and print the snippet's path.
//   * Touch any "real" file (non-symlink) that the user wrote themselves.
//     We replace symlinks; we never overwrite plain files. Print a warning
//     instead so the operator can decide.

const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");

const ROOT = path.resolve(__dirname, "..");
const HOME = os.homedir();

const log = (msg) => process.stderr.write(`orch postinstall: ${msg}\n`);
const warn = (msg) => process.stderr.write(`orch postinstall: WARN ${msg}\n`);

function linkOne(src, dst) {
    const dstDir = path.dirname(dst);
    fs.mkdirSync(dstDir, { recursive: true });
    if (fs.lstatSync(dst, { throwIfNoEntry: false })) {
        const stat = fs.lstatSync(dst);
        if (stat.isSymbolicLink()) {
            fs.unlinkSync(dst);
        } else {
            warn(`${dst} exists and is not a symlink; leaving it alone. Remove it manually if you want orch's version.`);
            return false;
        }
    }
    fs.symlinkSync(src, dst);
    log(`linked ${dst} → ${src}`);
    return true;
}

function linkDirEntries(srcDir, dstDir) {
    if (!fs.existsSync(srcDir)) return;
    for (const entry of fs.readdirSync(srcDir)) {
        const src = path.join(srcDir, entry);
        const dst = path.join(dstDir, entry);
        linkOne(src, dst);
    }
}

// injectDoctrine maintains a marker-block in `target` containing the
// fleet doctrine. Idempotent: re-running refreshes the block in place
// without duplicating or clobbering surrounding user content.
//
// Used for harnesses (codex, gemini) that have no
// --append-system-prompt CLI flag — the global instructions file is
// the only injection surface.
const FLEET_BEGIN_MARKER = "<!-- BEGIN orch-fleet-doctrine -->";
const FLEET_END_MARKER = "<!-- END orch-fleet-doctrine -->";

function injectDoctrine(target, fleetSrc) {
    fs.mkdirSync(path.dirname(target), { recursive: true });

    let fleet;
    try {
        fleet = fs.readFileSync(fleetSrc, "utf8");
    } catch (err) {
        warn(`fleet doctrine source missing at ${fleetSrc}; skipping ${target}`);
        return;
    }

    const block = `${FLEET_BEGIN_MARKER}\n${fleet}${fleet.endsWith("\n") ? "" : "\n"}${FLEET_END_MARKER}\n`;

    if (!fs.existsSync(target)) {
        fs.writeFileSync(target, block);
        log(`fleet-doctrine: created ${target}`);
        return;
    }

    const existing = fs.readFileSync(target, "utf8");
    if (existing.includes(FLEET_BEGIN_MARKER)) {
        // Splice the new block in place of the old one.
        const escaped = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
        const re = new RegExp(
            `${escaped(FLEET_BEGIN_MARKER)}[\\s\\S]*?${escaped(FLEET_END_MARKER)}\\n?`
        );
        const updated = existing.replace(re, block);
        fs.writeFileSync(target, updated);
        log(`fleet-doctrine: refreshed block in ${target}`);
        return;
    }

    // No marker yet — append a fresh block after a blank-line separator.
    const sep = existing.endsWith("\n") ? "" : "\n";
    fs.writeFileSync(target, existing + sep + "\n" + block);
    log(`fleet-doctrine: appended block to ${target}`);
}

function platformDepsHint() {
    switch (process.platform) {
        case "darwin":
            return "brew install tmux fswatch jq";
        case "linux":
            // We don't probe for apt vs dnf vs pacman at install time;
            // the user knows their package manager. Just hint at the
            // packages we need.
            return "tmux fswatch jq (apt/dnf/pacman/etc.)";
        default:
            return "install tmux fswatch jq via your platform package manager";
    }
}

// 1. Hooks
linkDirEntries(path.join(ROOT, "hooks"), path.join(HOME, ".claude", "hooks"));

// 2. Skills (each subdir is one skill)
linkDirEntries(path.join(ROOT, "skills"), path.join(HOME, ".claude", "skills"));

// 3. Fleet prompt — copy, not symlink (agents read once at spawn; stable file).
const fleetSrc = path.join(ROOT, "fleet-prompt.md");
const fleetDst = path.join(HOME, ".cache", "orch-fleet-prompt.md");
fs.mkdirSync(path.dirname(fleetDst), { recursive: true });
fs.copyFileSync(fleetSrc, fleetDst);
log(`fleet doctrine cached at ${fleetDst}`);

// 4. Inject fleet doctrine into harnesses that have no
//    --append-system-prompt CLI flag.
injectDoctrine(path.join(HOME, ".codex", "AGENTS.md"), fleetSrc);
injectDoctrine(path.join(HOME, ".gemini", "GEMINI.md"), fleetSrc);

// 5. Tell the user about the one manual step + the runtime deps.
process.stderr.write(`\n`);
process.stderr.write(`orch installed. Manual step remaining:\n`);
process.stderr.write(`  Merge ${path.join(ROOT, "settings-snippet.json")} into ~/.claude/settings.json\n`);
process.stderr.write(`  under the existing "hooks" object. Preserve any hooks already there.\n`);
process.stderr.write(`\n`);
process.stderr.write(`Runtime deps not installed by npm: ${platformDepsHint()}\n`);
process.stderr.write(`\n`);
process.stderr.write(`Verify with: orch version\n`);
