import { chmodSync, existsSync, lstatSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { scratchDirectories } from '../../../scripts/test-support.mjs'
import {
  injectPluginAllowlist,
  linuxdeployPaths,
  patchGstreamerHook,
  patchGtkPlugin,
  patchLinuxdeploy,
  PLUGIN_ALLOWLIST,
} from './patch-linuxdeploy.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)
const fixture = () => scratch.create('patch-linuxdeploy')

// The upstream lines that the patches anchor on.
const GTK_FIND_LINE = '    done < <(find "$directory" \\( -type l -o -type f \\) -name "$library" -print0)'
const GST_HOOK = [
  'export GST_REGISTRY_REUSE_PLUGIN_SCANNER="no"',
  `export GST_PLUGIN_SCANNER_1_0="\${APPDIR}/usr/lib/gstreamer1.0/gstreamer-1.0/gst-plugin-scanner"`,
  `export GST_PLUGIN_SYSTEM_PATH_1_0="\${APPDIR}/usr/lib/gstreamer1.0"`,
].join('\n')
const GST_COPY_LOOP = [
  'for i in "$plugins_dir"/*.so; do',
  '    echo "Copying plugin: $i"',
  '    cp "$i" "$plugins_target_dir"',
  'done',
].join('\n')

describe('patchGtkPlugin', () => {
  it('excludes VMware directories from the recursive find', () => {
    const patched = patchGtkPlugin(`# library lookup\n${GTK_FIND_LINE}\n`)
    expect(patched).toContain('find "$directory" -not -path "*/vmware/*" \\( -type l -o -type f \\)')
    expect(patched).not.toContain('find "$directory" \\( -type l -o -type f \\)')
  })

  it('refuses a plugin whose find line moved', () => {
    expect(() => patchGtkPlugin('find "$directory" -name "$library"\n'))
      .toThrow('the GTK plugin no longer contains the recursive find')
  })
})

describe('patchGstreamerHook', () => {
  it('forces in-process plugin loading and fixes the doubled helper path', () => {
    const patched = patchGstreamerHook(GST_HOOK)
    expect(patched).not.toContain('GST_REGISTRY_REUSE_PLUGIN_SCANNER')
    expect(patched).toContain('export GST_REGISTRY_FORK="no"')
    expect(patched).not.toContain('gstreamer1.0/gstreamer-1.0')
    expect(patched).toContain(`export GST_PLUGIN_SCANNER_1_0="\${APPDIR}/usr/lib/gstreamer1.0/gst-plugin-scanner"`)
  })

  it('keeps a hook that already writes the single path', () => {
    const patched = patchGstreamerHook(`export GST_REGISTRY_REUSE_PLUGIN_SCANNER="no"\nexport GST_PLUGIN_SCANNER_1_0="\${APPDIR}/usr/lib/gstreamer1.0/gst-plugin-scanner"\n`)
    expect(patched).toContain(`export GST_PLUGIN_SCANNER_1_0="\${APPDIR}/usr/lib/gstreamer1.0/gst-plugin-scanner"`)
    expect(patched).not.toContain('gstreamer-1.0/gstreamer-1.0')
  })

  it('refuses a plugin that no longer sets the scanner flag', () => {
    expect(() => patchGstreamerHook('export GST_PLUGIN_SYSTEM_PATH_1_0="/x"\n'))
      .toThrow('the GStreamer plugin no longer sets GST_REGISTRY_REUSE_PLUGIN_SCANNER')
  })
})

describe('injectPluginAllowlist', () => {
  it('wraps the plugin copy loop in the allowlist', () => {
    const patched = injectPluginAllowlist(GST_COPY_LOOP)
    const lines = patched.split('\n')
    const insertion = lines.findIndex(line => line.includes('LEAPMUX_PLUGIN_ALLOWLIST'))
    const echo = lines.findIndex(line => line.startsWith('    echo "Copying plugin:'))
    expect(insertion).toBeGreaterThanOrEqual(0)
    expect(insertion).toBeLessThan(echo)
    expect(patched).toContain(`case "$(basename "$i")" in ${PLUGIN_ALLOWLIST.join('|')}) ;; *) echo "Skipping plugin (not in leapmux allowlist): $i"; continue ;; esac`)
  })

  it('refuses a plugin whose copy step moved', () => {
    expect(() => injectPluginAllowlist('for i in *.so; do\ndone\n'))
      .toThrow('the GStreamer plugin no longer copies plugins with the expected echo line')
  })
})

describe('patchLinuxdeploy', () => {
  const gtkPlugin = `# lookup\n${GTK_FIND_LINE}\n`
  const gstreamerPlugin = `${GST_HOOK}\n${GST_COPY_LOOP}\n`

  function fakeDownload() {
    const calls = []
    return {
      calls,
      download: async (url, destination) => {
        calls.push(url)
        const name = url.split('/').pop()
        if (name === 'linuxdeploy-x86_64.AppImage')
          writeFileSync(destination, 'appimage binary\n')
        else if (name === 'linuxdeploy-plugin-gtk.sh')
          writeFileSync(destination, gtkPlugin)
        else
          writeFileSync(destination, gstreamerPlugin)
      },
    }
  }

  function fakeRun(commands) {
    return (command, args, options) => {
      const name = basename(command)
      commands.push([name, ...args])
      if (name === 'uname')
        return { status: 0, stdout: 'x86_64\n', stderr: '' }
      if (args?.[0] === '--appimage-extract') {
        const root = join(options.cwd, 'squashfs-root')
        mkdirSync(join(root, 'usr', 'bin'), { recursive: true })
        writeFileSync(join(root, 'AppRun'), '#!/bin/sh\n')
        chmodSync(join(root, 'AppRun'), 0o755)
        writeFileSync(join(root, 'usr', 'bin', 'strip'), 'bundled strip\n')
        return { status: 0, stdout: '', stderr: '' }
      }
      if (name === 'gcc') {
        writeFileSync(args[args.indexOf('-o') + 1], 'wrapper binary\n')
        chmodSync(args[args.indexOf('-o') + 1], 0o755)
        return { status: 0, stdout: '', stderr: '' }
      }
      throw new Error(`unexpected command: ${name}`)
    }
  }

  it('downloads the tooling and patches every piece in place', async () => {
    const home = fixture()
    const env = { HOME: home }
    const paths = linuxdeployPaths(env, 'x86_64')
    const { calls, download } = fakeDownload()
    const commands = []
    await patchLinuxdeploy({
      env,
      run: fakeRun(commands),
      download,
      log: () => {},
    })

    expect(calls.map(url => url.split('/').pop())).toEqual([
      'linuxdeploy-x86_64.AppImage',
      'linuxdeploy-plugin-gtk.sh',
      'linuxdeploy-plugin-gstreamer.sh',
    ])
    expect(calls[0]).toBe('https://github.com/tauri-apps/binary-releases/releases/download/linuxdeploy/linuxdeploy-x86_64.AppImage')
    // The wrapper replaced the AppImage and the original moved aside.
    expect(readFileSync(paths.appImage, 'utf8')).toBe('wrapper binary\n')
    expect(readFileSync(`${paths.appImage}.orig`, 'utf8')).toBe('appimage binary\n')
    // The extraction moved into place and the bundled strip became a link
    // to the system strip.
    expect(existsSync(join(paths.extracted, 'AppRun'))).toBe(true)
    expect(lstatSync(join(paths.extracted, 'usr', 'bin', 'strip')).isSymbolicLink()).toBe(true)
    expect(commands.map(([name]) => name)).toEqual(['uname', 'linuxdeploy-x86_64.AppImage', 'gcc'])
    expect(commands[2].at(-1)).toEndWith(join('desktop', 'rust', 'scripts', 'linuxdeploy-wrapper.c'))
    expect(readFileSync(paths.gtkPlugin, 'utf8')).toContain('-not -path "*/vmware/*"')
    const gstreamer = readFileSync(paths.gstreamerPlugin, 'utf8')
    expect(gstreamer).toContain('GST_REGISTRY_FORK')
    expect(gstreamer).toContain('LEAPMUX_PLUGIN_ALLOWLIST')
    expect(gstreamer).not.toContain('GST_REGISTRY_REUSE_PLUGIN_SCANNER')
    expect(gstreamer).not.toContain('gstreamer-1.0/gstreamer-1.0')
  })

  it('skips every download and patch on a second run', async () => {
    const home = fixture()
    const env = { HOME: home }
    const paths = linuxdeployPaths(env, 'x86_64')
    const { calls, download } = fakeDownload()
    const logs = []
    const log = message => logs.push(message)
    await patchLinuxdeploy({
      env,
      run: fakeRun([]),
      download,
      log,
    })
    const commands = []
    await patchLinuxdeploy({
      env,
      run: fakeRun(commands),
      download,
      log,
    })

    expect(calls).toHaveLength(3)
    expect(commands.map(([name]) => name)).toEqual(['uname'])
    expect(logs.join('\n')).toContain('linuxdeploy AppImage already patched; skipping.')
    expect(readFileSync(paths.appImage, 'utf8')).toBe('wrapper binary\n')
  })

  it('fails when the AppImage extraction fails', async () => {
    const home = fixture()
    const env = { HOME: home }
    const { download } = fakeDownload()
    const run = (command) => {
      if (basename(command) === 'uname')
        return { status: 0, stdout: 'x86_64\n', stderr: '' }
      return { status: 1, stderr: 'extraction failed' }
    }
    await expect(patchLinuxdeploy({
      env,
      run,
      download,
      log: () => {},
    }))
      .rejects
      .toThrow('failed with exit code 1: extraction failed')
  })
})
