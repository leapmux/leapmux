import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { chatScrollContainer, messageBubbles, visibleOnly } from './ui'

export interface TranscriptGuard { allow: (reason: string) => void }
export interface TranscriptGuardState extends TranscriptGuard { readonly allowedReason: string | undefined }

/** Create one explicit exception state for one test. */
export function createTranscriptGuard(): TranscriptGuardState {
  let allowedReason: string | undefined
  return {
    allow(reason) {
      const text = reason.trim()
      if (!text)
        throw new Error('A transcript guard exception needs a reason.')
      allowedReason = text
    },
    get allowedReason() { return allowedReason },
  }
}

/** Recognize a whole JSON frame without treating ordinary JSON data as a native frame. */
export function isRawFrameText(text: string): boolean {
  const trimmed = text.trim()
  if (!trimmed.startsWith('{') || !trimmed.endsWith('}'))
    return false
  try {
    const value: unknown = JSON.parse(trimmed)
    if (value === null || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).length < 2)
      return false
    const fields = value as Record<string, unknown>
    return ['type', 'method', 'event', 'subtype', 'sessionUpdate', 'jsonrpc'].some(key => typeof fields[key] === 'string')
  }
  catch {
    return false
  }
}

/** Read raw frames only from visible notification and result-divider content. */
export async function rawFrameRowTexts(page: Page): Promise<string[]> {
  const bubbles = messageBubbles(page).and(page.locator('[data-category="notification"], [data-category="result_divider"]'))
  const texts = await visibleOnly(bubbles.getByTestId('message-content')).allTextContents()
  return texts.filter(isRawFrameText)
}

/** Locate stored loading dividers. The live compaction indicator has a separate test ID. */
export function storedCompactionSpinners(page: Page): Locator {
  return chatScrollContainer(page).locator('[data-testid="notification-divider"][data-loading="true"]:visible')
}

export async function expectCleanTranscript(page: Page): Promise<void> {
  await expect(visibleOnly(page.getByTestId('unrendered-row'))).toHaveCount(0)
  await expect.poll(() => rawFrameRowTexts(page), 'the transcript contains no raw native frame').toEqual([])
  await expect(storedCompactionSpinners(page)).toHaveCount(0)
}

/** Check the transcript only after the expected test outcome and without an explicit exception. */
export async function runTranscriptGuard(page: Page, guard: TranscriptGuardState, testInfo: { status?: string, expectedStatus?: string }): Promise<void> {
  if (testInfo.status !== testInfo.expectedStatus || guard.allowedReason !== undefined)
    return
  await expectCleanTranscript(page)
}
