#!/usr/bin/env node
// postinstall — symlink orch's hooks and skills into Claude Code's tree.
//
// What we do here (idempotent):
//   1. Symlink hooks/*           → ~/.claude/hooks/
//   2. Symlink skills/*          → ~/.claude/skills/
//   3. Symlink pi-extensions/*   → ~/.pi/agent/extensions/  (only if that dir exists)
//   4. Copy fleet-prompt.md      → ~/.cache/orch-fleet-prompt.md  (stable file, not symlink)
//
// What we do NOT do (intentionally):
//   * Modify ~/.claude/settings.json  — the user merges settings-snippet.json by hand.
//     Auto-merging settings is destructive in the wrong cases; we tell the user
//     to do it themselves and print the snippet content.
//   * Inject fleet doctrine into ~/.codex/AGENTS.md or ~/.gemini/GEMINI.md.
//     That's an opinion the user adopts deliberately, not on package install.
//
// Bail out gracefully if anything is missing or already present in the wrong shape.
// We never overwrite a real file that someone authored; we only replace symlinks.

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

// 1. Hooks
linkDirEntries(path.join(ROOT, "hooks"), path.join(HOME, ".claude", "hooks"));

// 2. Skills (each subdir is one skill)
linkDirEntries(path.join(ROOT, "skills"), path.join(HOME, ".claude", "skills"));

// 3. Pi extensions (only if pi is installed)
const piExtDir = path.join(HOME, ".pi", "agent", "extensions");
if (fs.existsSync(path.join(HOME, ".pi"))) {
    linkDirEntries(path.join(ROOT, "pi-extensions"), piExtDir);
}

// 4. Fleet prompt — copy, not symlink (agents read once at spawn; stable file).
const fleetSrc = path.join(ROOT, "fleet-prompt.md");
const fleetDst = path.join(HOME, ".cache", "orch-fleet-prompt.md");
fs.mkdirSync(path.dirname(fleetDst), { recursive: true });
fs.copyFileSync(fleetSrc, fleetDst);
log(`fleet doctrine cached at ${fleetDst}`);

// 5. Tell the user about the one manual step.
process.stderr.write(`\n`);
process.stderr.write(`orch installed. One manual step remaining:\n`);
process.stderr.write(`  Merge ${path.join(ROOT, "settings-snippet.json")} into ~/.claude/settings.json\n`);
process.stderr.write(`  under the existing "hooks" object. Preserve any hooks already there.\n`);
process.stderr.write(`\n`);
process.stderr.write(`Runtime deps not installed by npm: tmux, fswatch, jq.\n`);
process.stderr.write(`  macOS:  brew install tmux fswatch jq\n`);
process.stderr.write(`  Linux:  apt install tmux fswatch jq    (or dnf / pacman)\n`);
process.stderr.write(`\n`);
process.stderr.write(`Verify with: orch-version\n`);
