import { chmodSync, existsSync, mkdirSync, mkdtempSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { delimiter, dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { agentSearchPath, agentSearchPathEnv, exeExtensionOnPath, findBinary, findBinaryOnPath, requireBinary, unusableBinaryReason } from './binaryOnPath'

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
    expect(findBinaryOnPath('agent', [
      '',
      join(directory, 'absent'),
      directory,
    ].join(delimiter))).toBe(agent)
  })

  // A spawn takes the first match, so a later match is not the file that runs.
  it('returns the match of the first directory that holds one', () => {
    const first = file('agent', 0o755, join(directory, 'first'))
    file('agent', 0o755, join(directory, 'second'))
    expect(findBinaryOnPath('agent', [
      join(directory, 'first'),
      join(directory, 'second'),
    ].join(delimiter))).toBe(first)
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

describe('exeExtensionOnPath', () => {
  it('takes the .exe spelling that PATHEXT states, whatever its case', () => {
    expect(exeExtensionOnPath('.COM;.EXE;.BAT')).toBe('.EXE')
    expect(exeExtensionOnPath('.com;.exe')).toBe('.exe')
  })

  it('answers the plain .exe extension when PATHEXT states none', () => {
    expect(exeExtensionOnPath('.COM;.BAT')).toBe('.exe')
    expect(exeExtensionOnPath('')).toBe('.exe')
  })
})

// A link needs a privilege on Windows, and mise installs a Windows shim differently.
describe.skipIf(process.platform === 'win32')('agentSearchPath', () => {
  it('keeps a path that holds no shim directory, and asks mise nothing', () => {
    file('agent', 0o755)
    const searchPath = [
      directory,
      join(directory, 'absent'),
    ].join(delimiter)
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

    expect(resolved).toBe([
      before,
      '/install/a',
      '/install/b',
      shims,
      after,
    ].join(delimiter))
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
    const shims = listingMise([
      install,
      join(directory, 'second'),
    ])
    expect(agentSearchPath(shims)).toBe([
      install,
      join(directory, 'second'),
      shims,
    ].join(delimiter))
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
    expect(agentSearchPathEnv({ PATH: shims })).toEqual({
      PATH: [install, shims].join(delimiter),
    })
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

describe('requireBinary', () => {
  it('finds an executable without running it', () => {
    const marker = join(directory, 'ran')
    const cli = file('agent', 0o755)
    writeFileSync(cli, `#!/bin/sh\ntouch '${marker}'\n`)
    expect(requireBinary('agent', 'needs agent', { PATH: directory })).toBe(cli)
    expect(existsSync(marker)).toBe(false)
  })

  it('fails when no executable exists', () => {
    expect(() => requireBinary('agent', 'needs agent', { PATH: directory })).toThrow('needs agent')
    expect(() => requireBinary('agent', 'needs agent', {})).toThrow('needs agent')
    expect(() => requireBinary(['agent', 'other'], 'needs agent', { PATH: directory })).toThrow('needs agent')
  })

  it('refuses an empty list of executable names', () => {
    expect(() => requireBinary([], 'needs agent', { PATH: directory })).toThrow('at least one executable name')
  })

  it('resolves the next alias when the first alias is absent', () => {
    const cli = file('other', 0o755)
    expect(requireBinary(['agent', 'other'], 'needs agent', { PATH: directory })).toBe(cli)
  })

  it('prefers alias order over directory order', () => {
    file('other', 0o755, join(directory, 'first'))
    const cli = file('agent', 0o755, join(directory, 'second'))
    expect(requireBinary(['agent', 'other'], 'needs agent', {
      PATH: [
        join(directory, 'first'),
        join(directory, 'second'),
      ].join(delimiter),
    })).toBe(cli)
  })

  it.skipIf(process.platform === 'win32')('rejects the first alias when it resolves to an unusable mise shim', () => {
    const shims = join(directory, 'shims')
    const shim = miseShim('agent', shims)
    file('other', 0o755, join(directory, 'install'))
    for (const reason of [
      'Needs agent',
      'Needs agent.',
      'Needs agent!',
      'Needs agent?',
    ]) {
      expect(() => requireBinary(['agent', 'other'], reason, {
        PATH: [
          shims,
          join(directory, 'install'),
        ].join(delimiter),
      })).toThrow(`${/[.!?]$/.test(reason)
        ? reason
        : `${reason}.`} ${unusableBinaryReason('agent', shim)}`)
    }
  })

  it.skipIf(process.platform === 'win32')('uses the installation directory that mise lists', () => {
    const install = join(directory, 'install')
    const cli = file('agent', 0o755, install)
    const shims = listingMise([install])
    expect(requireBinary('agent', 'needs agent', { PATH: shims })).toBe(cli)
  })

  it.skipIf(process.platform === 'win32')('accepts an installation before a shim', () => {
    const shims = join(directory, 'shims')
    miseShim('agent', shims)
    const install = join(directory, 'install')
    const cli = file('agent', 0o755, install)
    expect(requireBinary('agent', 'needs agent', {
      PATH: [install, shims].join(delimiter),
    })).toBe(cli)
  })
})
