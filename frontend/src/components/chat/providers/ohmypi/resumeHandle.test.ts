import { describe, expect, it } from 'vitest'
import { ohMyPiValidateResumeHandle } from './resumeHandle'

describe('ohMyPiValidateResumeHandle', () => {
  it('accepts no handle', () => {
    expect(ohMyPiValidateResumeHandle('')).toBeNull()
  })

  it('accepts the session file the worker stores', () => {
    expect(ohMyPiValidateResumeHandle('/Users/u/.omp/agent/sessions/-p/2026-09-23T18-11-57-284Z_01a0cf77-9ae4-72d8-9a42-665c431d3beb.jsonl')).toBeNull()
  })

  it('accepts a session id, which omp resolves too', () => {
    expect(ohMyPiValidateResumeHandle('01a0cf77-9ae4-72d8-9a42-665c431d3beb')).toBeNull()
  })

  it('refuses a relative path and an id with a control character', () => {
    expect(ohMyPiValidateResumeHandle('sessions/a.jsonl')).toBe('Session file path must be absolute')
    expect(ohMyPiValidateResumeHandle('a.jsonl')).toBe('Session file path must be absolute')
    expect(ohMyPiValidateResumeHandle('id\u0007bell')).toBe('Session ID contains invalid characters')
  })
})
