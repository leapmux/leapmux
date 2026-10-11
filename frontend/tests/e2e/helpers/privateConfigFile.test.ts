import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { writePrivateJSON } from './privateConfigFile'

let directory: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'private-config-file-test-'))
})

afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('writePrivateJSON', () => {
  it('writes indented JSON with a final newline', () => {
    const path = join(directory, 'settings.json')
    writePrivateJSON(path, { model: 'mock', nested: { enabled: false } })
    expect(readFileSync(path, 'utf8')).toBe('{\n  "model": "mock",\n  "nested": {\n    "enabled": false\n  }\n}\n')
  })

  it.runIf(process.platform !== 'win32')('makes the file readable by its owner alone', () => {
    const path = join(directory, 'settings.json')
    writePrivateJSON(path, {})
    expect(statSync(path).mode & 0o777).toBe(0o600)
  })
})
