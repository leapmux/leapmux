import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { crateIndexResponse, git, gitRepository, isCrateIndexRead, scratchDirectories, writeFiles } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)

describe('scratchDirectories', () => {
  it('creates distinct directories under .tmp, and the cleanup removes every one of them', () => {
    const own = scratchDirectories()
    const first = own.create('test-support')
    const second = own.create('test-support')
    expect(first).not.toBe(second)
    expect(first).toContain(join('.tmp', 'test-support-'))
    expect([first, second].every(existsSync)).toBe(true)
    own.cleanup()
    expect([first, second].some(existsSync)).toBe(false)
    // A second cleanup has nothing left to remove.
    expect(() => own.cleanup()).not.toThrow()
  })
})

describe('writeFiles', () => {
  it('writes nested files and removes a file whose content is null', () => {
    const directory = scratch.create('test-support-files')
    writeFiles(directory, { 'a/b/c.txt': 'text', 'gone.txt': 'old' })
    writeFiles(directory, { 'gone.txt': null, 'absent.txt': null })
    expect(readFileSync(join(directory, 'a/b/c.txt'), 'utf8')).toBe('text')
    expect(existsSync(join(directory, 'gone.txt'))).toBe(false)
  })
})

describe('gitRepository', () => {
  it('commits the files on main and pins the configuration that keeps git from outliving the test', () => {
    const directory = scratch.create('test-support-git')
    const head = gitRepository(directory, {
      files: { 'a.txt': 'a' },
      config: { 'core.autocrlf': 'false' },
    })
    expect(head).toMatch(/^[0-9a-f]{40}$/)
    expect(git(directory, ['rev-parse', '--abbrev-ref', 'HEAD'])).toBe('main')
    expect(git(directory, ['show', 'HEAD:a.txt'])).toBe('a')
    for (const [key, value] of [
      ['core.fsmonitor', 'false'],
      ['gc.auto', '0'],
      ['maintenance.auto', 'false'],
      ['commit.gpgsign', 'false'],
      ['core.autocrlf', 'false'],
    ])
      expect(git(directory, ['config', '--local', key]), key).toBe(value)
    expect(git(directory, ['config', '--local', 'core.hooksPath'])).toBe(join(directory, '.git', 'no-hooks'))
  })
  it('creates a bare repository without a commit', () => {
    const directory = scratch.create('test-support-bare')
    expect(gitRepository(directory, { bare: true })).toBeNull()
    expect(git(directory, ['rev-parse', '--is-bare-repository'])).toBe('true')
    expect(git(directory, ['config', '--local', 'core.fsmonitor'])).toBe('false')
  })
  it('reports the standard error of a failed git command', () => {
    const directory = scratch.create('test-support-error')
    gitRepository(directory)
    expect(() => git(directory, ['rev-parse', '--verify', 'no-such-ref'])).toThrow('git rev-parse --verify no-such-ref failed')
  })
})

describe('isCrateIndexRead', () => {
  it('matches a crate file of the crates.io sparse index, and nothing else', () => {
    expect(isCrateIndexRead('https://index.crates.io/3/f/foo')).toBe(true)
    for (const url of [
      'https://index.crates.io',
      'https://index.crates.io.evil/3/f/foo',
      'https://crates.io/api/v1/crates/foo',
      'https://registry.npmjs.org/foo',
    ])
      expect(isCrateIndexRead(url), url).toBe(false)
  })
})

describe('crateIndexResponse', () => {
  it('answers with one JSON line for each entry', async () => {
    const response = crateIndexResponse([
      { vers: '1.0.0', pubtime: '2026-10-20T00:00:00Z' },
      { vers: '1.1.0', yanked: true },
    ])
    expect((await response.text()).split('\n').map(line => JSON.parse(line))).toEqual([
      { vers: '1.0.0', pubtime: '2026-10-20T00:00:00Z' },
      { vers: '1.1.0', yanked: true },
    ])
    expect(await crateIndexResponse([]).text()).toBe('')
  })
})
