import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { writeToolImage } from './toolImages'

let workingDir: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  workingDir = mkdtempSync(join(scratch, 'tool-images-'))
})

afterEach(() => rmSync(workingDir, { recursive: true, force: true }))

describe('writeToolImage', () => {
  it('writes a PNG under the working directory, named from the marker', () => {
    const name = writeToolImage(workingDir, 'prov-42')
    expect(name).toBe('tool-image-prov-42.png')
    expect(existsSync(join(workingDir, name))).toBe(true)
  })

  it('writes a decodable PNG with the standard signature', () => {
    const bytes = readFileSync(join(workingDir, writeToolImage(workingDir, 'sig')))
    expect([...bytes.subarray(0, 8)]).toEqual([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A])
    expect(bytes.length).toBeGreaterThan(8)
  })

  // The marker is what a tool row shows. A name that carries it can only come
  // from a real read of this file, not from the prompt or the scripted reply.
  it('keeps the marker in the file name so a tool row can name it', () => {
    expect(writeToolImage(workingDir, 'kimi-77')).toBe('tool-image-kimi-77.png')
    expect(writeToolImage(workingDir, 'codex-77')).toBe('tool-image-codex-77.png')
  })
})
