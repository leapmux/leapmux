import { describe, expect, it } from 'vitest'
import { isClientEntryChunk } from '~/lib/clientEntryChunk'

describe('isClientEntryChunk', () => {
  it('matches the chunk that Rolldown names after src/entry-client.tsx', () => {
    expect(isClientEntryChunk('entry-client-CXyYpMh4.js')).toBe(true)
  })

  it('matches a hash that holds the dash and underscore of the hash alphabet', () => {
    expect(isClientEntryChunk('entry-client-CUf_sYX-.js')).toBe(true)
  })

  it('rejects the stylesheet that shares the entry name', () => {
    expect(isClientEntryChunk('entry-client-fkxBCGLW.css')).toBe(false)
  })

  it('rejects a chunk whose name only contains the entry name', () => {
    expect(isClientEntryChunk('myentry-client-CXyYpMh4.js')).toBe(false)
    expect(isClientEntryChunk('entry-client-CXyYpMh4.js.map')).toBe(false)
  })

  it('rejects the unbuilt entry module and an entry chunk with no hash', () => {
    expect(isClientEntryChunk('entry-client.tsx')).toBe(false)
    expect(isClientEntryChunk('entry-client.js')).toBe(false)
  })

  it('rejects the vinxi client chunk name and an empty name', () => {
    expect(isClientEntryChunk('client-CX-js57o.js')).toBe(false)
    expect(isClientEntryChunk('')).toBe(false)
  })
})
