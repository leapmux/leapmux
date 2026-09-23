import { appendFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { markSuiteServerLog, readSuiteServerLog } from './suiteServerLog'

let directory: string
let path: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'suite-server-log-test-'))
  path = join(directory, 'server.log')
  writeFileSync(path, '')
})

afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('suite server log', () => {
  it('returns only bytes written after a mark', () => {
    appendFileSync(path, 'earlier\n')
    const mark = markSuiteServerLog(path)
    appendFileSync(path, 'later\n')

    expect(readSuiteServerLog(path, mark)).toBe('later\n')
    expect(readSuiteServerLog(path, 0)).toBe('earlier\nlater\n')
  })

  it('uses byte offsets for multibyte output', () => {
    appendFileSync(path, '한글\n')
    const mark = markSuiteServerLog(path)
    appendFileSync(path, 'after\n')

    expect(readSuiteServerLog(path, mark)).toBe('after\n')
  })

  it('rejects an invalid mark', () => {
    expect(() => readSuiteServerLog(path, -1)).toThrow(RangeError)
    expect(() => readSuiteServerLog(path, 0.5)).toThrow(RangeError)
  })
})
