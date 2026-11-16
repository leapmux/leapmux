import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { codeBuddyCapturedOutputFiles } from './outputObserver'

let root: string
beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  root = mkdtempSync(join(scratch, 'codebuddy-output-observer-'))
})
afterEach(() => rmSync(root, { recursive: true, force: true }))
function spool(session: string, filename: string, text: string): string {
  const directory = join(root, 'projects', 'native-project', session, 'tool-results')
  mkdirSync(directory, { recursive: true })
  const path = join(directory, filename)
  writeFileSync(path, text)
  return path
}

describe('codeBuddyCapturedOutputFiles', () => {
  it('waits for the selected session and exact captured output', () => {
    expect(codeBuddyCapturedOutputFiles(root, 'session-1', 'MARK42')).toEqual([])
    spool('another-session', 'call.txt', 'MARK42')
    spool('session-1', 'partial.txt', 'MARK')
    spool('session-1', 'not-output.json', 'MARK42')
    expect(codeBuddyCapturedOutputFiles(root, 'session-1', 'MARK42')).toEqual([])
    const path = spool('session-1', 'native-call-a1b2c3d4.txt', 'MARK42\n')
    expect(codeBuddyCapturedOutputFiles(root, 'session-1', 'MARK42')).toEqual([path])
  })

  it('retains ambiguity instead of choosing an unrelated captured result', () => {
    const first = spool('session-1', 'one.txt', 'MARK42')
    const second = spool('session-1', 'two.txt', 'MARK42')
    expect(codeBuddyCapturedOutputFiles(root, 'session-1', 'MARK42')).toEqual([first, second])
  })

  it.each(['', '../another', 'session/child'])('refuses an invalid native session: %s', (session) => {
    expect(() => codeBuddyCapturedOutputFiles(root, session, 'MARK42')).toThrow('requires a native session ID and output marker')
  })

  it.each(['', 'relative/config'])('refuses a configuration directory that is not absolute: %s', (configDir) => {
    expect(() => codeBuddyCapturedOutputFiles(configDir, 'session-1', 'MARK42')).toThrow('requires an absolute configuration directory')
  })

  it('refuses an empty output marker', () => {
    expect(() => codeBuddyCapturedOutputFiles(root, 'session-1', '')).toThrow('requires a native session ID and output marker')
  })
})
