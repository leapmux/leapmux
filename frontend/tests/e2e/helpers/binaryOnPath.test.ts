import { chmodSync, existsSync, mkdirSync, mkdtempSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { delimiter, dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { agentSearchPath, agentSearchPathEnv, findBinary, findBinaryOnPath, lookupBinary, missingBinaryReason, unusableBinaryReason, versionOutput } from './binaryOnPath'

let directory: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'binary-on-path-test-'))
})

afterEach(() => rmSync(directory, { recursive: true, force: true }))

function file(name: string, mode: number, parent: string = directory): string {
  mkdirSync(parent, { recursive: true })
  const path = join(parent, name)
  writeFileSync(path, '#!/bin/sh\n')
  chmodSync(path, mode)
  return path
}

/** A mise shim as mise installs one: a link, named for the tool, to the mise executable. */
function miseShim(name: string, parent: string): string {
  const mise = file('mise', 0o755, join(directory, 'mise-bin'))
  mkdirSync(parent, { recursive: true })
  const shim = join(parent, name)
  symlinkSync(mise, shim)
  return shim
}

/**
 * A fake mise executable that answers `mise bin-paths` with these directories, and
 * a shim of `agent` that links to it. Returns the directory of the shim.
 */
function listingMise(installs: string[]): string {
  const mise = join(directory, 'mise-bin', 'mise')
  mkdirSync(dirname(mise), { recursive: true })
  const lines = installs.map(install => `echo '${install}'`).join('\n')
  writeFileSync(mise, `#!/bin/sh\nif [ "$1" = bin-paths ]; then\n${lines}\nfi\n`)
  chmodSync(mise, 0o755)
  const shims = join(directory, 'shims')
  mkdirSync(shims, { recursive: true })
  symlinkSync(mise, join(shims, 'agent'))
  return shims
}

describe('findBinaryOnPath', () => {
  it('finds an executable file in one of the directories', () => {
    const agent = file('agent', 0o755)
    expect(findBinaryOnPath('agent', ['', join(directory, 'absent'), directory].join(delimiter))).toBe(agent)
  })

  // A spawn takes the first match, so a later match is not the file that runs.
  it('returns the match of the first directory that holds one', () => {
    const first = file('agent', 0o755, join(directory, 'first'))
    file('agent', 0o755, join(directory, 'second'))
    expect(findBinaryOnPath('agent', [join(directory, 'first'), join(directory, 'second')].join(delimiter))).toBe(first)
  })

  it('finds nothing for an empty or absent search path', () => {
    file('agent', 0o755)
    expect(findBinaryOnPath('agent', undefined)).toBeNull()
    expect(findBinaryOnPath('agent', '')).toBeNull()
  })

  it('refuses a directory of the same name', () => {
    mkdirSync(join(directory, 'agent'))
    expect(findBinaryOnPath('agent', directory)).toBeNull()
  })

  // Windows has no execute bit, so the mode check applies where one exists.
  it.skipIf(process.platform === 'win32')('refuses a file that is not executable', () => {
    file('agent', 0o644)
    expect(findBinaryOnPath('agent', directory)).toBeNull()
  })

  it('tries each executable extension beside the bare name', () => {
    const agent = file('agent.cmd', 0o755)
    expect(findBinaryOnPath('agent', directory)).toBeNull()
    expect(findBinaryOnPath('agent', directory, '.EXE;.cmd;')).toBe(agent)
  })
})

describe('findBinary', () => {
  it('searches the path of the environment that it receives', () => {
    const agent = file('agent', 0o755)
    expect(findBinary('agent', { PATH: directory })).toBe(agent)
    expect(findBinary('agent', {})).toBeNull()
  })
})

// A link needs a privilege on Windows, and mise installs a Windows shim differently.
describe.skipIf(process.platform === 'win32')('agentSearchPath', () => {
  it('keeps a path that holds no shim directory, and asks mise nothing', () => {
    file('agent', 0o755)
    const searchPath = [directory, join(directory, 'absent')].join(delimiter)
    const asked: string[] = []
    expect(agentSearchPath(searchPath, (mise) => {
      asked.push(mise)
      return ['/never']
    })).toBe(searchPath)
    expect(asked).toEqual([])
  })

  it('keeps an absent or empty path', () => {
    expect(agentSearchPath(undefined, () => ['/never'])).toBeUndefined()
    expect(agentSearchPath('', () => ['/never'])).toBe('')
  })

  // The install directories go where the shims were, so the order of every other
  // directory stays, and the shims stay behind them for a tool that mise did not list.
  it('puts the directories that mise lists before each shim directory, and keeps the shims', () => {
    const shims = join(directory, 'shims')
    const mise = realpathSync(miseShim('agent', shims))
    const before = join(directory, 'before')
    const after = join(directory, 'after')
    const asked: string[] = []

    const resolved = agentSearchPath([before, shims, after].join(delimiter), (executable) => {
      asked.push(executable)
      return ['/install/a', '/install/b']
    })

    expect(resolved).toBe([before, '/install/a', '/install/b', shims, after].join(delimiter))
    expect(asked).toEqual([mise])
  })

  it('keeps the path when mise lists nothing or fails', () => {
    const shims = join(directory, 'shims')
    miseShim('agent', shims)
    expect(agentSearchPath(shims, () => [])).toBe(shims)
    expect(agentSearchPath(shims, () => null)).toBe(shims)
  })

  // A directory that holds the mise executable itself, as ~/.local/bin often does,
  // holds no shim, so its other programs keep their place.
  it('does not take the mise executable itself for a shim', () => {
    file('mise', 0o755, join(directory, 'bin'))
    file('agent', 0o755, join(directory, 'bin'))
    expect(agentSearchPath(join(directory, 'bin'), () => ['/never'])).toBe(join(directory, 'bin'))
  })

  it('asks the real mise executable behind the shims', () => {
    const install = join(directory, 'install')
    const shims = listingMise([install, join(directory, 'second')])
    expect(agentSearchPath(shims)).toBe([install, join(directory, 'second'), shims].join(delimiter))
  })

  // Each spawn of the run resolves the same PATH, and mise costs a process start.
  it('asks the real mise once for each distinct path', () => {
    const install = join(directory, 'install')
    const shims = listingMise([install])
    expect(agentSearchPath(shims)).toBe([install, shims].join(delimiter))
    // A mise that now lists another directory is not asked: the first answer stays.
    writeFileSync(join(directory, 'mise-bin', 'mise'), `#!/bin/sh\necho '${join(directory, 'other')}'\n`)
    expect(agentSearchPath(shims)).toBe([install, shims].join(delimiter))
  })

  it('asks an injected lister on every call, so no answer outlives its test', () => {
    const shims = join(directory, 'shims')
    miseShim('agent', shims)
    let asked = 0
    const lister = () => {
      asked++
      return [`/install/${asked}`]
    }
    expect(agentSearchPath(shims, lister)).toBe(['/install/1', shims].join(delimiter))
    expect(agentSearchPath(shims, lister)).toBe(['/install/2', shims].join(delimiter))
  })

  it('keeps each empty entry of the path in its place', () => {
    const shims = join(directory, 'shims')
    miseShim('agent', shims)
    expect(agentSearchPath(['', shims, ''].join(delimiter), () => ['/install'])).toBe(['', '/install', shims, ''].join(delimiter))
  })

  // Homebrew and the mise installer put a link named `mise` on PATH. The link is
  // the mise executable, not a shim of a tool.
  it('does not take a link named mise for a shim', () => {
    const mise = file('mise', 0o755, join(directory, 'mise-bin'))
    const bin = join(directory, 'bin')
    mkdirSync(bin)
    symlinkSync(mise, join(bin, 'mise'))
    file('agent', 0o755, bin)
    expect(agentSearchPath(bin, () => ['/never'])).toBe(bin)
  })
})

describe.skipIf(process.platform === 'win32')('agentSearchPathEnv', () => {
  it('states the resolved path when it differs from the inherited one', () => {
    const install = join(directory, 'install')
    const shims = listingMise([install])
    expect(agentSearchPathEnv({ PATH: shims })).toEqual({ PATH: [install, shims].join(delimiter) })
  })

  // Leaving the variable out keeps the inherited spelling, `Path` on Windows.
  it('states nothing when the path needs no change', () => {
    expect(agentSearchPathEnv({ PATH: directory })).toEqual({})
    expect(agentSearchPathEnv({})).toEqual({})
  })
})

describe.skipIf(process.platform === 'win32')('unusableBinaryReason', () => {
  it('states that a link to the mise executable is a mise shim, and how to fix the path', () => {
    const shim = miseShim('agent', join(directory, 'shims'))
    const reason = unusableBinaryReason('agent', shim)
    expect(reason).toContain(`The agent on PATH (${shim}) is a mise shim`)
    expect(reason).toContain('`mise which agent`')
  })

  it('accepts a plain executable file', () => {
    expect(unusableBinaryReason('agent', file('agent', 0o755))).toBeNull()
  })

  // Many installs link the command to the real executable, such as npm's `.bin`.
  it('accepts a link to an executable that is not mise', () => {
    const target = file('agent.js', 0o755, join(directory, 'package'))
    const link = join(directory, 'agent')
    symlinkSync(target, link)
    expect(unusableBinaryReason('agent', link)).toBeNull()
  })

  it('accepts the mise executable itself', () => {
    expect(unusableBinaryReason('mise', file('mise', 0o755))).toBeNull()
  })

  it('accepts a path that does not resolve', () => {
    const link = join(directory, 'agent')
    symlinkSync(join(directory, 'absent'), link)
    expect(unusableBinaryReason('agent', link)).toBeNull()
  })
})

describe('missingBinaryReason', () => {
  // The skip check runs in the Playwright process with the developer's own HOME,
  // so it must find the CLI without running it. This one would leave a marker.
  it('finds the CLI without running it', () => {
    const marker = join(directory, 'ran')
    const cli = join(directory, 'agent')
    writeFileSync(cli, `#!/bin/sh\ntouch '${marker}'\n`)
    chmodSync(cli, 0o755)

    expect(missingBinaryReason('agent', 'needs agent', { PATH: directory })).toBeNull()
    expect(existsSync(marker)).toBe(false)
  })

  it('states the reason when the CLI is not on the path', () => {
    expect(missingBinaryReason('agent', 'needs agent', { PATH: directory })).toBe('needs agent')
    expect(missingBinaryReason('agent', 'needs agent', {})).toBe('needs agent')
  })

  // The run's isolated HOME stops a mise shim, so a shim that the spawn takes must
  // skip the specs rather than fail each agent start.
  it.skipIf(process.platform === 'win32')('states the reason and the fix when the CLI that the spawn takes is a mise shim', () => {
    const shims = join(directory, 'shims')
    const shim = miseShim('agent', shims)
    file('agent', 0o755, join(directory, 'install'))

    const reason = missingBinaryReason('agent', 'needs agent', { PATH: [shims, join(directory, 'install')].join(delimiter) })
    expect(reason).toBe(`needs agent. ${unusableBinaryReason('agent', shim)}`)
    expect(reason).toContain('mise shim')
  })

  // The run's own search path puts the install directory before the shims, so the
  // specs run in a shell that holds only the shims.
  it.skipIf(process.platform === 'win32')('accepts the CLI when mise lists its install directory', () => {
    const install = join(directory, 'install')
    const agent = file('agent', 0o755, install)
    const shims = listingMise([install])

    expect(missingBinaryReason('agent', 'needs agent', { PATH: shims })).toBeNull()
    expect(findBinary('agent', { PATH: shims })).toBe(agent)
  })

  it.skipIf(process.platform === 'win32')('accepts the CLI when its install directory comes before the shims', () => {
    const shims = join(directory, 'shims')
    miseShim('agent', shims)
    file('agent', 0o755, join(directory, 'install'))

    expect(missingBinaryReason('agent', 'needs agent', { PATH: [join(directory, 'install'), shims].join(delimiter) })).toBeNull()
  })
})

describe('lookupBinary', () => {
  it('returns the file that the run starts', () => {
    const agent = file('agent', 0o755)
    expect(lookupBinary('agent', 'needs agent', { PATH: directory })).toEqual({ path: agent, skipReason: null })
  })

  it('returns the reason when the path holds no such file', () => {
    expect(lookupBinary('agent', 'needs agent', { PATH: directory })).toEqual({ path: null, skipReason: 'needs agent' })
  })

  it.skipIf(process.platform === 'win32')('returns no path for a mise shim that mise does not list', () => {
    const shim = miseShim('agent', join(directory, 'shims'))
    const lookup = lookupBinary('agent', 'needs agent', { PATH: join(directory, 'shims') })
    expect(lookup.path).toBeNull()
    expect(lookup.skipReason).toBe(`needs agent. ${unusableBinaryReason('agent', shim)}`)
  })
})

// A script stands in for the CLI, so these cases run on Unix alone.
describe.skipIf(process.platform === 'win32')('versionOutput', () => {
  it('returns what the file prints on stdout for --version', () => {
    const cli = join(directory, 'agent')
    writeFileSync(cli, '#!/bin/sh\n[ "$1" = --version ] && echo "agent 1.2.3"\necho noise >&2\n')
    chmodSync(cli, 0o755)
    expect(versionOutput(cli)).toBe('agent 1.2.3\n')
  })

  it('returns null for a file that fails or does not exist', () => {
    const cli = join(directory, 'agent')
    writeFileSync(cli, '#!/bin/sh\nexit 3\n')
    chmodSync(cli, 0o755)
    expect(versionOutput(cli)).toBeNull()
    expect(versionOutput(join(directory, 'absent'))).toBeNull()
  })
})
