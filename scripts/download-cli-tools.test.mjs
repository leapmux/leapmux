import { spawnSync } from 'node:child_process'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it, spyOn } from 'bun:test'
import { rootActivePath } from './cache-policy.mjs'
import {
  canonicalToolNames,
  downloadReleaseAsset,
  extractArchive,
  findExtractedExecutable,
  installCliTools,
  installExtractedTool,
  parseRetryAfter,
  retryBudgetMs,
  retryDelay,
  toolIdentity,
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
// runs the branch of its own host. CI covers every branch across its three
// platform jobs.
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
        'task_3.54.0_linux_amd64.tar.gz',
        'tar.gz',
      ],
      [
        'task',
        'Linux',
        'ARM64',
        'task_3.54.0_linux_arm64.tar.gz',
        'tar.gz',
      ],
      [
        'task',
        'macOS',
        'X64',
        'task_3.54.0_darwin_amd64.tar.gz',
        'tar.gz',
      ],
      [
        'task',
        'macOS',
        'ARM64',
        'task_3.54.0_darwin_arm64.tar.gz',
        'tar.gz',
      ],
      [
        'task',
        'Windows',
        'X64',
        'task_3.54.0_windows_amd64.zip',
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
      .toBe('https://github.com/go-task/task/releases/download/v3.54.0/task_3.54.0_windows_amd64.zip')
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
    expect(canonicalToolNames(['buf', 'buf', 'protoc'])).toEqual(['buf', 'protoc'])
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
    const binDir = join(targetDir, 'bin')
    const release = toolRelease('protoc', { version: '36.2', os: 'Linux', arch: 'X64' })
    const installed = installExtractedTool({
      release,
      extracted,
      binDir,
      targetDir,
    })
    expect(installed).toBe(join(binDir, 'protoc'))
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
      binDir: join(fixture(), 'bin'),
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
      binDir: join(targetDir, 'bin'),
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

  it('refuses an archive without an extraction rule', () => {
    const dir = fixture()
    const archive = join(dir, 'tool.7z')
    writeFileSync(archive, 'bytes')
    expect(() => extractArchive(archive, join(dir, 'out'), { os: currentOs })).toThrow('No extraction rule')
  })
})

describe('downloadReleaseAsset', () => {
  /** Run `body` with console.log captured, and return the captured lines. */
  async function captureLog(body) {
    const lines = []
    const spy = spyOn(console, 'log').mockImplementation(message => lines.push(String(message)))
    try {
      await body()
    }
    finally {
      spy.mockRestore()
    }
    return lines
  }

  it('writes the response body to the destination', async () => {
    const dir = fixture()
    const destination = join(dir, 'asset')
    await downloadReleaseAsset('https://example.invalid/asset', destination, {
      fetchImpl: async () => new Response('binary-bytes'),
    })
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
  })

  it('reports the URL and the status when the download fails', async () => {
    const dir = fixture()
    let calls = 0
    await expect(downloadReleaseAsset('https://example.invalid/asset', join(dir, 'asset'), {
      fetchImpl: async () => {
        calls++
        return new Response('missing', { status: 404 })
      },
      sleep: async () => {},
    })).rejects.toThrow('Downloading https://example.invalid/asset failed with HTTP 404.')
    expect(calls).toBe(1)
  })

  it('retries a transient gateway failure and writes the body', async () => {
    const dir = fixture()
    const destination = join(dir, 'asset')
    const responses = [
      new Response('gateway timeout', { status: 504 }),
      new Response('binary-bytes'),
    ]
    const delays = []
    const lines = await captureLog(() => downloadReleaseAsset('https://example.invalid/asset', destination, {
      fetchImpl: async () => responses.shift(),
      sleep: async ms => delays.push(ms),
      random: () => 0.5,
    }))
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
    expect(delays).toEqual([750])
    expect(lines.join('\n')).toContain('Retrying (1/10) in 750ms.')
  })

  it('retries a network failure and writes the body', async () => {
    const dir = fixture()
    const destination = join(dir, 'asset')
    let calls = 0
    await downloadReleaseAsset('https://example.invalid/asset', destination, {
      fetchImpl: async () => {
        calls++
        if (calls === 1)
          throw new Error('socket hang up')
        return new Response('binary-bytes')
      },
      sleep: async () => {},
    })
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
    expect(calls).toBe(2)
  })

  it('fails after the first try and ten retries when every one is transient', async () => {
    const dir = fixture()
    let calls = 0
    const lines = await captureLog(() => expect(downloadReleaseAsset('https://example.invalid/asset', join(dir, 'asset'), {
      fetchImpl: async () => {
        calls++
        return new Response('gateway timeout', { status: 504 })
      },
      sleep: async () => {},
    })).rejects.toThrow('Downloading https://example.invalid/asset failed with HTTP 504.'))
    expect(calls).toBe(11)
    expect(lines.join('\n')).toContain('Giving up after 10 retries.')
  })

  it('backs off exponentially with jitter and caps each backoff at one minute', async () => {
    const dir = fixture()
    const delays = []
    await expect(downloadReleaseAsset('https://example.invalid/asset', join(dir, 'asset'), {
      fetchImpl: async () => new Response('gateway timeout', { status: 504 }),
      sleep: async ms => delays.push(ms),
      random: () => 0.5,
    })).rejects.toThrow('failed with HTTP 504.')
    expect(delays).toEqual([
      750,
      1500,
      3000,
      6000,
      12000,
      24000,
      48000,
      60000,
      60000,
      60000,
    ])
  })

  it('jitters each backoff between half and all of the exponential delay', () => {
    expect(retryDelay(0, () => 0)).toBe(500)
    expect(retryDelay(0, () => 1)).toBe(1000)
    expect(retryDelay(3, () => 0)).toBe(4000)
    expect(retryDelay(3, () => 1)).toBe(8000)
  })

  it('retries at the delay that Retry-After asks for', async () => {
    const dir = fixture()
    const destination = join(dir, 'asset')
    const responses = [
      new Response('too many requests', {
        status: 429,
        headers: { 'retry-after': '5' },
      }),
      new Response('binary-bytes'),
    ]
    const delays = []
    await downloadReleaseAsset('https://example.invalid/asset', destination, {
      fetchImpl: async () => responses.shift(),
      sleep: async ms => delays.push(ms),
      random: () => 0.5,
    })
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
    expect(delays).toEqual([5000])
  })

  it('waits the full Retry-After even above the one-minute cap', async () => {
    const dir = fixture()
    const destination = join(dir, 'asset')
    const responses = [
      new Response('too many requests', {
        status: 429,
        headers: { 'retry-after': '120' },
      }),
      new Response('binary-bytes'),
    ]
    const delays = []
    await downloadReleaseAsset('https://example.invalid/asset', destination, {
      fetchImpl: async () => responses.shift(),
      sleep: async ms => delays.push(ms),
    })
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
    expect(delays).toEqual([120000])
  })

  it('gives up quickly when Retry-After lies beyond the retry budget', async () => {
    const dir = fixture()
    let calls = 0
    const lines = await captureLog(() => expect(downloadReleaseAsset('https://example.invalid/asset', join(dir, 'asset'), {
      fetchImpl: async () => {
        calls++
        return new Response('service unavailable', {
          status: 503,
          headers: { 'retry-after': '600' },
        })
      },
      sleep: async () => {},
    })).rejects.toThrow('failed with HTTP 503. The server asked to retry after 600s, beyond the 303s retry budget.'))
    expect(calls).toBe(1)
    expect(lines.join('\n')).toContain('Giving up: The server asked to retry after 600s, beyond the 303s retry budget.')
  })
})

describe('parseRetryAfter', () => {
  it('reads the delta seconds of a Retry-After header', () => {
    expect(parseRetryAfter('5')).toBe(5000)
    expect(parseRetryAfter(' 30 ')).toBe(30000)
    expect(parseRetryAfter('0')).toBe(0)
  })

  it('reads the HTTP date of a Retry-After header', () => {
    const now = Date.parse('2026-11-08T10:00:00Z')
    expect(parseRetryAfter('Sun, 08 Nov 2026 10:00:30 GMT', now)).toBe(30000)
    expect(parseRetryAfter('Sun, 08 Nov 2026 09:59:30 GMT', now)).toBe(0)
  })

  it('falls back to the backoff for an absent or malformed header', () => {
    expect(parseRetryAfter(null)).toBeNull()
    expect(parseRetryAfter('')).toBeNull()
    expect(parseRetryAfter('soon')).toBeNull()
    expect(parseRetryAfter('-1')).toBeNull()
    expect(parseRetryAfter('2026-11')).toBeNull()
  })
})

describe('retryBudgetMs', () => {
  it('sums the capped backoff maxima of the retry schedule', () => {
    expect(retryBudgetMs(1)).toBe(1000)
    expect(retryBudgetMs(10)).toBe(303000)
  })
})

describe('installCliTools', () => {
  /** Fixture release archives served by the fake download. */
  function fixtureDownload(env, workspace) {
    const served = []
    const download = async (url, destination) => {
      const asset = url.split('/').pop()
      served.push(asset)
      if (asset.startsWith('buf-')) {
        writeFileSync(destination, 'binary')
        return
      }
      if (asset.endsWith('.zip')) {
        writeFileSync(destination, createStoredZip(asset.startsWith('protoc-')
          ? { [`bin/${binName('protoc')}`]: 'binary', 'include/google/protobuf/descriptor.proto': 'syntax;' }
          : { [binName('task')]: 'binary' }))
        return
      }
      const tree = join(workspace, `asset-tree-${served.length}`)
      writeFiles(tree, { [binName('task')]: 'binary' })
      const created = spawnSync('tar', [
        '-czf',
        destination,
        '-C',
        tree,
        binName('task'),
      ], { encoding: 'utf8' })
      if (created.status !== 0)
        throw new Error(created.stderr)
    }
    return { served, download }
  }

  function runner(binDir) {
    const probes = []
    const run = (command, args, options) => {
      if (command.startsWith(binDir)) {
        probes.push([command, args])
        return { status: 0, stdout: 'fixture 1.0\n' }
      }
      return spawnSync(command, args, options)
    }
    return { probes, run }
  }

  it('installs the requested tools and probes each installed binary', async () => {
    const home = fixture()
    const workspace = fixture()
    const env = {
      RUNNER_OS: currentOs,
      RUNNER_ARCH: 'X64',
      HOME: home,
      USERPROFILE: home,
      ...pins,
    }
    const binDir = join(rootActivePath('cli-tools', 'tools', env, workspace), 'bin')
    const { served, download } = fixtureDownload(env, workspace)
    const { probes, run } = runner(binDir)
    const installed = await installCliTools({
      tools: 'protoc,buf,task',
      env,
      workspace,
      download,
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
    const home = fixture()
    const workspace = fixture()
    const env = {
      RUNNER_OS: currentOs,
      RUNNER_ARCH: 'X64',
      HOME: home,
      USERPROFILE: home,
      ...pins,
    }
    const binDir = join(rootActivePath('cli-tools', 'tools', env, workspace), 'bin')
    const { served, download } = fixtureDownload(env, workspace)
    const { run } = runner(binDir)
    await installCliTools({
      tools: 'task',
      env,
      workspace,
      download,
      run,
    })
    expect(served).toEqual([
      expect.stringContaining('task_3.54.0_'),
    ])
    expect(existsSync(join(binDir, binName('task')))).toBe(true)
    expect(existsSync(join(binDir, binName('protoc')))).toBe(false)
    expect(existsSync(join(binDir, binName('buf')))).toBe(false)
  })

  it('fails when an installed binary reports no version', async () => {
    const home = fixture()
    const workspace = fixture()
    const env = {
      RUNNER_OS: currentOs,
      RUNNER_ARCH: 'X64',
      HOME: home,
      USERPROFILE: home,
      ...pins,
    }
    const binDir = join(rootActivePath('cli-tools', 'tools', env, workspace), 'bin')
    const { download } = fixtureDownload(env, workspace)
    const run = (command, args, options) => command.startsWith(binDir)
      ? { status: 1, stderr: 'exec format error' }
      : spawnSync(command, args, options)
    await expect(installCliTools({
      tools: 'buf',
      env,
      workspace,
      download,
      run,
    }))
      .rejects
      .toThrow('failed with exit code 1: exec format error')
  })

  it('fails when a requested pin is missing from the environment', async () => {
    const home = fixture()
    const workspace = fixture()
    const env = {
      RUNNER_OS: currentOs,
      RUNNER_ARCH: 'X64',
      HOME: home,
      USERPROFILE: home,
    }
    const { download } = fixtureDownload(env, workspace)
    const { run } = runner(join(home, 'bin'))
    await expect(installCliTools({
      tools: 'task',
      env,
      workspace,
      download,
      run,
    }))
      .rejects
      .toThrow('TASK_VERSION')
  })
})

describe('the command line', () => {
  const script = new URL('./download-cli-tools.mjs', import.meta.url).pathname

  it('rejects an unknown argument', () => {
    const result = spawnSync('node', [script, '--frobnicate'], { encoding: 'utf8' })
    expect(result.status).toBe(1)
    expect(result.stderr).toContain('Unknown argument --frobnicate.')
  })

  it('rejects --tools without a value', () => {
    const result = spawnSync('node', [script, '--tools'], { encoding: 'utf8' })
    expect(result.status).toBe(1)
    expect(result.stderr).toContain('--tools needs a comma-separated tool list.')
  })
})
