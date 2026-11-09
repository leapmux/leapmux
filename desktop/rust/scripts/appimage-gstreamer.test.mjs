import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, readdirSync, readFileSync, symlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { scratchDirectories } from '../../../scripts/test-support.mjs'
import {
  deployedPluginDir,
  gstreamerHookPath,
  hostPluginDir,
  patchAppDir,
  patchGstreamerHook,
  PLUGIN_ALLOWLIST,
  stagePlugins,
} from './appimage-gstreamer.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)
const fixture = () => scratch.create('appimage-gstreamer')

// The GStreamer 1.0 AppRun hook that Tauri CLI 2.12.4 embeds
// (crates/tauri-bundler/src/bundle/linux/appimage/linuxdeploy-plugin-gstreamer.sh,
// lines 143-150), copied exactly.
const GST_HOOK = [
  '#! /bin/bash',
  '',
  'export GST_REGISTRY_REUSE_PLUGIN_SCANNER="no"',
  `export GST_PLUGIN_SYSTEM_PATH_1_0="\${APPDIR}/usr/lib/gstreamer-1.0"`,
  `export GST_PLUGIN_PATH_1_0="\${APPDIR}/usr/lib/gstreamer-1.0"`,
  '',
  `export GST_PLUGIN_SCANNER_1_0="\${APPDIR}/usr/lib/gstreamer1.0/gstreamer-1.0/gst-plugin-scanner"`,
  `export GST_PTP_HELPER_1_0="\${APPDIR}/usr/lib/gstreamer1.0/gstreamer-1.0/gst-ptp-helper"`,
  '',
].join('\n')
// The same hook in linuxdeploy-plugin-gstreamer master, which finds the
// scanner at run time (lines 143-154), copied exactly.
const GST_RUNTIME_FIND_HOOK = [
  '#! /bin/bash',
  '',
  'export GST_REGISTRY_REUSE_PLUGIN_SCANNER="no"',
  `export GST_PLUGIN_SYSTEM_PATH_1_0="\${APPDIR}/usr/lib/gstreamer-1.0"`,
  `export GST_PLUGIN_PATH_1_0="\${APPDIR}/usr/lib/gstreamer-1.0"`,
  '',
  `GST_SCANNER="$(find "\${APPDIR}/usr/lib" -type f -name gst-plugin-scanner 2>/dev/null | head -n1)"`,
  `GST_PTP_HELPER="$(find "\${APPDIR}/usr/lib" -type f -name gst-ptp-helper 2>/dev/null | head -n1)"`,
  '',
  '[ -n "$GST_SCANNER" ]    && export GST_PLUGIN_SCANNER_1_0="$GST_SCANNER"',
  '[ -n "$GST_PTP_HELPER" ] && export GST_PTP_HELPER_1_0="$GST_PTP_HELPER"',
  '',
].join('\n')
/** The hook with only the scanner flag replaced, as the fix must leave it. */
function forkingHook(hook) {
  return hook.replace('export GST_REGISTRY_REUSE_PLUGIN_SCANNER="no"', 'export GST_REGISTRY_FORK="no"')
}

/** An AppDir as `tauri build` leaves it: the GStreamer hook and the deployed plugins. */
function appDirFixture({ hook = GST_HOOK, plugins = PLUGIN_ALLOWLIST.slice(0, 3) } = {}) {
  const appDir = join(fixture(), 'leapmux-desktop.AppDir')
  mkdirSync(join(appDir, 'apprun-hooks'), { recursive: true })
  writeFileSync(gstreamerHookPath(appDir), hook)
  mkdirSync(deployedPluginDir(appDir), { recursive: true })
  for (const plugin of plugins)
    writeFileSync(join(deployedPluginDir(appDir), plugin), 'ELF')
  return appDir
}

describe('patchGstreamerHook', () => {
  it('forces in-process plugin loading and keeps the helper paths', () => {
    // With GST_REGISTRY_FORK="no", GStreamer loads every plugin in-process
    // and never spawns the scanner, so the helper paths stay as written.
    expect(patchGstreamerHook(GST_HOOK)).toBe(forkingHook(GST_HOOK))
  })

  it('patches a hook that finds the scanner at run time', () => {
    expect(patchGstreamerHook(GST_RUNTIME_FIND_HOOK)).toBe(forkingHook(GST_RUNTIME_FIND_HOOK))
  })

  it('refuses a hook that no longer sets the scanner flag', () => {
    expect(() => patchGstreamerHook('export GST_PLUGIN_SYSTEM_PATH_1_0="/x"\n'))
      .toThrow('the GStreamer AppRun hook no longer sets GST_REGISTRY_REUSE_PLUGIN_SCANNER')
  })
})

describe('hostPluginDir', () => {
  it('prefers the multiarch directory, as the GStreamer plugin does', () => {
    expect(hostPluginDir({
      arch: 'x86_64',
      exists: path => path === '/usr/lib/x86_64-linux-gnu/gstreamer-1.0',
    }))
      .toBe('/usr/lib/x86_64-linux-gnu/gstreamer-1.0')
    expect(hostPluginDir({
      arch: 'aarch64',
      exists: () => false,
    })).toBe('/usr/lib/gstreamer-1.0')
  })
})

describe('stagePlugins', () => {
  it('copies only the allowlisted plugins that the host has into an empty directory', () => {
    const hostDir = fixture()
    for (const plugin of [PLUGIN_ALLOWLIST[0], PLUGIN_ALLOWLIST[1], 'libgstx265.so'])
      writeFileSync(join(hostDir, plugin), plugin)
    const stageDir = join(fixture(), 'stage')
    mkdirSync(stageDir)
    writeFileSync(join(stageDir, 'stale.so'), 'stale')
    const logs = []
    const staged = stagePlugins(stageDir, {
      hostDir,
      log: line => logs.push(line),
    })
    expect(staged).toEqual([PLUGIN_ALLOWLIST[0], PLUGIN_ALLOWLIST[1]])
    expect(readdirSync(stageDir).sort()).toEqual([PLUGIN_ALLOWLIST[0], PLUGIN_ALLOWLIST[1]].sort())
    expect(readFileSync(join(stageDir, PLUGIN_ALLOWLIST[0]), 'utf8')).toBe(PLUGIN_ALLOWLIST[0])
    expect(logs.join('\n')).toContain(`the host has no ${PLUGIN_ALLOWLIST[2]}`)
    expect(logs.at(-1)).toBe(`appimage-gstreamer: staged 2 GStreamer plugins in ${stageDir}.`)
  })

  it('fails when the host has none of the allowlisted plugins', () => {
    const hostDir = fixture()
    writeFileSync(join(hostDir, 'libgstx265.so'), 'x')
    expect(() => stagePlugins(join(fixture(), 'stage'), {
      hostDir,
      log: () => {},
    }))
      .toThrow('holds none of the allowlisted GStreamer plugins')
  })
})

describe('patchAppDir', () => {
  it('fixes the hook and accepts an AppDir that bundles only allowlisted plugins', () => {
    const appDir = appDirFixture()
    expect(patchAppDir(appDir, {
      log: () => {},
    })).toEqual(PLUGIN_ALLOWLIST.slice(0, 3).sort())
    expect(readFileSync(gstreamerHookPath(appDir), 'utf8')).toBe(forkingHook(GST_HOOK))
    // A second run over the same AppDir keeps the fixed hook.
    patchAppDir(appDir, {
      log: () => {},
    })
    expect(readFileSync(gstreamerHookPath(appDir), 'utf8')).toBe(forkingHook(GST_HOOK))
  })

  it('fails when the AppDir bundles a plugin outside the allowlist', () => {
    const appDir = appDirFixture({
      plugins: [PLUGIN_ALLOWLIST[0], 'libgstx265.so'],
    })
    expect(() => patchAppDir(appDir, {
      log: () => {},
    }))
      .toThrow('the AppDir bundles GStreamer plugins outside the allowlist: libgstx265.so.')
  })

  it('fails when the AppDir bundles no plugin or has no hook', () => {
    expect(() => patchAppDir(appDirFixture({ plugins: [] }), {
      log: () => {},
    })).toThrow('the AppDir holds no GStreamer plugin')
    const appDir = join(fixture(), 'empty.AppDir')
    mkdirSync(appDir)
    expect(() => patchAppDir(appDir, {
      log: () => {},
    })).toThrow('the AppDir holds no GStreamer AppRun hook')
  })

  it('fails on a hook that no longer sets the scanner flag', () => {
    const appDir = appDirFixture({ hook: '#! /bin/bash\n' })
    expect(() => patchAppDir(appDir, {
      log: () => {},
    })).toThrow('no longer sets GST_REGISTRY_REUSE_PLUGIN_SCANNER')
  })
})

describe('appimage-gstreamer.mjs', () => {
  const run = (script, args) => spawnSync('node', [script, ...args], { encoding: 'utf8' })

  it('fixes an AppDir from the command line', () => {
    const appDir = appDirFixture()
    const result = run(join(import.meta.dirname, 'appimage-gstreamer.mjs'), ['patch-appdir', appDir])
    expect(result.status, result.stderr).toBe(0)
    expect(readFileSync(gstreamerHookPath(appDir), 'utf8')).toBe(forkingHook(GST_HOOK))
  })

  it('refuses an unknown command, a missing path, or an extra argument', () => {
    const script = join(import.meta.dirname, 'appimage-gstreamer.mjs')
    const unknown = run(script, [
      'patch',
      fixture(),
    ])
    expect(unknown.status).toBe(1)
    expect(unknown.stderr).toContain('unknown command patch')
    const usage = 'appimage-gstreamer: Use `stage <dir>` or `patch-appdir <appdir>`.'
    const missing = run(script, ['stage'])
    expect(missing.status).toBe(1)
    expect(missing.stderr.trim()).toBe(usage)
    const appDir = appDirFixture()
    const extra = run(script, [
      'patch-appdir',
      appDir,
      'more',
    ])
    expect(extra.status).toBe(1)
    expect(extra.stderr.trim()).toBe(usage)
    // The refused command changes nothing.
    expect(readFileSync(gstreamerHookPath(appDir), 'utf8')).toBe(GST_HOOK)
  })

  it.skipIf(process.platform === 'win32')('runs when started through a symlinked directory', () => {
    // Node resolves the entry module to its real path, while argv keeps the
    // symlinked one. A checkout under a symlinked home (/home → /var/home)
    // reaches the script that way, and the script must still run.
    const appDir = appDirFixture()
    const link = join(fixture(), 'scripts')
    symlinkSync(import.meta.dirname, link)
    const result = run(join(link, 'appimage-gstreamer.mjs'), ['patch-appdir', appDir])
    expect(result.status, result.stderr).toBe(0)
    expect(result.stdout).toContain('appimage-gstreamer: the AppDir bundles 3 allowlisted GStreamer plugins')
    expect(existsSync(gstreamerHookPath(appDir))).toBe(true)
  })
})
