// THE electron-builder configuration — the whole thing, one file. There is
// no "build" field in package.json: run-electron-builder.mjs always passes
// --config for this file, so a stray package.json field would be silently
// ignored anyway, and splitting the config across JSON + this overlay is
// how the two halves drift.
//
// A .cjs module (not JSON) so the variant is decided at require time:
// HERMES_DESKTOP_VARIANT=light builds "Hermes Light". The whole config
// derives from that one flag.
// @ts-check
'use strict'

const fs = require('node:fs')
const path = require('node:path')
const feedContract = require('./update-feed.cjs')
const { createMacSigner } = require('./scripts/mac-sign.mjs')

const {
  light,
  displayName,
  appId,
  appNamePascal,
  artifactNamePascal,
  channel,
  token
} = require('./product-identity.cjs')

const { channelBuildRequest } = require('../../scripts/msix-shared.mjs')
const { macIconResource } = require('./scripts/mac-icon.cjs')
const channelRequest = channelBuildRequest()

/** @typedef {import("app-builder-lib").Configuration} Configuration */

const [owner, repo] = (process.env.GITHUB_REPOSITORY || 'NousResearch/hermes-agent').split('/')
if (!owner || !repo) {
  throw new Error(`invalid GITHUB_REPOSITORY ${process.env.GITHUB_REPOSITORY}`)
}
const electronVersion = require('./package.json').devDependencies.electron
if (!/^\d+\.\d+\.\d+$/.test(electronVersion)) {
  throw new Error(`invalid electron version ${electronVersion} in package.json`)
}

const macFeed = channelRequest ? null : feedContract.darwinFeed(channel === 'canary' || channel === 'light-canary' ? 'canary' : 'stable', light)
const publicUrl = feedContract.feedBaseUrl(process.env.CLOUDFLARE_R2_PUBLIC_URL)

/** @satisfies {Configuration} */
module.exports = {
  electronVersion,
  appId,
  productName: displayName,
  executableName: displayName,
  protocols: [
    {
      name: `${displayName} Protocol`,
      schemes: ['hermes']
    }
  ],
  artifactName: `${artifactNamePascal}-\${version}-\${os}-\${arch}.\${ext}`,
  icon: 'assets/icon',
  // The electron-updater feed. CI builds set CLOUDFLARE_R2_PUBLIC_URL (the R2
  // public bucket / custom domain) and publish there — the feed yml, blockmaps
  // and installers all live in the same flat R2 bucket, and electron-updater
  // resolves the yml's relative artifact paths against it. Builds without the
  // var (local, or a fork without the R2 vars) keep the github provider, which
  // is exactly today's behavior.
  publish: channelRequest ? null : !channel
    ? null
    : [
        publicUrl
          ? { provider: 'generic', url: publicUrl, channel }
          : { provider: 'github', owner, repo, channel }
      ],
  extraMetadata: {
    name: appNamePascal,
    // Electron bootstrap reads package.productName before main.ts. Keep the
    // shipped stable default, but isolate nonstable userData from first access.
    // A stable-branded channel has no token and keeps stable's userData.
    ...((channelRequest && token) || appNamePascal !== artifactNamePascal ? { productName: displayName } : {}),
    desktopName: appId
  },
  directories: {
    output: 'release'
  },
  files: ['dist/**', 'assets/**', 'public/**', 'package.json'],
  beforeBuild: channelRequest ? async () => {
    await require(path.join(__dirname, 'scripts/before-build.mjs')).default()
    return false
  } : 'scripts/before-build.mjs',
  beforePack: 'scripts/before-pack.mjs',
  afterPack: 'scripts/after-pack.mjs',
  ...(process.platform === 'darwin' ? { afterSign: 'scripts/notarize.mjs' } : {}),
  extraResources: [
    {
      from: 'build/install-stamp.json',
      to: 'install-stamp.json'
    },
    ...(process.env.HERMES_DESKTOP_VARIANT === 'bundled'
      ? [{ from: 'build/agent-payload', to: 'agent-payload' }]
      : [])
  ],
  asar: {
    unpack: ['**/*.node', '**/prebuilds/**', 'dist/**']
  },
  mac: {
    // macOS 26 masks every icon into its own squircle: the layered Icon
    // Composer package lets the system do that with the ring following the
    // outline, while `assets/icon.icns` stays the artwork for macOS <= 15.
    // electron-builder compiles `.icon` with actool >= 26 only, so hosts
    // without Xcode 26 fall back to the .icns alone (see scripts/mac-icon.cjs);
    // after-pack.mjs restores our full-resolution .icns either way.
    icon: macIconResource(__dirname),
    // The afterSign hook owns notarization, including keychain-profile builds.
    notarize: false,
    // The packaged client reads this generated app-update.yml by default.
    publish: channelRequest?.receiverCandidate
      ? [{ provider: 'generic', url: `${channelRequest.publicBase}/releases/darwin/stable/`, channel: 'stable' }]
      : channelRequest
      ? [{ provider: 'generic', url: `${channelRequest.publicBase}/releases/channel-builds/${channelRequest.buildId}/darwin/`, channel: 'latest' }]
      : publicUrl && channel && macFeed
      ? [{ provider: 'generic', url: `${publicUrl}/${macFeed.directory}/`, channel: macFeed.channel }]
      : null,
    category: 'public.app-category.developer-tools',
    extendInfo: {
      CFBundleDisplayName: displayName,
      CFBundleExecutable: displayName,
      CFBundleName: displayName,
      LSRequiresNativeExecution: true,
      NSAudioCaptureUsageDescription: `${displayName} uses audio capture for voice conversations.`,
      NSCameraUsageDescription: `${displayName} uses the camera when a plugin or feature you enable requests it.`,
      NSMicrophoneUsageDescription: `${displayName} uses the microphone for voice input and voice conversations.`,
      NSCalendarsUsageDescription: `${displayName} needs access to Calendar to provide requested meeting and scheduling support.`,
      NSCalendarsFullAccessUsageDescription: `${displayName} needs full access to Calendar to read and manage events when explicitly requested.`,
      NSRemindersUsageDescription: `${displayName} needs access to Reminders to provide requested personal-assistant and scheduling support.`,
      NSRemindersFullAccessUsageDescription: `${displayName} needs full access to Reminders to read and manage reminders when explicitly requested.`,
      NSScreenCaptureUsageDescription: `${displayName} captures the screen when you ask the agent to screenshot or record it.`,
      NSLocalNetworkUsageDescription: `${displayName} connects to devices on your local network when a plugin or feature you enable requests it.`,
      NSAppleMusicUsageDescription: `${displayName} accesses your music library when a plugin or feature you enable requests it.`,
      NSContactsUsageDescription: `${displayName} uses Contacts access when you ask it to read or update your address book.`,
      NSAppleEventsUsageDescription: `${displayName} uses Apple Events to automate apps you explicitly ask it to control.`
    },
    target: ['dmg', 'zip'],
    sign: createMacSigner({
      entitlements: path.join(__dirname, 'electron/entitlements.mac.plist'),
      entitlementsInherit: path.join(__dirname, 'electron/entitlements.mac.inherit.plist'),
      hardenedRuntime: true,
      ignore: (/** @type {string} */ file) => {
        try {
          if (fs.lstatSync(file).isDirectory()) {
            return false
          }
          return !isMachO(file)
        } catch {
          return true
        }
      }
    })
  },
  dmg: {
    // Avoid the failing optional APFS shrink pass; keep compressed conversion.
    shrink: false,
    // The volume icon defaults to the packager's icns, which is actool's 256px
    // fallback whenever `mac.icon` is the Icon Composer package. Ship our own
    // drive-with-the-girl artwork instead (dmgbuild's badge option can only
    // paste onto the stock removable-drive icon). It lives in packaging/ with
    // the background so the `files` whitelist keeps it out of the app bundle.
    icon: 'packaging/dmg-volume.icns',
    title: 'Hermes Agent Installer',
    // A prebuilt .tiff on purpose, not a PNG plus a @2x sibling: dmg-builder's
    // PNG path runs `tiffutil -cathidpicheck`, which on macOS 26 rewrites both
    // frames to 72 dpi and silently drops the 2x representation. A .tiff is
    // handed to dmgbuild untouched (dmg-builder/dist/dmgUtil.js), and living
    // outside assets/ keeps it out of the app bundle via the `files` whitelist.
    background: 'packaging/nous-dmg-2b.tiff',
    iconSize: 96,
    iconTextSize: 11,
    window: {
      width: 660,
      height: 400
    },
    contents: [
      {
        x: 253,
        y: 238,
        type: 'file'
      },
      {
        x: 512,
        y: 235,
        type: 'link',
        path: '/Applications'
      }
    ]
  },
  linux: {
    category: 'Development',
    maintainer: 'Nous Research <support@nousresearch.com>',
    synopsis: light
      ? 'Remote-only desktop client for Hermes Agent.'
      : 'Native desktop shell for Hermes Agent.',
    target: ['AppImage']
  }
}

if (channelRequest) {
  Object.assign(module.exports, { buildVersion: channelRequest.version })
  Object.assign(module.exports.extraMetadata, {
    version: channelRequest.version
  })
  Object.assign(module.exports.mac, {
    bundleVersion: channelRequest.version,
    bundleShortVersion: channelRequest.version
  })
}

const MACHO_MAGICS = new Set([
  0xfeedface,
  0xcefaedfe,
  0xfeedfacf,
  0xcffaedfe,
  0xcafebabe,
  0xbebafeca
])

/** @param {string} file */
function isMachO(file) {
  const buf = Buffer.alloc(4)
  const fd = fs.openSync(file, 'r')
  try {
    if (fs.readSync(fd, buf, 0, 4, 0) !== 4) {
      return false
    }
  } finally {
    fs.closeSync(fd)
  }
  return MACHO_MAGICS.has(buf.readUInt32BE(0))
}
