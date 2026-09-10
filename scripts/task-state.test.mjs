import { chmodSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, utimesSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { checkState, forgetState, recordState } from './task-state.mjs'

const root = resolve(import.meta.dirname, '..')
const fixtures = []

function fixture() {
  mkdirSync(join(root, '.tmp'), { recursive: true })
  const dir = mkdtempSync(join(root, '.tmp', 'task-state-'))
  fixtures.push(dir)
  mkdirSync(join(dir, 'output'))
  writeFileSync(join(dir, 'output', 'one'), 'first')
  writeFileSync(join(dir, 'output', '.hidden'), 'second')
  return dir
}

const patterns = ['output/**/*']

afterEach(() => {
  for (const dir of fixtures.splice(0))
    rmSync(dir, { recursive: true, force: true })
})

describe('task output state', () => {
  it('accepts unchanged bytes and ignores timestamps', () => {
    const dir = fixture()
    expect(checkState(dir, 'build', patterns)).toBe(false)
    recordState(dir, 'build', patterns)
    utimesSync(join(dir, 'output', 'one'), new Date(0), new Date(0))
    expect(checkState(dir, 'build', patterns)).toBe(true)
  })

  it.each(['edit', 'delete', 'add', 'hidden'])('rejects an output change: %s', (change) => {
    const dir = fixture()
    recordState(dir, 'build', patterns)
    if (change === 'delete')
      rmSync(join(dir, 'output', 'one'))
    else
      writeFileSync(join(dir, 'output', change === 'hidden' ? '.hidden' : change === 'edit' ? 'one' : 'new'), 'other')
    expect(checkState(dir, 'build', patterns)).toBe(false)
  })

  it('rejects a missing output group even when another group exists', () => {
    const dir = fixture()
    expect(() => recordState(dir, 'build', [...patterns, 'absent/**/*'])).toThrow('No output matches absent/**/*')
  })

  it('separates tasks and rejects changed build options in either direction', () => {
    const dir = fixture()
    recordState(dir, 'build', patterns, 'production')
    expect(checkState(dir, 'build-other', patterns, 'production')).toBe(false)
    expect(checkState(dir, 'build', patterns, 'development')).toBe(false)
    recordState(dir, 'build', patterns, 'development')
    expect(checkState(dir, 'build', patterns, 'production')).toBe(false)
    expect(checkState(dir, 'build', patterns, 'development')).toBe(true)
  })

  it('forgets a previous success before an attempted build', () => {
    const dir = fixture()
    recordState(dir, 'build', patterns)
    forgetState(dir, 'build')
    expect(checkState(dir, 'build', patterns)).toBe(false)
    forgetState(dir, 'build')
  })

  it('ignores excluded cache files', () => {
    const dir = fixture()
    const selected = [...patterns, { exclude: 'output/.hidden' }]
    recordState(dir, 'build', selected)
    writeFileSync(join(dir, 'output', '.hidden'), 'cache')
    expect(checkState(dir, 'build', selected)).toBe(true)
  })

  it.skipIf(process.platform === 'win32')('detects executable permission and symlink changes', () => {
    const dir = fixture()
    recordState(dir, 'build', patterns)
    chmodSync(join(dir, 'output', 'one'), 0o755)
    expect(checkState(dir, 'build', patterns)).toBe(false)
    symlinkSync('one', join(dir, 'output', 'link'))
    recordState(dir, 'build', patterns)
    rmSync(join(dir, 'output', 'link'))
    symlinkSync('.hidden', join(dir, 'output', 'link'))
    expect(checkState(dir, 'build', patterns)).toBe(false)
  })

  it('rejects an output pattern that contains only empty directories', () => {
    const dir = fixture()
    mkdirSync(join(dir, 'empty', 'nested'), { recursive: true })
    expect(() => recordState(dir, 'build', ['empty/**/*'])).toThrow('No output matches')
  })
})
