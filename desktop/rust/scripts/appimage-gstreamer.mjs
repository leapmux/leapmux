#!/usr/bin/env node
// Limits the GStreamer plugins that the Linux AppImage bundles, and fixes the
// AppRun hook that the GStreamer plugin of linuxdeploy writes.
//
// `tauri build` runs its own linuxdeploy with the linuxdeploy plugins that the
// Tauri CLI embeds, and it rewrites those plugin scripts in its cache before
// each run, so the build cannot patch them. The build works around the plugin
// instead, in two subcommands that `task build-desktop-linux` runs:
//
// - `stage <dir>` copies the allowlisted plugins of the host's GStreamer
//   plugin directory into <dir>. The task passes <dir> to `tauri build` as
//   GSTREAMER_PLUGINS_DIR, which the GStreamer plugin reads in place of the
//   host directory, so linuxdeploy deploys only those plugins and the shared
//   libraries that they need.
// - `patch-appdir <appdir>` fixes the AppRun hook in the AppDir that
//   `tauri build` leaves, before the task repacks the AppDir, and fails
//   unless the AppDir holds exactly the allowlisted plugins and the fixed
//   hook. A Tauri upgrade that changes either step then fails the build
//   instead of shipping every plugin again.
//
// The hook that the GStreamer plugin writes has two upstream bugs that
// combine to break WebKitGTK media init inside the AppImage:
//
//   1) Some versions of the hook point the scanner / PTP helper paths at
//      "${APPDIR}/usr/lib/gstreamer1.0/gstreamer-1.0/" — that doubled segment is the
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
// The fix replaces GST_REGISTRY_REUSE_PLUGIN_SCANNER with
// GST_REGISTRY_FORK. With GST_REGISTRY_FORK="no", GStreamer loads every
// plugin in-process and never spawns the scanner (gst/gstregistry.c), so the
// fix covers both bugs and leaves the helper paths as the plugin writes them.
//
// Note: the `_1_0` suffix on GST_PLUGIN_SCANNER_1_0 / GST_PTP_HELPER_1_0
// is *not* a typo. GStreamer reads the suffixed name first and falls
// back to the unsuffixed one (gstreamer/gst/gstpluginloader.c:~469 and
// gstreamer/libs/gst/net/gstptpclock.c:~2784) — same pattern as
// GST_PLUGIN_SYSTEM_PATH_1_0. Leave the names alone.
import { copyFileSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { machine } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'

// Without the allowlist, the GStreamer plugin bundles every plugin in the
// build host's /usr/lib/<arch>-linux-gnu/gstreamer-1.0/ directory (~270 .so
// files on Ubuntu 24.04). GStreamer scans each of them when it builds its
// registry, and each one whose dependencies do not resolve on the host distro
// logs a warning (e.g. libgstfluidsynthmidi.so pulls in libjack, which on
// Arch's pipewire-jack fails to resolve pw_log_topic_register). The allowlist
// below keeps only the plugins that the app uses.
//
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
// Adding a plugin later means: update PLUGIN_ALLOWLIST below and rebuild the
// AppImage. Each build stages the plugins again.
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

// The scanner flag in the AppRun hook of the GStreamer plugin (see the header).
const GST_TOKEN = 'GST_REGISTRY_REUSE_PLUGIN_SCANNER'
const GST_TOKEN_PATCHED = 'GST_REGISTRY_FORK'

/** The AppRun hook that the GStreamer plugin writes into an AppDir. */
export function gstreamerHookPath(appDir) {
  return join(appDir, 'apprun-hooks', 'linuxdeploy-plugin-gstreamer.sh')
}

/** The directory of an AppDir that holds the deployed GStreamer plugins. */
export function deployedPluginDir(appDir) {
  return join(appDir, 'usr', 'lib', 'gstreamer-1.0')
}

/**
 * The host directory that the GStreamer plugin of linuxdeploy copies plugins
 * from when GSTREAMER_PLUGINS_DIR is unset: the multiarch directory when it
 * exists, else /usr/lib/gstreamer-1.0. The plugin spells the architecture
 * with `uname -m`, the value that os.machine() returns.
 */
export function hostPluginDir({ arch = machine(), exists = existsSync } = {}) {
  const multiarch = `/usr/lib/${arch}-linux-gnu/gstreamer-1.0`
  return exists(multiarch)
    ? multiarch
    : '/usr/lib/gstreamer-1.0'
}

/**
 * Copy the allowlisted plugins of the host directory into an empty staging
 * directory. Logs each allowlisted plugin that the host lacks, and fails when
 * the host holds none of them.
 */
export function stagePlugins(stageDir, { hostDir = hostPluginDir(), log = console.log } = {}) {
  rmSync(stageDir, { recursive: true, force: true })
  mkdirSync(stageDir, { recursive: true })
  const staged = []
  for (const plugin of PLUGIN_ALLOWLIST) {
    const source = join(hostDir, plugin)
    if (!existsSync(source)) {
      log(`appimage-gstreamer: the host has no ${plugin} in ${hostDir}; the AppImage bundles it only when the host has it.`)
      continue
    }
    copyFileSync(source, join(stageDir, plugin))
    staged.push(plugin)
  }
  if (staged.length === 0)
    throw new Error(`appimage-gstreamer: ${hostDir} holds none of the allowlisted GStreamer plugins.`)
  log(`appimage-gstreamer: staged ${staged.length} GStreamer plugins in ${stageDir}.`)
  return staged
}

/**
 * Fix the GStreamer AppRun hook: force in-process plugin loading, so
 * GStreamer never spawns the plugin scanner. Refuses a hook that no longer
 * sets the scanner flag.
 */
export function patchGstreamerHook(text) {
  if (!text.includes(GST_TOKEN)) {
    throw new Error(
      'appimage-gstreamer: the GStreamer AppRun hook no longer sets GST_REGISTRY_REUSE_PLUGIN_SCANNER; upstream changed it.',
    )
  }
  return text.replaceAll(GST_TOKEN, GST_TOKEN_PATCHED)
}

/**
 * Fix the AppRun hook of an AppDir that `tauri build` left, and verify that
 * the AppDir holds the fixed hook and exactly the allowlisted plugins, so a
 * change in Tauri's bundling fails the build instead of shipping every
 * plugin again.
 */
export function patchAppDir(appDir, { log = console.log } = {}) {
  const hook = gstreamerHookPath(appDir)
  if (!existsSync(hook))
    throw new Error(`appimage-gstreamer: the AppDir holds no GStreamer AppRun hook at ${hook}.`)
  const text = readFileSync(hook, 'utf8')
  // A second run over the same AppDir finds the hook already fixed.
  if (!text.includes(GST_TOKEN_PATCHED) || text.includes(GST_TOKEN))
    writeFileSync(hook, patchGstreamerHook(text))
  const pluginDir = deployedPluginDir(appDir)
  const plugins = existsSync(pluginDir)
    ? readdirSync(pluginDir).filter(name => name.endsWith('.so')).sort()
    : []
  const unexpected = plugins.filter(plugin => !PLUGIN_ALLOWLIST.includes(plugin))
  if (unexpected.length > 0)
    throw new Error(`appimage-gstreamer: the AppDir bundles GStreamer plugins outside the allowlist: ${unexpected.join(', ')}. The build did not pass the staged plugins to linuxdeploy.`)
  if (plugins.length === 0)
    throw new Error(`appimage-gstreamer: the AppDir holds no GStreamer plugin in ${pluginDir}.`)
  log(`appimage-gstreamer: the AppDir bundles ${plugins.length} allowlisted GStreamer plugins and the fixed AppRun hook.`)
  return plugins
}

const USAGE = 'Use `stage <dir>` or `patch-appdir <appdir>`.'

if (import.meta.main) {
  try {
    const [command, path, ...rest] = process.argv.slice(2)
    if (!path || rest.length > 0)
      throw new Error(`appimage-gstreamer: ${USAGE}`)
    if (command === 'stage')
      stagePlugins(path)
    else if (command === 'patch-appdir')
      patchAppDir(path)
    else
      throw new Error(`appimage-gstreamer: unknown command ${command}. ${USAGE}`)
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
