#!/usr/bin/env node
// preuninstall — remove the symlinks postinstall created.
//
// Only removes links that point back at this package's install dir.
// Real files (the user's own configs) are never touched.

const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");

const ROOT = path.resolve(__dirname, "..");
const HOME = os.homedir();

const log = (msg) => process.stderr.write(`orch preuninstall: ${msg}\n`);

function unlinkIfOurs(dst) {
    const stat = fs.lstatSync(dst, { throwIfNoEntry: false });
    if (!stat) return;
    if (!stat.isSymbolicLink()) return;
    let target;
    try {
        target = fs.readlinkSync(dst);
    } catch {
        return;
    }
    // Resolve relative targets against the symlink's directory.
    const absTarget = path.isAbsolute(target)
        ? target
        : path.resolve(path.dirname(dst), target);
    if (absTarget.startsWith(ROOT)) {
        fs.unlinkSync(dst);
        log(`unlinked ${dst}`);
    }
}

function sweepDir(srcDir, dstDir) {
    if (!fs.existsSync(srcDir)) return;
    for (const entry of fs.readdirSync(srcDir)) {
        unlinkIfOurs(path.join(dstDir, entry));
    }
}

sweepDir(path.join(ROOT, "hooks"),  path.join(HOME, ".claude", "hooks"));
sweepDir(path.join(ROOT, "skills"), path.join(HOME, ".claude", "skills"));
sweepDir(path.join(ROOT, "pi-extensions"), path.join(HOME, ".pi", "agent", "extensions"));

// Fleet prompt is a copy, not a symlink — we leave it in place. Operator deletes
// manually if they want it gone:
//   rm ~/.cache/orch-fleet-prompt.md

log("done");
