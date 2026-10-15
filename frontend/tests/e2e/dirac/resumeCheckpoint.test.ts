import { describe, expect, it } from 'vitest'
import { hasDiracResumeCheckpoint, waitForDiracCheckpoint } from './resumeCheckpoint'

describe('hasDiracResumeCheckpoint', () => {
  const runtime = { taskId: 'current-task' }
  const row = { id: 'current-task', ulid: 'session' }

  it('requires the current task and session while allowing older tasks', () => {
    expect(hasDiracResumeCheckpoint(runtime, [{ id: 'old-task', ulid: 'session' }, row], 'session')).toBe(true)
  })

  it('rejects a task ID that appears under another session also', () => {
    expect(hasDiracResumeCheckpoint(runtime, [{ ...row, ulid: 'another-session' }, row], 'session')).toBe(false)
    expect(hasDiracResumeCheckpoint(runtime, [row, { ...row, ulid: 'another-session' }], 'session')).toBe(false)
  })

  it.each([null, {}, { taskId: '' }, { taskId: ' ' }, { taskId: 0 }, { taskId: -1 }])('rejects an invalid runtime %j', (value) => {
    expect(hasDiracResumeCheckpoint(value, [row], 'session')).toBe(false)
  })

  it.each([null, {}, [], [null], [row, row], [{ ...row, id: 'another' }], [{ ...row, ulid: 'another' }]])('rejects an absent or ambiguous checkpoint %j', (history) => {
    expect(hasDiracResumeCheckpoint(runtime, history, 'session')).toBe(false)
  })

  it.each(['', ' '])('rejects an empty session ID %j', (sessionId) => {
    expect(hasDiracResumeCheckpoint(runtime, [{ ...row, ulid: sessionId }], sessionId)).toBe(false)
  })
})

describe('waitForDiracCheckpoint', () => {
  it.each([
    ['', 'session'],
    ['relative/profile', 'session'],
    ['/private/profile', ''],
    ['/private/profile', '..'],
    ['/private/profile', '../other'],
    ['/private/profile', 'nested/session'],
    ['/private/profile', 'nested\\session'],
    ['/private/profile', 'session\u0000'],
  ])('rejects an unsafe profile or session before reading the filesystem: %j, %j', async (profile, session) => {
    await expect(waitForDiracCheckpoint(profile, session)).rejects.toThrow('The Dirac checkpoint requires an absolute profile directory and a safe session ID.')
  })
})
