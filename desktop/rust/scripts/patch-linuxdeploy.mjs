#!/usr/bin/env node
// Patches the cached linuxdeploy AppImage so that it uses the system's
// `strip` instead of its bundled one (binutils 2.35), which cannot handle
// the `.relr.dyn` sections produced by modern toolchains (binutils >= 2.36).
//
// Also patches the GTK plugin to exclude VMware's bundled libraries from
// recursive find, preventing deployment failures caused by VMware's old
// libgdk_pixbuf depending on the missing libcroco-0.6.so.3.
//
// Also patches the GStreamer plugin's AppRun hook generator, which has
// two upstream bugs that combine to break WebKitGTK media init inside
// the AppImage:
//
//   1) The scanner / PTP helper paths point to "${APPDIR}/usr/lib/
//      gstreamer1.0/gstreamer-1.0/" — that doubled segment is the
//      plugin's `helpers_target_dir`, but on distros without a multiarch
//      helpers dir (e.g. Arch Linux) the plugin's helpers copy step
//      leaves it empty; the scanner actually lands alongside the
//      plugins in "${APPDIR}/usr/lib/gstreamer-1.0/" via the plugins
//      copy loop. The hook therefore points GStreamer at a non-existent
//      binary, and gst_plugin_loader_spawn() falls back to the
//      build-time `GST_PLUGIN_SCANNER_INSTALLED` path baked into
//      libgstreamer — which also doesn't resolve inside the AppImage.
//   2) GST_REGISTRY_REUSE_PLUGIN_SCANNER="no" forces GStreamer to
//      respawn the (broken) scanner once per plugin. With ~270 bundled
//      plugins that's ~270 fork-exec failures and a flood of "External
//      plugin loader failed" warnings, adding ~5s to startup.
//
// Note: the `_1_0` suffix on GST_PLUGIN_SCANNER_1_0 / GST_PTP_HELPER_1_0
// is *not* a typo. GStreamer reads the suffixed name first and falls
// back to the unsuffixed one (gstreamer/gst/gstpluginloader.c:~469 and
// libgst/gst/gstptpclock.c:~2777) — same pattern as
// GST_PLUGIN_SYSTEM_PATH_1_0. Leave the names alone.
//
// This script is idempotent — it only patches once and skips subsequent
// runs. Every patch states its marker, and every anchor that upstream may
// move is REQUIRED: when one is absent the script fails instead of making
// a substitution that does nothing.
//
// The downloads come through scripts/download.mjs, which retries
// transient failures the same way every other build download does.
import { spawnSync } from 'node:child_process'
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  readFileSync,
  renameSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { downloadReleaseAsset } from '../../../scripts/download.mjs'

// Tier 1 covers the app's only confirmed media use case: HTML5 <audio>
// playback of MP3 files via WebKitGTK's GStreamer backend (playbin →
// decodebin → mpegaudioparse → mpg123audiodec → audioconvert →
// audioresample → autoaudiosink → pulsesink/alsasink). Tier 2 adds tiny
// low-risk extras WebKit's media layer benefits from (appsrc/appsink for
// MediaSource, souphttpsrc for HTTP playbin URIs, coretracers for
// diagnostics). Tier 3 keeps the common HTML5 audio fallbacks (Ogg/Vorbis,
// Opus, FLAC, WAV) so we don't break those formats if anything embeds
// them.
//
// Everything else is dropped — video codecs, hardware encoders, WebRTC
// stack, streaming protocols (HLS/DASH/RTSP/RTP), MIDI, JACK, OpenGL/
// Vulkan, exotic codecs (codec2, flite). This also lets linuxdeploy skip
// bundling those plugins' transitive shared libs (libcodec2, libx265,
// libavcodec, libavfilter, libplacebo, libSvtAv1Enc, libaom,
// libflite_*, etc.) — saving ~120 MB off the AppImage on top of the
// ~25 MB of plugin .so files themselves.
//
// Adding new plugins later means: (1) update PLUGIN_ALLOWLIST below,
// (2) delete ~/.cache/tauri/linuxdeploy-plugin-gstreamer.sh to force
// a re-download + re-patch, and (3) rebuild the AppImage. The
// `LEAPMUX_PLUGIN_ALLOWLIST` marker guards the patch.
export const PLUGIN_ALLOWLIST = [
  'libgstcoreelements.so', // Tier 1: pipeline plumbing
  'libgstplayback.so', // Tier 1: playbin / decodebin
  'libgsttypefindfunctions.so', // Tier 1: format detection
  'libgstaudioparsers.so', // Tier 1: mpegaudioparse
  'libgstmpg123.so', // Tier 1: MP3 decoder
  'libgstid3demux.so', // Tier 1: ID3v2 tag stripping
  'libgstaudioconvert.so', // Tier 1: format conversion
  'libgstaudioresample.so', // Tier 1: sample-rate conversion
  'libgstvolume.so', // Tier 1: <audio> volume
  'libgstautodetect.so', // Tier 1: autoaudiosink
  'libgstpulseaudio.so', // Tier 1: PulseAudio sink
  'libgstalsa.so', // Tier 1: ALSA fallback sink
  'libgstapp.so', // Tier 2: appsrc/appsink (MediaSource)
  'libgstsoup.so', // Tier 2: souphttpsrc (HTTP URIs)
  'libgstcoretracers.so', // Tier 2: tracing / diagnostics
  'libgstogg.so', // Tier 3: Ogg demuxer
  'libgstvorbis.so', // Tier 3: Vorbis decoder
  'libgstopus.so', // Tier 3: Opus decoder
  'libgstflac.so', // Tier 3: FLAC decoder
  'libgstwavparse.so', // Tier 3: WAV parser
]

const LINUXDEPLOY_URL = 'https://github.com/tauri-apps/binary-releases/releases/download/linuxdeploy/'
const GTK_PLUGIN_URL = 'https://raw.githubusercontent.com/tauri-apps/linuxdeploy-plugin-gtk/master/linuxdeploy-plugin-gtk.sh'
const GSTREAMER_PLUGIN_URL = 'https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gstreamer/master/linuxdeploy-plugin-gstreamer.sh'

// The GTK plugin's recursive find. The shell source spells the find
// parentheses with backslashes, which is why the anchor is a literal.
const GTK_FIND_ANCHOR = 'find "$directory" \\( -type l -o -type f \\)'
const GTK_FIND_PATCHED = 'find "$directory" -not -path "*/vmware/*" \\( -type l -o -type f \\)'

// The GStreamer AppRun hook generator. The `_1_0` names are GStreamer's
// own suffixed lookups (see the header), and the doubled path is the
// upstream helpers_target_dir bug of note 1.
const GST_TOKEN = 'GST_REGISTRY_REUSE_PLUGIN_SCANNER'
const GST_TOKEN_PATCHED = 'GST_REGISTRY_FORK'
const GST_DOUBLED_PATH = `\${APPDIR}/usr/lib/gstreamer1.0/gstreamer-1.0/`
const GST_SINGLE_PATH = `\${APPDIR}/usr/lib/gstreamer1.0/`

const ALLOWLIST_ANCHOR = '    echo "Copying plugin:'

/** The cached linuxdeploy tooling that the Linux packaging build consumes. */
export function linuxdeployPaths(env, arch) {
  const cacheDir = join(env.HOME, '.cache', 'tauri')
  return {
    cacheDir,
    appImage: join(cacheDir, `linuxdeploy-${arch}.AppImage`),
    extracted: join(cacheDir, 'linuxdeploy-extracted'),
    gtkPlugin: join(cacheDir, 'linuxdeploy-plugin-gtk.sh'),
    gstreamerPlugin: join(cacheDir, 'linuxdeploy-plugin-gstreamer.sh'),
    wrapper: join(cacheDir, 'linuxdeploy-wrapper'),
  }
}

/**
 * Exclude VMware's bundled libraries from the GTK plugin's recursive
 * find. Refuses to patch a plugin whose find line moved.
 */
export function patchGtkPlugin(text) {
  if (!text.includes(GTK_FIND_ANCHOR)) {
    throw new Error(
      'patch-linuxdeploy: the GTK plugin no longer contains the recursive find to patch; upstream changed it.',
    )
  }
  return text.replaceAll(GTK_FIND_ANCHOR, GTK_FIND_PATCHED)
}

/**
 * Fix the GStreamer AppRun hook: force in-process plugin loading and drop
 * the doubled helpers path where the plugin still writes it. Refuses to
 * patch a plugin that no longer sets the scanner flag.
 */
export function patchGstreamerHook(text) {
  if (!text.includes(GST_TOKEN)) {
    throw new Error(
      'patch-linuxdeploy: the GStreamer plugin no longer sets GST_REGISTRY_REUSE_PLUGIN_SCANNER; upstream changed it.',
    )
  }
  return text
    .replaceAll(GST_TOKEN, GST_TOKEN_PATCHED)
    .replaceAll(GST_DOUBLED_PATH, GST_SINGLE_PATH)
}

/**
 * Wrap the GStreamer plugin's copy loop in a plugin allowlist. Refuses to
 * patch a plugin whose copy step moved.
 */
export function injectPluginAllowlist(text, allowlist = PLUGIN_ALLOWLIST) {
  const insertion = [
    '    # LEAPMUX_PLUGIN_ALLOWLIST: only bundle plugins we actually use,',
    '    # so unused plugins are neither shipped nor scanned at registry',
    '    # build time. See desktop/rust/scripts/patch-linuxdeploy.mjs.',
    `    case "$(basename "$i")" in ${allowlist.join('|')}) ;; *) echo "Skipping plugin (not in leapmux allowlist): $i"; continue ;; esac`,
  ]
  const out = []
  let inserted = 0
  for (const line of text.split('\n')) {
    if (line.startsWith(ALLOWLIST_ANCHOR)) {
      out.push(...insertion)
      inserted++
    }
    out.push(line)
  }
  if (inserted === 0) {
    throw new Error(
      'patch-linuxdeploy: the GStreamer plugin no longer copies plugins with the expected echo line; upstream changed it.',
    )
  }
  return out.join('\n')
}

function runOrFail(run, command, args, options) {
  const result = run(command, args, { encoding: 'utf8', ...options })
  if (result.error)
    throw result.error
  if (result.status !== 0) {
    const output = String(result.stderr ?? result.stdout ?? '').trim()
    throw new Error(`${command} failed with exit code ${result.status}${output
      ? `: ${output}`
      : '.'}`)
  }
  return result
}

function isExecutableFile(path) {
  try {
    return statSync(path).isFile() && (statSync(path).mode & 0o111) !== 0
  }
  catch {
    return false
  }
}

async function ensureDownload(url, target, label, { download, log }) {
  if (existsSync(target))
    return
  log(`patch-linuxdeploy: downloading ${label} ...`)
  await download(url, target)
  chmodSync(target, 0o755)
}

/**
 * Patch the cached linuxdeploy AppImage in place: extract it, swap the
 * bundled strip for the system one, and replace the AppImage with the
 * compiled wrapper. The .orig backup and the extracted directory only
 * exist after a successful patching run, so they are the marker.
 */
function patchAppImage({ paths, scriptDir, run, log }) {
  const appRun = join(paths.extracted, 'AppRun')
  if (existsSync(`${paths.appImage}.orig`) && existsSync(paths.extracted) && isExecutableFile(appRun)) {
    log('patch-linuxdeploy: linuxdeploy AppImage already patched; skipping.')
    return
  }
  log('patch-linuxdeploy: extracting linuxdeploy AppImage ...')
  rmSync(join(paths.cacheDir, 'squashfs-root'), { recursive: true, force: true })
  rmSync(paths.extracted, { recursive: true, force: true })
  runOrFail(run, paths.appImage, ['--appimage-extract'], { cwd: paths.cacheDir, stdio: 'ignore' })
  renameSync(join(paths.cacheDir, 'squashfs-root'), paths.extracted)

  log('patch-linuxdeploy: replacing bundled strip with system strip ...')
  const strip = join(paths.extracted, 'usr', 'bin', 'strip')
  rmSync(strip, { force: true })
  symlinkSync('/usr/bin/strip', strip)

  log('patch-linuxdeploy: installing compiled wrapper binary ...')
  // Compile into the cache directory, never next to the source: the
  // desktop/rust/scripts/**/* globs are build-desktop-linux sources, and a
  // task whose commands write into its own sources re-runs on every later
  // invocation (Task hashes sources before it runs commands), which fails
  // check-build-cache on a cold tauri cache.
  if (!isExecutableFile(paths.wrapper)) {
    log('patch-linuxdeploy: compiling wrapper ...')
    runOrFail(run, 'gcc', [
      '-static',
      '-O2',
      '-o',
      paths.wrapper,
      join(scriptDir, 'linuxdeploy-wrapper.c'),
    ], {})
  }
  renameSync(paths.appImage, `${paths.appImage}.orig`)
  copyFileSync(paths.wrapper, paths.appImage)
  chmodSync(paths.appImage, 0o755)
}

/**
 * Download (when absent) and patch the cached linuxdeploy tooling. Every
 * download goes through the shared retrying downloader.
 */
export async function patchLinuxdeploy({
  env = process.env,
  run = spawnSync,
  download = downloadReleaseAsset,
  log = console.log,
} = {}) {
  const arch = runOrFail(run, 'uname', ['-m'], { encoding: 'utf8' }).stdout.trim()
  const paths = linuxdeployPaths(env, arch)
  const scriptDir = dirname(fileURLToPath(import.meta.url))
  mkdirSync(paths.cacheDir, { recursive: true })

  await ensureDownload(`${LINUXDEPLOY_URL}linuxdeploy-${arch}.AppImage`, paths.appImage, 'linuxdeploy', { download, log })
  patchAppImage({ paths, scriptDir, run, log })

  await ensureDownload(GTK_PLUGIN_URL, paths.gtkPlugin, 'GTK plugin', { download, log })
  let gtk = readFileSync(paths.gtkPlugin, 'utf8')
  if (!gtk.includes('vmware')) {
    log('patch-linuxdeploy: patching GTK plugin to exclude VMware libraries ...')
    gtk = patchGtkPlugin(gtk)
    writeFileSync(paths.gtkPlugin, gtk)
  }

  await ensureDownload(GSTREAMER_PLUGIN_URL, paths.gstreamerPlugin, 'GStreamer plugin', { download, log })
  let gstreamer = readFileSync(paths.gstreamerPlugin, 'utf8')
  const before = gstreamer
  if (!gstreamer.includes(GST_TOKEN_PATCHED)) {
    log('patch-linuxdeploy: patching GStreamer plugin AppRun hook ...')
    gstreamer = patchGstreamerHook(gstreamer)
  }
  if (!gstreamer.includes('LEAPMUX_PLUGIN_ALLOWLIST')) {
    log('patch-linuxdeploy: patching GStreamer plugin to use allowlist ...')
    gstreamer = injectPluginAllowlist(gstreamer)
  }
  if (gstreamer !== before)
    writeFileSync(paths.gstreamerPlugin, gstreamer)

  log('patch-linuxdeploy: done.')
}

const isMain = process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href

if (isMain) {
  patchLinuxdeploy({}).catch((error) => {
    console.error(error.message)
    process.exitCode = 1
  })
}
