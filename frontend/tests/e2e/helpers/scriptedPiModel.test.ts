import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { withMockPiModel } from './scriptedPiModel'

let directory: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'pi-model-test-'))
})

afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('withMockPiModel', () => {
  it.each([
    'https://127.0.0.1:1234',
    'http://example.com:1234',
    'http://127.0.0.1:1234/prefix',
  ])('refuses a non-local mock server before it writes a Pi extension: %s', async (url) => {
    await expect(withMockPiModel(directory, url, async () => {})).rejects.toThrow('loopback HTTP origin')
    expect(existsSync(join(directory, '.pi'))).toBe(false)
  })
})
