import type { Page, TestInfo } from '@playwright/test'
import type { RecordedToast } from './toast'
import { describe, expect, it, vi } from 'vitest'
import { attachToastLog, formatToastLog } from './toast'

const STARTED = Date.UTC(2026, 9, 6, 12, 0, 0)

function toast(message: string, variant: string, offsetMs = 0): RecordedToast {
  return { message, variant, timestamp: STARTED + offsetMs }
}

/** A page whose recorded toast list is `toasts`, or whose read fails with `toasts` when it is an error. */
function recordingPage(toasts: RecordedToast[] | Error): Page {
  return { evaluate: vi.fn(async () => {
    if (toasts instanceof Error)
      throw toasts
    return toasts
  }) } as unknown as Page
}

function reportingTestInfo() {
  const attach = vi.fn<TestInfo['attach']>(async () => {})
  return { testInfo: { attach } as unknown as TestInfo, attach }
}

describe('formatToastLog', () => {
  it('writes one line for each toast with its time, its variant, and its message', () => {
    expect(formatToastLog([toast('Saved', 'success'), toast('Could not reach the Worker', 'danger', 1500)])).toBe(
      '[2026-10-06T12:00:00.000Z] [success] Saved\n[2026-10-06T12:00:01.500Z] [danger] Could not reach the Worker',
    )
  })

  it('reads a toast with no variant as info', () => {
    expect(formatToastLog([toast('Copied', '')])).toBe('[2026-10-06T12:00:00.000Z] [info] Copied')
  })

  it('returns an empty text for no toast', () => {
    expect(formatToastLog([])).toBe('')
  })
})

describe('attachToastLog', () => {
  it('attaches the formatted toasts as toast-log', async () => {
    const toasts = [toast('Saved', 'success')]
    const { testInfo, attach } = reportingTestInfo()
    await attachToastLog(recordingPage(toasts), testInfo)
    expect(attach).toHaveBeenCalledExactlyOnceWith('toast-log', { body: formatToastLog(toasts), contentType: 'text/plain' })
  })

  it('attaches nothing when the page recorded no toast', async () => {
    const { testInfo, attach } = reportingTestInfo()
    await attachToastLog(recordingPage([]), testInfo)
    expect(attach).not.toHaveBeenCalled()
  })

  it('attaches nothing, and does not throw, when the page cannot answer', async () => {
    const { testInfo, attach } = reportingTestInfo()
    await expect(attachToastLog(recordingPage(new Error('Target page, context or browser has been closed')), testInfo)).resolves.toBeUndefined()
    expect(attach).not.toHaveBeenCalled()
  })
})
