import { describe, expect, it } from 'vitest'
import { CODEX_STATUS } from './itemVocabulary'
import { isCodexFinishedStatus, parseCodexStatus } from './status'

describe('parseCodexStatus', () => {
  it('keeps every word Codex sends', () => {
    expect(parseCodexStatus(CODEX_STATUS.COMPLETED)).toBe(CODEX_STATUS.COMPLETED)
    expect(parseCodexStatus(CODEX_STATUS.FAILED)).toBe(CODEX_STATUS.FAILED)
    expect(parseCodexStatus(CODEX_STATUS.IN_PROGRESS)).toBe(CODEX_STATUS.IN_PROGRESS)
  })

  // The refusal used to fold into `inProgress`, so a command the reader denied kept
  // its spinner for the rest of the transcript.
  it('keeps a refused approval as its own word', () => {
    expect(parseCodexStatus(CODEX_STATUS.DECLINED)).toBe(CODEX_STATUS.DECLINED)
  })

  it('reads a word it does not know, and a non-string, as still running', () => {
    expect(parseCodexStatus('pending')).toBe(CODEX_STATUS.IN_PROGRESS)
    expect(parseCodexStatus(undefined)).toBe(CODEX_STATUS.IN_PROGRESS)
    expect(parseCodexStatus(null)).toBe(CODEX_STATUS.IN_PROGRESS)
    expect(parseCodexStatus(7)).toBe(CODEX_STATUS.IN_PROGRESS)
    expect(parseCodexStatus({ status: 'declined' })).toBe(CODEX_STATUS.IN_PROGRESS)
  })
})

describe('isCodexFinishedStatus', () => {
  it('reports every word that ends the item, however it ended', () => {
    expect(isCodexFinishedStatus(CODEX_STATUS.COMPLETED)).toBe(true)
    expect(isCodexFinishedStatus(CODEX_STATUS.FAILED)).toBe(true)
    expect(isCodexFinishedStatus(CODEX_STATUS.DECLINED)).toBe(true)
  })

  it('reports a running item and an absent status as unfinished', () => {
    expect(isCodexFinishedStatus(CODEX_STATUS.IN_PROGRESS)).toBe(false)
    expect(isCodexFinishedStatus('')).toBe(false)
    expect(isCodexFinishedStatus(null)).toBe(false)
    expect(isCodexFinishedStatus(undefined)).toBe(false)
  })
})
