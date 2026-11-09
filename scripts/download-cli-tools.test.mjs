import { Buffer } from 'node:buffer'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { rootActivePath } from './cache-policy.mjs'
import {
  canonicalToolNames,
  cliToolsBinDir,
  cliToolVersionEnvs,
  DIGESTS_FILE,
  extractArchive,
  findExtractedExecutable,
  formatToolDigests,
  installCliTools,
  installExtractedTool,
  parseCommandLine,
  parseToolDigests,
  pinnedToolReleases,
  syncToolDigests,
  toolIdentity,
  toolIdentityHash,
  toolRelease,
} from './download-cli-tools.mjs'
import { createStoredZip, scratchDirectories, writeFiles } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)
const fixture = () => scratch.create('cli-tools')

const pins = {
  PROTOC_VERSION: '36.2',
  BUF_VERSION: '1.75.0',
  TASK_VERSION: '3.54.0',
}

// The extraction commands are platform-specific, so each extraction test
// that runs a real extractor runs the branch of its own host. `task
// test-scripts` runs this file on Linux amd64, and `task test-cache-native`
// runs it on macOS, Windows and Linux arm64, so CI runs every real extractor.
// The Windows branch also runs below against a recording runner, which
// checks the PowerShell command on every host.
const currentOs = process.env.RUNNER_OS
  ?? (process.platform === 'win32'
    ? 'Windows'
    : process.platform === 'darwin'
      ? 'macOS'
      : 'Linux')
function binName(name) {
  return name + (currentOs === 'Windows'
    ? '.exe'
    : '')
}

describe('toolRelease', () => {
  it('identifies the release asset of every tool and runner platform', () => {
    const cases = [
      [
        'protoc',
        'Linux',
        'X64',
        'protoc-36.2-linux-x86_64.zip',
        'zip',
      ],
      [
        'protoc',
        'Linux',
        'ARM64',
        'protoc-36.2-linux-aarch_64.zip',
        'zip',
      ],
      [
        'protoc',
        'macOS',
        'X64',
        'protoc-36.2-osx-x86_64.zip',
        'zip',
      ],
      [
        'protoc',
        'macOS',
        'ARM64',
        'protoc-36.2-osx-aarch_64.zip',
        'zip',
      ],
      [
        'protoc',
        'Windows',
        'X64',
        'protoc-36.2-win64.zip',
        'zip',
      ],
      [
        'buf',
        'Linux',
        'X64',
        'buf-Linux-x86_64',
        'binary',
      ],
      [
        'buf',
        'Linux',
        'ARM64',
        'buf-Linux-aarch64',
        'binary',
      ],
      [
        'buf',
        'macOS',
        'X64',
        'buf-Darwin-x86_64',
        'binary',
      ],
      [
        'buf',
        'macOS',
        'ARM64',
        'buf-Darwin-arm64',
        'binary',
      ],
      [
        'buf',
        'Windows',
        'X64',
        'buf-Windows-x86_64.exe',
        'binary',
      ],
      [
        'buf',
        'Windows',
        'ARM64',
        'buf-Windows-arm64.exe',
        'binary',
      ],
      [
        'task',
        'Linux',
        'X64',
        'task_linux_amd64.tar.gz',
        'tar.gz',
      ],
      [
        'task',
        'Linux',
        'ARM64',
        'task_linux_arm64.tar.gz',
        'tar.gz',
      ],
      [
        'task',
        'macOS',
        'X64',
        'task_darwin_amd64.tar.gz',
        'tar.gz',
      ],
      [
        'task',
        'macOS',
        'ARM64',
        'task_darwin_arm64.tar.gz',
        'tar.gz',
      ],
      [
        'task',
        'Windows',
        'X64',
        'task_windows_amd64.zip',
        'zip',
      ],
    ]
    for (const [name, os, arch, asset, archiveKind] of cases) {
      const version = { protoc: '36.2', buf: '1.75.0', task: '3.54.0' }[name]
      const release = toolRelease(name, { version, os, arch })
      expect(release.asset, `${name} ${os} ${arch}`).toBe(asset)
      expect(release.archiveKind, `${name} ${os} ${arch}`).toBe(archiveKind)
    }
  })

  it('builds the download URL from the repository, the v-tag and the asset', () => {
    expect(toolRelease('protoc', { version: '36.2', os: 'Linux', arch: 'X64' }).url)
      .toBe('https://github.com/protocolbuffers/protobuf/releases/download/v36.2/protoc-36.2-linux-x86_64.zip')
    expect(toolRelease('buf', { version: '1.75.0', os: 'macOS', arch: 'ARM64' }).url)
      .toBe('https://github.com/bufbuild/buf/releases/download/v1.75.0/buf-Darwin-arm64')
    expect(toolRelease('task', { version: '3.54.0', os: 'Windows', arch: 'X64' }).url)
      .toBe('https://github.com/go-task/task/releases/download/v3.54.0/task_windows_amd64.zip')
  })

  it('composes the installed executable name and the include flag per tool', () => {
    expect(toolRelease('protoc', { version: '36.2', os: 'Linux', arch: 'X64' }).binName).toBe('protoc')
    expect(toolRelease('buf', { version: '1.75.0', os: 'Windows', arch: 'X64' }).binName).toBe('buf.exe')
    expect(toolRelease('protoc', { version: '36.2', os: 'Linux', arch: 'X64' }).wellKnownIncludes).toBe(true)
    expect(toolRelease('task', { version: '3.54.0', os: 'Linux', arch: 'X64' }).wellKnownIncludes).toBe(false)
  })

  it('refuses a platform that the release publishes no asset for', () => {
    expect(() => toolRelease('protoc', { version: '36.2', os: 'Windows', arch: 'ARM64' }))
      .toThrow('The protoc release publishes no asset for Windows ARM64.')
    expect(() => toolRelease('buf', { version: '1.75.0', os: 'Linux', arch: 'RISCV' }))
      .toThrow('publishes no asset')
  })

  it('refuses an unknown tool', () => {
    expect(() => toolRelease('sed', { version: '1', os: 'Linux', arch: 'X64' })).toThrow('Unknown tool sed')
  })
})

describe('canonicalToolNames', () => {
  it('sorts and de-duplicates the requested tool names', () => {
    expect(canonicalToolNames('task, protoc,task')).toEqual(['protoc', 'task'])
    expect(canonicalToolNames('buf,buf,protoc')).toEqual(['buf', 'protoc'])
  })

  it('rejects an empty or unknown request', () => {
    expect(() => canonicalToolNames('')).toThrow('empty')
    expect(() => canonicalToolNames('sed')).toThrow('Unknown tool sed')
  })
})

describe('toolIdentity', () => {
  it('lists the requested tools and only their pins', () => {
    expect(toolIdentity('task', pins)).toBe('tools=task\ntask=3.54.0')
    expect(toolIdentity('protoc,task', pins)).toBe('tools=protoc,task\nprotoc=36.2\ntask=3.54.0')
  })

  it('is stable under the request order', () => {
    expect(toolIdentity('buf,protoc', pins)).toBe(toolIdentity('protoc,buf', pins))
  })

  it('fails when a requested pin is missing', () => {
    expect(() => toolIdentity('task', {})).toThrow('TASK_VERSION')
  })
})

describe('toolIdentityHash', () => {
  it('hashes the archive identity', () => {
    expect(toolIdentityHash('task', pins)).toBe(createHash('sha256').update('tools=task\ntask=3.54.0').digest('hex'))
  })

  it('stays equal under the request order and changes with a pin', () => {
    expect(toolIdentityHash('buf,protoc', pins)).toBe(toolIdentityHash('protoc,buf', pins))
    expect(toolIdentityHash('task', { ...pins, TASK_VERSION: '3.55.0' })).not.toBe(toolIdentityHash('task', pins))
  })
})

describe('cliToolVersionEnvs', () => {
  it('lists the pin of every CLI tool', () => {
    expect(cliToolVersionEnvs()).toEqual(['BUF_VERSION', 'PROTOC_VERSION', 'TASK_VERSION'])
  })
})

describe('pinnedToolReleases', () => {
  it('lists every tool on every runner platform that its release serves', () => {
    const releases = pinnedToolReleases(pins)
    expect(releases).toHaveLength(17)
    // protoc publishes no Windows ARM64 build.
    expect(releases.filter(release => release.name === 'protoc').map(release => release.asset)).toEqual([
      'protoc-36.2-linux-x86_64.zip',
      'protoc-36.2-linux-aarch_64.zip',
      'protoc-36.2-osx-x86_64.zip',
      'protoc-36.2-osx-aarch_64.zip',
      'protoc-36.2-win64.zip',
    ])
    expect(releases.filter(release => release.name === 'task').map(release => release.asset)).toContain('task_windows_arm64.zip')
  })
})

describe('parseToolDigests', () => {
  const digest = 'a'.repeat(64)

  it('reads one digest per release row and skips comments', () => {
    const digests = parseToolDigests(`# comment\n${digest}  task 3.54.0 task_linux_amd64.tar.gz\n\n`)
    expect([...digests]).toEqual([
      ['task 3.54.0 task_linux_amd64.tar.gz', digest],
    ])
  })

  it('refuses a malformed line and a duplicate row', () => {
    expect(() => parseToolDigests(`${digest} task 3.54.0 task_linux_amd64.tar.gz`)).toThrow('Line 1 of the CLI tool digest table is malformed')
    expect(() => parseToolDigests(`${'A'.repeat(64)}  task 3.54.0 x`)).toThrow('malformed')
    expect(() => parseToolDigests(`${digest}  task 3.54.0\n`)).toThrow('malformed')
    const row = `${digest}  task 3.54.0 task_linux_amd64.tar.gz`
    expect(() => parseToolDigests(`${row}\n${row}\n`)).toThrow('lists task 3.54.0 task_linux_amd64.tar.gz twice')
  })

  it('reads back what formatToolDigests writes, sorted by row', () => {
    const digests = new Map([
      [
        'task 3.54.0 task_linux_amd64.tar.gz',
        'b'.repeat(64),
      ],
      ['buf 1.75.0 buf-Linux-x86_64', digest],
    ])
    const text = formatToolDigests(digests)
    expect([
      ...parseToolDigests(text).keys(),
    ]).toEqual(['buf 1.75.0 buf-Linux-x86_64', 'task 3.54.0 task_linux_amd64.tar.gz'])
    expect(text.endsWith('\n')).toBe(true)
  })
})

describe('the pinned digest table', () => {
  it('pins every release row of the versions in versions.env', () => {
    // A pin bump that skips `task sync-cli-tool-digests` fails here, not in
    // a CI job that misses the cache.
    const versions = Object.fromEntries(readFileSync(join(import.meta.dirname, '..', 'versions.env'), 'utf8')
      .split('\n')
      .filter(line => /^[A-Z_][A-Z0-9_]*=/.test(line))
      .map(line => line.split(/=(.*)/s, 2)))
    const digests = parseToolDigests(readFileSync(DIGESTS_FILE, 'utf8'))
    const rows = pinnedToolReleases(versions).map(release => `${release.name} ${release.version} ${release.asset}`)
    expect([
      ...digests.keys(),
    ].sort()).toEqual(rows.sort())
  })
})

describe('syncToolDigests', () => {
  it('keeps the pinned rows, hashes each new row, and drops the rows of an older version', async () => {
    const dir = fixture()
    const file = join(dir, 'cli-tools.sha256')
    const kept = 'c'.repeat(64)
    const releases = pinnedToolReleases(pins)
    const keptRelease = releases.find(release => release.name === 'buf')
    writeFileSync(file, formatToolDigests(new Map([
      [`buf 1.75.0 ${keptRelease.asset}`, kept],
      [
        'task 3.53.0 task_linux_amd64.tar.gz',
        'd'.repeat(64),
      ],
    ])))
    const downloaded = []
    const synced = await syncToolDigests({
      env: pins,
      file,
      scratchRoot: join(dir, 'scratch'),
      download: async (url, destination) => {
        downloaded.push(url.split('/').pop())
        writeFileSync(destination, url)
      },
    })
    expect(downloaded).toHaveLength(releases.length - 1)
    expect(downloaded).not.toContain(keptRelease.asset)
    const written = parseToolDigests(readFileSync(file, 'utf8'))
    expect(Object.fromEntries(written)).toEqual(Object.fromEntries(synced))
    expect(written.get(`buf 1.75.0 ${keptRelease.asset}`)).toBe(kept)
    expect(written.has('task 3.53.0 task_linux_amd64.tar.gz')).toBe(false)
    const task = releases.find(release => release.asset === 'task_linux_amd64.tar.gz')
    expect(written.get('task 3.54.0 task_linux_amd64.tar.gz')).toBe(createHash('sha256').update(task.url).digest('hex'))
    // The scratch downloads are gone.
    expect(readdirSync(join(dir, 'scratch'))).toEqual([])
    expect(readFileSync(file, 'utf8')).toBe(formatToolDigests(written))
  })

  it('writes nothing when a download fails', async () => {
    const dir = fixture()
    const file = join(dir, 'cli-tools.sha256')
    writeFileSync(file, '# empty\n')
    await expect(syncToolDigests({
      env: pins,
      file,
      scratchRoot: join(dir, 'scratch'),
      download: async () => {
        throw new Error('HTTP 503')
      },
    })).rejects.toThrow('HTTP 503')
    expect(readFileSync(file, 'utf8')).toBe('# empty\n')
  })
})

describe('findExtractedExecutable', () => {
  const tree = () => {
    const dir = fixture()
    writeFiles(dir, {
      'bin/protoc': 'binary',
      'include/google/protobuf/descriptor.proto': 'syntax;',
      'readme.txt': 'hello',
    })
    return dir
  }

  it('finds the executable nested in the extracted tree', () => {
    const dir = tree()
    expect(findExtractedExecutable(dir, 'protoc')).toBe(join(dir, 'bin', 'protoc'))
  })

  it('reports the extracted entries when the executable is absent', () => {
    expect(() => findExtractedExecutable(tree(), 'task'))
      .toThrow('The extracted archive holds no task.')
    expect(() => findExtractedExecutable(tree(), 'task')).toThrow('readme.txt')
  })

  it('refuses an archive that holds the executable twice', () => {
    const dir = fixture()
    writeFiles(dir, {
      'task': 'binary',
      'nested/task': 'binary',
    })
    expect(() => findExtractedExecutable(dir, 'task')).toThrow('2 files named task')
  })
})

describe('installExtractedTool', () => {
  it('installs the executable and the protoc well-known includes', () => {
    const extracted = fixture()
    writeFiles(extracted, {
      'bin/protoc': 'binary',
      'include/google/protobuf/descriptor.proto': 'syntax;',
    })
    const targetDir = fixture()
    const release = toolRelease('protoc', { version: '36.2', os: 'Linux', arch: 'X64' })
    const installed = installExtractedTool({
      release,
      extracted,
      targetDir,
    })
    expect(installed).toBe(join(targetDir, 'bin', 'protoc'))
    // The extracted tree keeps its files: the install copies them.
    expect(existsSync(join(extracted, 'bin', 'protoc'))).toBe(true)
    expect(readFileSync(installed, 'utf8')).toBe('binary')
    expect(readFileSync(join(targetDir, 'include', 'google', 'protobuf', 'descriptor.proto'), 'utf8')).toBe('syntax;')
  })

  it('refuses a protoc archive without an include directory', () => {
    const extracted = fixture()
    writeFiles(extracted, { 'bin/protoc': 'binary' })
    const release = toolRelease('protoc', { version: '36.2', os: 'Linux', arch: 'X64' })
    expect(() => installExtractedTool({
      release,
      extracted,
      targetDir: fixture(),
    }))
      .toThrow('no include directory beside its bin directory')
  })

  it('installs a tool with no includes as its executable alone', () => {
    const extracted = fixture()
    writeFiles(extracted, { task: 'binary', LICENSE: 'terms' })
    const targetDir = fixture()
    const release = toolRelease('task', { version: '3.54.0', os: 'Linux', arch: 'X64' })
    installExtractedTool({
      release,
      extracted,
      targetDir,
    })
    expect(existsSync(join(targetDir, 'bin', 'task'))).toBe(true)
    expect(existsSync(join(targetDir, 'include'))).toBe(false)
  })
})

describe('extractArchive', () => {
  it('extracts a tar.gz archive with the platform tar', () => {
    const dir = fixture()
    const tree = join(dir, 'tree')
    writeFiles(tree, { task: 'binary', LICENSE: 'terms' })
    const archive = join(dir, 'task.tar.gz')
    const created = spawnSync('tar', [
      '-czf',
      archive,
      '-C',
      tree,
      'task',
      'LICENSE',
    ], { encoding: 'utf8' })
    expect(created.status, created.stderr).toBe(0)
    const destination = join(dir, 'out')
    extractArchive(archive, destination, { os: currentOs })
    expect(readFileSync(join(destination, 'task'), 'utf8')).toBe('binary')
    expect(readFileSync(join(destination, 'LICENSE'), 'utf8')).toBe('terms')
  })

  it('extracts a zip archive with the platform extractor', () => {
    const dir = fixture()
    const archive = join(dir, 'protoc.zip')
    writeFileSync(archive, createStoredZip({
      'bin/protoc': 'binary',
      'include/google/protobuf/descriptor.proto': 'syntax;',
    }))
    const destination = join(dir, 'out')
    extractArchive(archive, destination, { os: currentOs })
    expect(readFileSync(join(destination, 'bin', 'protoc'), 'utf8')).toBe('binary')
    expect(readFileSync(join(destination, 'include', 'google', 'protobuf', 'descriptor.proto'), 'utf8')).toBe('syntax;')
  })

  it('passes the Windows paths to Expand-Archive through the environment', () => {
    const dir = fixture()
    const calls = []
    const archive = 'C:\\Users\\O\'Brien\\work\\task.zip'
    const destination = join(dir, 'out')
    extractArchive(archive, destination, {
      os: 'Windows',
      run: (command, args, options) => {
        calls.push({ command, args, options })
        return { status: 0, stdout: '', stderr: '' }
      },
    })
    expect(calls).toHaveLength(1)
    expect(calls[0].command).toBe('powershell.exe')
    // A quote in a path cannot end a string of the command, because the
    // command holds no path.
    expect(calls[0].args.at(-1)).toBe('Expand-Archive -LiteralPath $env:LEAPMUX_EXTRACT_ARCHIVE -DestinationPath $env:LEAPMUX_EXTRACT_DESTINATION -Force')
    expect(calls[0].args.join(' ')).not.toContain('Brien')
    expect(calls[0].options.env.LEAPMUX_EXTRACT_ARCHIVE).toBe(archive)
    expect(calls[0].options.env.LEAPMUX_EXTRACT_DESTINATION).toBe(destination)
  })

  it('reports the output of a failed extractor', () => {
    const dir = fixture()
    expect(() => extractArchive(join(dir, 'task.zip'), join(dir, 'out'), {
      os: 'Linux',
      run: () => ({ status: 9, stdout: '', stderr: 'unzip: cannot find zipfile directory\n' }),
    })).toThrow('unzip failed with exit code 9: unzip: cannot find zipfile directory')
  })

  it('refuses an archive without an extraction rule', () => {
    const dir = fixture()
    const archive = join(dir, 'tool.7z')
    writeFileSync(archive, 'bytes')
    expect(() => extractArchive(archive, join(dir, 'out'), { os: currentOs })).toThrow('No extraction rule')
  })
})

describe('installCliTools', () => {
  /** A runner environment with fresh home and workspace directories. */
  function installFixture(pinEnv = pins) {
    const home = fixture()
    const workspace = fixture()
    const env = {
      RUNNER_OS: currentOs,
      RUNNER_ARCH: 'X64',
      HOME: home,
      USERPROFILE: home,
      ...pinEnv,
    }
    return {
      workspace,
      env,
      binDir: cliToolsBinDir(env, workspace),
    }
  }

  /**
   * Fixture release archives of the pinned versions for this host, the fake
   * download that serves them, and the digest table that pins their bytes.
   */
  function fixtureDownload(workspace) {
    const assets = new Map()
    const digests = new Map()
    for (const name of ['protoc', 'buf', 'task']) {
      const version = { protoc: pins.PROTOC_VERSION, buf: pins.BUF_VERSION, task: pins.TASK_VERSION }[name]
      const release = toolRelease(name, { version, os: currentOs, arch: 'X64' })
      let bytes
      if (release.archiveKind === 'binary') {
        bytes = Buffer.from('binary')
      }
      else if (release.archiveKind === 'zip') {
        bytes = createStoredZip(name === 'protoc'
          ? { [`bin/${binName('protoc')}`]: 'binary', 'include/google/protobuf/descriptor.proto': 'syntax;' }
          : { [binName('task')]: 'binary' })
      }
      else {
        const tree = join(workspace, `asset-tree-${name}`)
        writeFiles(tree, { [binName(name)]: 'binary' })
        const archive = join(workspace, release.asset)
        const created = spawnSync('tar', [
          '-czf',
          archive,
          '-C',
          tree,
          binName(name),
        ], { encoding: 'utf8' })
        if (created.status !== 0)
          throw new Error(created.stderr)
        bytes = readFileSync(archive)
      }
      assets.set(release.asset, bytes)
      digests.set(`${name} ${version} ${release.asset}`, createHash('sha256').update(bytes).digest('hex'))
    }
    const served = []
    const download = async (url, destination) => {
      const asset = url.split('/').pop()
      served.push(asset)
      writeFileSync(destination, assets.get(asset))
    }
    return { served, download, digests }
  }

  /** A runner that answers the version probes of `binDir` with `probe`, and runs everything else. */
  function runner(binDir, probe = () => ({ status: 0, stdout: 'fixture 1.0\n', stderr: '' })) {
    const probes = []
    const run = (command, args, options) => {
      if (command.startsWith(binDir)) {
        probes.push([command, args])
        return probe()
      }
      return spawnSync(command, args, options)
    }
    return { probes, run }
  }

  it('installs the requested tools and probes each installed binary', async () => {
    const { workspace, env, binDir } = installFixture()
    const { served, download, digests } = fixtureDownload(workspace)
    const { probes, run } = runner(binDir)
    const installed = await installCliTools({
      tools: 'protoc,buf,task',
      env,
      workspace,
      download,
      digests,
      run,
    })
    expect(installed).toEqual([
      join(binDir, binName('buf')),
      join(binDir, binName('protoc')),
      join(binDir, binName('task')),
    ])
    expect(readFileSync(join(binDir, binName('protoc')), 'utf8')).toBe('binary')
    expect(readFileSync(join(rootActivePath('cli-tools', 'tools', env, workspace), 'include', 'google', 'protobuf', 'descriptor.proto'), 'utf8')).toBe('syntax;')
    expect(served.length).toBe(3)
    expect(probes).toEqual(installed.map(command => [
      command,
      ['--version'],
    ]))
    expect(existsSync(join(workspace, '.tmp', 'ci-cache', 'download-cli-tools'))).toBe(false)
  })

  it('installs only the requested subset', async () => {
    const { workspace, env, binDir } = installFixture()
    const { served, download, digests } = fixtureDownload(workspace)
    const { run } = runner(binDir)
    await installCliTools({
      tools: 'task',
      env,
      workspace,
      download,
      digests,
      run,
    })
    expect(served).toEqual([
      toolRelease('task', { version: pins.TASK_VERSION, os: currentOs, arch: 'X64' }).asset,
    ])
    expect(existsSync(join(binDir, binName('task')))).toBe(true)
    expect(existsSync(join(binDir, binName('protoc')))).toBe(false)
    expect(existsSync(join(binDir, binName('buf')))).toBe(false)
  })

  it('fails when an installed binary reports no version', async () => {
    const { workspace, env, binDir } = installFixture()
    const { download, digests } = fixtureDownload(workspace)
    const { run } = runner(binDir, () => ({ status: 1, stdout: '', stderr: 'exec format error' }))
    await expect(installCliTools({
      tools: 'buf',
      env,
      workspace,
      download,
      digests,
      run,
    }))
      .rejects
      .toThrow('failed with exit code 1: exec format error')
  })

  it('reports the standard output of a failed probe that writes nothing to standard error', async () => {
    const { workspace, env, binDir } = installFixture()
    const { download, digests } = fixtureDownload(workspace)
    const { run } = runner(binDir, () => ({ status: 3, stdout: 'bad CPU type in executable\n', stderr: '' }))
    await expect(installCliTools({
      tools: 'buf',
      env,
      workspace,
      download,
      digests,
      run,
    }))
      .rejects
      .toThrow(`${binName('buf')} failed with exit code 3: bad CPU type in executable`)
  })

  it('names the signal that stopped a probe', async () => {
    const { workspace, env, binDir } = installFixture()
    const { download, digests } = fixtureDownload(workspace)
    const { run } = runner(binDir, () => ({
      status: null,
      signal: 'SIGKILL',
      stdout: '',
      stderr: '',
    }))
    await expect(installCliTools({
      tools: 'buf',
      env,
      workspace,
      download,
      digests,
      run,
    }))
      .rejects
      .toThrow(`${binName('buf')} was stopped by SIGKILL.`)
  })

  it('refuses a download whose sha256 differs from its pinned digest, before it installs anything', async () => {
    const { workspace, env, binDir } = installFixture()
    const { download, digests } = fixtureDownload(workspace)
    const release = toolRelease('task', { version: pins.TASK_VERSION, os: currentOs, arch: 'X64' })
    const key = `task ${pins.TASK_VERSION} ${release.asset}`
    const pinned = digests.get(key)
    digests.set(key, 'e'.repeat(64))
    const { probes, run } = runner(binDir)
    await expect(installCliTools({
      tools: 'task',
      env,
      workspace,
      download,
      digests,
      run,
    }))
      .rejects
      .toThrow(`The task asset ${release.asset} has sha256 ${pinned}, but scripts/cli-tools.sha256 pins ${'e'.repeat(64)}.`)
    expect(existsSync(join(binDir, binName('task')))).toBe(false)
    expect(probes).toEqual([])
  })

  it('refuses a release without a pinned digest before anything downloads', async () => {
    const { workspace, env, binDir } = installFixture()
    const { served, download } = fixtureDownload(workspace)
    const { run } = runner(binDir)
    await expect(installCliTools({
      tools: 'task',
      env,
      workspace,
      download,
      digests: new Map(),
      run,
    }))
      .rejects
      .toThrow('scripts/cli-tools.sha256 pins no sha256 for task 3.54.0')
    expect(served).toEqual([])
  })

  it('fails when a requested pin is missing from the environment', async () => {
    const { workspace, env, binDir } = installFixture({})
    const { download, digests } = fixtureDownload(workspace)
    const { run } = runner(binDir)
    await expect(installCliTools({
      tools: 'task',
      env,
      workspace,
      download,
      digests,
      run,
    }))
      .rejects
      .toThrow('TASK_VERSION')
  })
})

describe('cliToolsBinDir', () => {
  it('places the executables in the bin directory of the cli-tools snapshot root', () => {
    const env = { RUNNER_OS: 'Linux', RUNNER_ARCH: 'X64', HOME: '/home/fixture' }
    expect(cliToolsBinDir(env, '/work')).toBe(join(rootActivePath('cli-tools', 'tools', env, '/work'), 'bin'))
    const windows = { RUNNER_OS: 'Windows', RUNNER_ARCH: 'X64', USERPROFILE: '/c/fixture' }
    expect(cliToolsBinDir(windows, '/work')).toBe(join('/c/fixture', 'AppData', 'Local', 'leapmux-cli-tools', 'bin'))
  })
})

describe('parseCommandLine', () => {
  it('reads the sync-digests mode alone', () => {
    expect(parseCommandLine(['--sync-digests'])).toEqual({ mode: 'sync-digests' })
    expect(() => parseCommandLine(['--sync-digests', '--tools', 'task'])).toThrow('--sync-digests takes no other option.')
    expect(() => parseCommandLine(['--sync-digests', '--bin-dir'])).toThrow('takes no other option.')
  })

  it('reads the install, identity, and bin-dir modes', () => {
    expect(parseCommandLine(['--tools', 'task'])).toEqual({ mode: 'install', tools: 'task' })
    expect(parseCommandLine(['--tools=protoc,task', '--identity'])).toEqual({ mode: 'identity', tools: 'protoc,task' })
    expect(parseCommandLine(['--bin-dir'])).toEqual({ mode: 'bin-dir' })
  })

  it('requires a tool list outside the bin-dir mode', () => {
    expect(() => parseCommandLine([])).toThrow('--tools needs a comma-separated tool list.')
    expect(() => parseCommandLine(['--identity'])).toThrow('--tools needs a comma-separated tool list.')
  })

  it('refuses another option beside --bin-dir', () => {
    expect(() => parseCommandLine(['--bin-dir', '--tools', 'task'])).toThrow('--bin-dir takes no other option.')
    expect(() => parseCommandLine(['--bin-dir', '--identity'])).toThrow('--bin-dir takes no other option.')
  })

  it('refuses an unknown option or a positional argument, with the usage', () => {
    expect(() => parseCommandLine(['--frobnicate'])).toThrow('--frobnicate')
    expect(() => parseCommandLine(['--frobnicate'])).toThrow('Use --tools <list> to install')
    expect(() => parseCommandLine(['task'])).toThrow('task')
  })
})

describe('the command line', () => {
  const script = join(import.meta.dirname, 'download-cli-tools.mjs')

  it('rejects an unknown argument', () => {
    const result = spawnSync('node', [script, '--frobnicate'], { encoding: 'utf8' })
    expect(result.status).toBe(1)
    expect(result.stderr).toContain('--frobnicate')
    expect(result.stderr).toContain('Use --tools <list> to install')
  })

  it('rejects --tools without a value', () => {
    const result = spawnSync('node', [script, '--tools'], { encoding: 'utf8' })
    expect(result.status).toBe(1)
    expect(result.stderr).toContain('--tools')
  })

  it('prints the archive identity hash and the bin directory', () => {
    const identity = spawnSync('node', [
      script,
      '--tools',
      'task',
      '--identity',
    ], {
      encoding: 'utf8',
      env: {
        ...process.env,
        ...pins,
      },
    })
    expect(identity.status, identity.stderr).toBe(0)
    expect(identity.stdout).toBe(`${toolIdentityHash('task', pins)}\n`)
    const home = fixture()
    const env = {
      ...process.env,
      RUNNER_OS: 'Linux',
      RUNNER_ARCH: 'X64',
      HOME: home,
    }
    const binDir = spawnSync('node', [script, '--bin-dir'], { encoding: 'utf8', env, cwd: home })
    expect(binDir.status, binDir.stderr).toBe(0)
    expect(binDir.stdout).toBe(`${cliToolsBinDir(env, home)}\n`)
  })
})
