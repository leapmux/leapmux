import type { Page, TestInfo } from '@playwright/test'
import type { RecordedToast } from './toast'
import { describe, expect, it, vi } from 'vitest'
import { attachToastLog, dangerToasts, expectToastRecorded, formatToastLog } from './toast'

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

describe('expectToastRecorded', () => {
  /** A page whose recorder holds no toast on the first read and `later` from the second read on. */
  function laterRecordingPage(later: RecordedToast[]) {
    const evaluate = vi.fn<() => Promise<RecordedToast[]>>().mockResolvedValueOnce([]).mockResolvedValue(later)
    return { page: { evaluate } as unknown as Page, evaluate }
  }

  it('waits for a recorded toast whose message contains the text', async () => {
    const { page, evaluate } = laterRecordingPage([toast('Saved', 'success'), toast('The worker is offline. Try again.', 'danger')])
    await expectToastRecorded(page, 'worker is offline')
    expect(evaluate.mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  it('matches a regular expression against the message', async () => {
    const { page } = laterRecordingPage([toast('Could not copy: permission denied', 'danger')])
    await expectToastRecorded(page, /^Could not copy/)
  })

  it('refuses an empty text before it reads the page', async () => {
    const { page, evaluate } = laterRecordingPage([])
    await expect(expectToastRecorded(page, '')).rejects.toThrow('needs text')
    expect(evaluate).not.toHaveBeenCalled()
  })
})

describe('dangerToasts', () => {
  it('returns only the toasts of the danger variant, in record order', async () => {
    const recorded = [toast('Saved', 'success'), toast('Offline', 'danger'), toast('Copied', ''), toast('Failed', 'danger', 10)]
    expect(await dangerToasts(recordingPage(recorded))).toEqual([recorded[1], recorded[3]])
  })

  it('returns an empty list for a page with no error toast', async () => {
    expect(await dangerToasts(recordingPage([toast('Saved', 'success')]))).toEqual([])
  })
})
