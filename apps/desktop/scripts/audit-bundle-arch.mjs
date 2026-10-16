// audit-bundle-arch.mjs: prove every native binary inside a packed desktop
// bundle was built for the target architecture.
//
// Why: wrong-arch binaries run FINE on the build runner (Rosetta emulates
// x64 silently) and only fail — or quietly burn cycles in
// emulation — on user machines. One x64 pip launcher shim inside an arm64
// payload shipped exactly this way. The build must fail loudly instead.
//
// The scan walks every file, sniffs the magic bytes (ELF / Mach-O,
// including fat headers), and reports any binary whose architecture does
// not match the target. Non-binaries are skipped by content, not by file
// extension — a mis-extensioned binary still gets caught.
//
// CLI:
//   node audit-bundle-arch.mjs --arch=arm64 --root=release
//
// --root is scanned for the unpacked app dirs electron-builder produces
// (linux-unpacked, linux-arm64-unpacked, mac/, mac-arm64/, …).
// Auditing the unpacked tree covers the exact bytes the installer embeds:
// DMG/AppImage add compression around it, not content.

import fs from "node:fs"
import path from "node:path"

import { isMain } from "./utils.mjs"

// ─── pure classification (unit-tested, no I/O) ─────────────────────────────

const ELF_MACHINES = {
  0x03: "ia32",
  0x28: "arm",
  0x3e: "x64",
  0xb7: "arm64",
}

const MACHO_CPUTYPES = {
  0x01000007: "x64",
  0x0100000c: "arm64",
  0x00000007: "ia32",
  0x0000000c: "arm",
}

/**
 * Classify a buffer holding the head of a file (>= 64 bytes is plenty).
 *
 * Returns null for non-binaries, or { format, arches } where arches lists
 * every architecture the file carries (>1 only for Mach-O fat binaries).
 */
export function classifyHeader(buf) {
  if (buf.length >= 20 && buf[0] === 0x7f && buf[1] === 0x45 && buf[2] === 0x4c && buf[3] === 0x46) {
    const machine = buf.readUInt16LE(18)
    return { format: "elf", arches: [ELF_MACHINES[machine] || `unknown(0x${machine.toString(16)})`] }
  }
  if (buf.length >= 8) {
    const be = buf.readUInt32BE(0)
    // Thin Mach-O, either endianness.
    if (be === 0xfeedface || be === 0xfeedfacf) {
      const cputype = buf.readUInt32BE(4)
      return { format: "macho", arches: [MACHO_CPUTYPES[cputype] || `unknown(0x${cputype.toString(16)})`] }
    }
    if (be === 0xcefaedfe || be === 0xcffaedfe) {
      const cputype = buf.readUInt32LE(4)
      return { format: "macho", arches: [MACHO_CPUTYPES[cputype] || `unknown(0x${cputype.toString(16)})`] }
    }
    // Fat/universal: big-endian header, one arch entry per slice.
    if (be === 0xcafebabe) {
      const count = buf.readUInt32BE(4)
      // A Java .class file shares the magic; its "count" here is the
      // bytecode version (>= 45). Real fat binaries carry a few slices.
      if (count > 0 && count < 30) {
        const arches = []
        for (let i = 0; i < count && 8 + i * 20 + 4 <= buf.length; i++) {
          const cputype = buf.readUInt32BE(8 + i * 20)
          arches.push(MACHO_CPUTYPES[cputype] || `unknown(0x${cputype.toString(16)})`)
        }
        return { format: "macho-fat", arches }
      }
    }
  }
  return null
}

/**
 * Does a classified binary satisfy the target arch?
 *  - exact match, always
 *  - macOS fat binary: any slice matching is enough (universal ships both)
 * Everything else — including "unknown(...)" — is a mismatch: an
 * unclassifiable machine code in a shipped binary deserves a loud failure,
 * not a shrug.
 */
export function archMatches(arches, targetArch) {
  return arches.some((a) => a === targetArch)
}

/**
 * The unpacked-app directories electron-builder leaves under release/ for
 * a target arch. Names differ per platform: Linux uses
 * <platform>[-<arch>]-unpacked with the HOST-default arch unsuffixed; mac
 * uses mac[-<arch>]/. Match by shape, not an exhaustive list, so a rename
 * in electron-builder fails the audit (no dirs found) instead of silently
 * auditing nothing.
 */
export function findUnpackedDirs(entries) {
  return entries.filter((name) => /-unpacked$/.test(name) || /^mac(-[a-z0-9]+)?$/.test(name))
}

// ─── impure walker + CLI ────────────────────────────────────────────────────

function classifyFile(filePath) {
  const fd = fs.openSync(filePath, "r")
  try {
    const head = Buffer.alloc(4096)
    const n = fs.readSync(fd, head, 0, head.length, 0)
    return classifyHeader(head.subarray(0, n))
  } finally {
    fs.closeSync(fd)
  }
}

function* walkFiles(dir) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isSymbolicLink()) continue // targets are scanned as files where they live
    if (entry.isDirectory()) {
      yield* walkFiles(full)
    } else if (entry.isFile()) {
      yield full
    }
  }
}

/**
 * Paths allowed to carry foreign-arch binaries. Anchored at agent-payload (not
 * the tree root: mac nests the payload under Hermes.app/Contents/Resources/).
 * The lib segment is `lib/python3.x` in the payload venv and python store entry.
 */
const EXEMPT_PATTERNS = [
  // pvporcupine ships one native lib per OS/arch in the same wheel.
  // Only the matching sibling is loaded.
  /agent-payload\/venv\/lib\/python[\d.]+\/site-packages\/pvporcupine\/lib\//,
  // debugpy ships attach helpers for every OS/arch in one wheel.
  /agent-payload\/venv\/lib\/python[\d.]+\/site-packages\/debugpy\/_vendored\/pydevd\//,
  // The uv wheel/build cache (uv-cache/) is DELIBERATELY shipped with the
  // payload for warm rebuilds of the mutable venv (pm bundle copies it).
  // It holds cached sdists/archives that uv may have built for ANY arch —
  // inert cache bytes, never loaded at runtime. The audit must not fail a
  // payload for shipping its own cache.
  /agent-payload\/uv-cache\//,
]

export function isExemptPath(relPath) {
  return EXEMPT_PATTERNS.some((p) => p.test(relPath))
}

export function auditTree(rootDir, targetArch) {
  const mismatches = []
  let binaries = 0
  let exempted = 0
  for (const file of walkFiles(rootDir)) {
    let info
    try {
      info = classifyFile(file)
    } catch {
      continue // unreadable file: electron-builder would have failed on it already
    }
    if (!info) continue
    binaries += 1
    if (!archMatches(info.arches, targetArch)) {
      const rel = path.relative(rootDir, file)
      if (isExemptPath(rel)) {
        exempted += 1
        continue
      }
      mismatches.push({ file: rel, format: info.format, arches: info.arches })
    }
  }
  return { binaries, exempted, mismatches }
}

function main() {
  const args = Object.fromEntries(
    process.argv.slice(2).map((a) => {
      const [k, ...v] = a.replace(/^--/, "").split("=")
      return [k, v.join("=")]
    })
  )
  const targetArch = args.arch
  const root = args.root
  if (!targetArch || !root) {
    console.error("usage: audit-bundle-arch.mjs --arch=<x64|arm64> --root=<release dir>")
    process.exit(2)
  }

  const dirs = findUnpackedDirs(fs.readdirSync(root)).map((d) => path.join(root, d))
  if (dirs.length === 0) {
    console.error(`audit-bundle-arch: no unpacked app directory under ${root} — nothing was audited, failing`)
    process.exit(1)
  }

  let failed = false
  for (const dir of dirs) {
    const { binaries, exempted, mismatches } = auditTree(dir, targetArch)
    if (binaries === 0) {
      console.error(`audit-bundle-arch: ${dir}: found no native binaries at all — the scan is broken, failing`)
      failed = true
      continue
    }
    if (mismatches.length > 0) {
      failed = true
      console.error(`audit-bundle-arch: ${dir}: ${mismatches.length} binaries do not match --arch=${targetArch}:`)
      for (const m of mismatches) {
        console.error(`  [${m.format} ${m.arches.join("+")}] ${m.file}`)
      }
    } else {
      const exemptNote = exempted > 0 ? ` (${exempted} exempt)` : ""
      console.log(`audit-bundle-arch: ${dir}: ${binaries} native binaries, all ${targetArch}${exemptNote}`)
    }
  }
  process.exit(failed ? 1 : 0)
}

if (isMain(import.meta.url)) {
  main()
}
