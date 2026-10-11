import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve, sep } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { isInsideDirectory, RUN_ROOT_PREFIX } from './runRoot'

let scratch: string

beforeEach(() => {
  const parent = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(parent, { recursive: true })
  scratch = mkdtempSync(join(parent, 'run-root-test-'))
})

afterEach(() => rmSync(scratch, { recursive: true, force: true }))

describe('isInsideDirectory', () => {
  it('holds the directory itself and each path below it', () => {
    const root = join(scratch, 'root')
    expect(isInsideDirectory(root, root)).toBe(true)
    expect(isInsideDirectory(join(root, 'AGENTS.md'), root)).toBe(true)
    expect(isInsideDirectory(join(root, '1', 'work', '.cursor', 'rules', 'a.mdc'), root)).toBe(true)
  })

  it('holds no path above the directory, and no sibling that shares the start of its name', () => {
    const root = join(scratch, 'root')
    expect(isInsideDirectory(scratch, root)).toBe(false)
    expect(isInsideDirectory(join(scratch, 'AGENTS.md'), root)).toBe(false)
    expect(isInsideDirectory(`${root}-other${sep}AGENTS.md`, root)).toBe(false)
    expect(isInsideDirectory('/AGENTS.md', root)).toBe(false)
  })

  it('reads a path that climbs out through `..` by where it ends', () => {
    const root = join(scratch, 'root')
    expect(isInsideDirectory(`${root}${sep}work${sep}..${sep}..${sep}AGENTS.md`, root)).toBe(false)
    expect(isInsideDirectory(`${root}${sep}work${sep}..${sep}AGENTS.md`, root)).toBe(true)
  })

  it('holds a file whose name starts with two dots', () => {
    const root = join(scratch, 'root')
    expect(isInsideDirectory(join(root, '..hidden'), root)).toBe(true)
  })

  it('holds no relative path and no empty path', () => {
    expect(isInsideDirectory('AGENTS.md', scratch)).toBe(false)
    expect(isInsideDirectory('', scratch)).toBe(false)
  })

  it('places a path through a symbolic link by its real path, both ways', () => {
    const root = join(scratch, 'root')
    mkdirSync(join(root, 'work'), { recursive: true })
    const link = join(scratch, 'link')
    symlinkSync(root, link, 'junction')
    // The agent states the file through the link, and the launcher states the real path of the run root.
    expect(isInsideDirectory(join(link, 'work'), root)).toBe(true)
    // The agent states the real path, and the run root is stated through the link.
    expect(isInsideDirectory(join(root, 'work'), link)).toBe(true)
    // A link outside the run root to a place outside it stays outside.
    expect(isInsideDirectory(join(link, 'work'), join(scratch, 'elsewhere'))).toBe(false)
  })

  it('refuses an existing file through a link inside the run that points outside it', () => {
    const root = join(scratch, 'root')
    const outside = join(scratch, 'outside')
    mkdirSync(root)
    mkdirSync(outside)
    writeFileSync(join(outside, 'AGENTS.md'), 'Outside instructions.')
    symlinkSync(outside, join(root, 'link'), 'junction')

    expect(isInsideDirectory(join(root, 'link', 'AGENTS.md'), root)).toBe(false)
  })

  it('places a missing file through its nearest existing ancestor', () => {
    const root = join(scratch, 'root')
    const outside = join(scratch, 'outside')
    mkdirSync(root)
    mkdirSync(outside)
    symlinkSync(outside, join(root, 'outside-link'), 'junction')
    symlinkSync(root, join(outside, 'inside-link'), 'junction')

    expect(isInsideDirectory(join(root, 'outside-link', 'missing', 'AGENTS.md'), root)).toBe(false)
    expect(isInsideDirectory(join(outside, 'inside-link', 'missing', 'AGENTS.md'), root)).toBe(true)
  })

  it('resolves parent segments after following symbolic links as the file system does', () => {
    const root = join(scratch, 'root')
    const outside = join(scratch, 'outside')
    mkdirSync(join(root, 'child'), { recursive: true })
    mkdirSync(join(outside, 'child'), { recursive: true })
    writeFileSync(join(root, 'AGENTS.md'), 'Inside')
    writeFileSync(join(outside, 'AGENTS.md'), 'Outside')
    symlinkSync(join(outside, 'child'), join(root, 'outside-link'), 'junction')
    symlinkSync(join(root, 'child'), join(outside, 'inside-link'), 'junction')

    for (const link of [join(root, 'outside-link'), join(outside, 'inside-link')]) {
      const path = `${link}${sep}..${sep}AGENTS.md`
      expect(isInsideDirectory(path, root)).toBe(readFileSync(path, 'utf8') === 'Inside')
    }
  })

  it('refuses a broken link and a loop of links', () => {
    const root = join(scratch, 'root')
    mkdirSync(root)
    symlinkSync(join(scratch, 'missing'), join(root, 'broken'), 'junction')
    symlinkSync(join(root, 'loop'), join(root, 'loop'), 'junction')

    expect(isInsideDirectory(join(root, 'broken', 'AGENTS.md'), root)).toBe(false)
    expect(isInsideDirectory(join(root, 'loop', 'AGENTS.md'), root)).toBe(false)
  })

  it('refuses a relative directory and paths that contain a NUL', () => {
    expect(isInsideDirectory(join(scratch, 'AGENTS.md'), '.')).toBe(false)
    expect(isInsideDirectory(`${scratch}\0/AGENTS.md`, scratch)).toBe(false)
    expect(isInsideDirectory(join(scratch, 'AGENTS.md'), `${scratch}\0`)).toBe(false)
  })
})

describe('RUN_ROOT_PREFIX', () => {
  // The Amp environment matches each run root by this prefix in a glob (./ampEnvironment.ts).
  it('is one name segment with no glob character', () => {
    expect(RUN_ROOT_PREFIX).toMatch(/^[\w-]+$/)
  })
})
