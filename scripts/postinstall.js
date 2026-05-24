#!/usr/bin/env node
// postinstall — set up orch on the operator's machine.
//
// What we do here (idempotent):
//   1. Fetch the platform-specific Go binaries (orch, orch-subtree,
//      orch-workflow, orch-registry, orch-goal-stop-account-daemon,
//      orch-cc-subagent-bridge) from the matching GitHub Release and
//      extract them into vendor/.
//      The bash shims in bin/ exec the vendored binary instead of
//      lazy-building from Go source — the npm tarball does not ship Go
//      source, so lazy-build is broken for npm-installed users (#214).
//   2. Symlink hooks/*       → ~/.claude/hooks/
//   3. Symlink skills/*      → ~/.claude/skills/
//   4. Copy fleet-prompt.md  → ~/.cache/orch-fleet-prompt.md  (stable file, not symlink)
//   5. Inject fleet doctrine into ~/.codex/AGENTS.md and ~/.gemini/GEMINI.md
//      (those harnesses have no --append-system-prompt CLI flag, so we
//      maintain a marker-block insertion that re-runs cleanly).
//   6. Print the manual step (settings-snippet.json → ~/.claude/settings.json)
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
//
// Binary-fetch resolution:
//   1. version = package.json.version → tag = `v${version}`.
//   2. asset = `orch_${version}_${os}_${arch}.tar.gz`.
//   3. download from https://github.com/danmestas/orch/releases/download/<tag>/<asset>.
//   4. extract every listed binary into vendor/ and chmod +x.
//
// Skipped when:
//   - $ORCH_SKIP_DOWNLOAD=1 (CI / offline / vendored / dev installs where the
//     repo is symlinked, not npm-installed, and the bash shims fall back to
//     lazy-building from source).
//   - The vendor binaries already exist and the recorded version matches
//     package.json (re-runs of `npm install` don't redownload).
//
// On download failure: log a warning and continue with the symlink work.
// The shims fall back to lazy-build, which is graceful degradation on a
// dev checkout (Go source present) and a clear error elsewhere.

const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");
const { spawnSync } = require("node:child_process");

const ROOT = path.resolve(__dirname, "..");
const HOME = os.homedir();
const VENDOR_DIR = path.join(ROOT, "vendor");
const VENDOR_STAMP = path.join(VENDOR_DIR, ".version");

// Six Go binaries shipped in the goreleaser archive (see .goreleaser.yaml).
// Kept in sync with the `builds:` ids there — any addition / removal here
// must land in the same PR as the goreleaser change.
const BINARIES = [
    "orch",
    "orch-subtree",
    "orch-workflow",
    "orch-registry",
    "orch-goal-stop-account-daemon",
    "orch-cc-subagent-bridge",
];

const log = (msg) => process.stderr.write(`orch postinstall: ${msg}\n`);
const warn = (msg) => process.stderr.write(`orch postinstall: WARN ${msg}\n`);

// ───────────────────────────────────────────────────────────────────
// 1. Fetch + extract Go binaries from the matching GH Release
// ───────────────────────────────────────────────────────────────────

function fetchBinaries() {
    if (process.env.ORCH_SKIP_DOWNLOAD === "1") {
        log("ORCH_SKIP_DOWNLOAD=1, skipping binary download");
        return;
    }

    const pkg = JSON.parse(fs.readFileSync(path.join(ROOT, "package.json"), "utf8"));
    const version = pkg.version;
    const tag = `v${version}`;

    // Idempotent — if vendor/ already has every binary AND the stamp matches
    // the current version, do nothing. Skips needless downloads on repeated
    // `npm install` runs (e.g. CI cold caches re-running install each job).
    if (vendorComplete(version)) {
        log(`vendor binaries already at ${version}, skipping download`);
        return;
    }

    const goos = mapOS(process.platform);
    const goarch = mapArch(process.arch);
    if (!goos || !goarch) {
        warn(`unsupported platform ${process.platform}/${process.arch} — skipping binary download`);
        warn("bin shims will fall back to lazy-build (needs go on PATH and Go source)");
        return;
    }

    const asset = `orch_${version}_${goos}_${goarch}.tar.gz`;
    const url = `https://github.com/danmestas/orch/releases/download/${tag}/${asset}`;

    fs.mkdirSync(VENDOR_DIR, { recursive: true });

    log(`fetching ${url}`);
    try {
        downloadAndExtract(url, asset);
    } catch (err) {
        warn(`download failed: ${err.message}`);
        warn("bin shims will fall back to lazy-build (needs go on PATH and Go source)");
        return;
    }

    // chmod every binary explicitly — tar -xz preserves the archive's perms,
    // and goreleaser-built binaries are 0755 already, but belt-and-braces.
    for (const name of BINARIES) {
        const p = path.join(VENDOR_DIR, name);
        if (fs.existsSync(p)) {
            fs.chmodSync(p, 0o755);
        }
    }

    // Stamp the vendor dir with the version we just unpacked. Used to
    // short-circuit redownload on the next install.
    fs.writeFileSync(VENDOR_STAMP, `${version}\n`);
    log(`extracted ${BINARIES.length} binaries to ${VENDOR_DIR}`);
}

function vendorComplete(version) {
    if (!fs.existsSync(VENDOR_STAMP)) return false;
    let stamp;
    try {
        stamp = fs.readFileSync(VENDOR_STAMP, "utf8").trim();
    } catch {
        return false;
    }
    if (stamp !== version) return false;
    for (const name of BINARIES) {
        const p = path.join(VENDOR_DIR, name);
        if (!fs.existsSync(p)) return false;
    }
    return true;
}

function mapOS(p) {
    if (p === "darwin") return "darwin";
    if (p === "linux") return "linux";
    return null;
}

function mapArch(a) {
    if (a === "x64") return "amd64";
    if (a === "arm64") return "arm64";
    return null;
}

function downloadAndExtract(url, asset) {
    // Shell out to curl + tar; both ship on macOS and most Linux. Avoids a
    // runtime dependency on the `node-tar` library.
    if (!hasCmd("curl") || !hasCmd("tar")) {
        throw new Error("curl + tar required (both ship on macOS / most Linux)");
    }

    const tmp = path.join(os.tmpdir(), `${asset}-${process.pid}`);

    let r = spawnSync(
        "curl",
        ["-fsSL", "--retry", "3", "--retry-delay", "2", "-o", tmp, url],
        { stdio: "inherit" },
    );
    if (r.status !== 0) throw new Error(`curl exited ${r.status}`);

    // Extract every BINARY entry from the archive into VENDOR_DIR. The
    // archive's top level is flat (goreleaser default), so the binary
    // names sit at the root of the tar.
    r = spawnSync("tar", ["-xzf", tmp, "-C", VENDOR_DIR, ...BINARIES], {
        stdio: "inherit",
    });
    if (r.status !== 0) throw new Error(`tar exited ${r.status}`);

    try {
        fs.unlinkSync(tmp);
    } catch {
        // best-effort cleanup
    }
}

function hasCmd(cmd) {
    const r = spawnSync(process.platform === "win32" ? "where" : "which", [cmd], {
        stdio: "ignore",
    });
    return r.status === 0;
}

// ───────────────────────────────────────────────────────────────────
// 2. Symlink farm + fleet doctrine injection (pre-v0.2.1 behavior)
// ───────────────────────────────────────────────────────────────────

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

function symlinkFarm() {
    // 1. Hooks
    linkDirEntries(path.join(ROOT, "hooks"), path.join(HOME, ".claude", "hooks"));

    // 2. Skills (each subdir is one skill)
    linkDirEntries(path.join(ROOT, "skills"), path.join(HOME, ".claude", "skills"));

    // 3. Fleet prompt — copy, not symlink (agents read once at spawn; stable file).
    const fleetSrc = path.join(ROOT, "fleet-prompt.md");
    const fleetDst = path.join(HOME, ".cache", "orch-fleet-prompt.md");
    if (fs.existsSync(fleetSrc)) {
        fs.mkdirSync(path.dirname(fleetDst), { recursive: true });
        fs.copyFileSync(fleetSrc, fleetDst);
        log(`fleet doctrine cached at ${fleetDst}`);

        // 4. Inject fleet doctrine into harnesses that have no
        //    --append-system-prompt CLI flag.
        injectDoctrine(path.join(HOME, ".codex", "AGENTS.md"), fleetSrc);
        injectDoctrine(path.join(HOME, ".gemini", "GEMINI.md"), fleetSrc);
    }

    // 5. Tell the user about the one manual step + the runtime deps.
    process.stderr.write(`\n`);
    process.stderr.write(`orch installed. Manual step remaining:\n`);
    process.stderr.write(`  Merge ${path.join(ROOT, "settings-snippet.json")} into ~/.claude/settings.json\n`);
    process.stderr.write(`  under the existing "hooks" object. Preserve any hooks already there.\n`);
    process.stderr.write(`\n`);
    process.stderr.write(`Runtime deps not installed by npm: ${platformDepsHint()}\n`);
    process.stderr.write(`\n`);
    process.stderr.write(`Verify with: orch version\n`);
}

// ───────────────────────────────────────────────────────────────────
// main
// ───────────────────────────────────────────────────────────────────

try {
    fetchBinaries();
} catch (err) {
    // Never let binary-fetch problems break the install — the symlink
    // farm step is still useful, and the bash shims surface a clear
    // error on first invocation if the binaries didn't land.
    warn(`fetchBinaries error: ${err.message}`);
}
symlinkFarm();
