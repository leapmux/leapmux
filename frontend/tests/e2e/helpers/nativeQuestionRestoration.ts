import type { Locator } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { accountStorageKey, PREFIX_CONTROL_STATE } from '../../../src/lib/browserStorage'
import { isObject } from '../../../src/lib/jsonPick'
import { getUserId } from './api'
import { waitForStoredEntry } from './storage'
import { composerEditor, controlAgentId, controlBanner, waitForControlBanner, waitForEditorDraft } from './ui'

/** Reject an invalid page before any browser or storage operation. */
function validateQuestionPageIndex(pageIndex: number | undefined): void {
  if (pageIndex !== undefined && (!Number.isSafeInteger(pageIndex) || pageIndex < 0))
    throw new Error('A question page index requires a nonnegative safe integer.')
}

/** Reload only after durable storage contains the answer, then verify its original owner and value. */
async function restoreQuestionAfterStoredState(
  context: ManagedNativeScenarioContext,
  waitForState: (identity: { agentId: string, userId: string }) => Promise<void>,
  assertRestored: (banner: Locator) => Promise<void>,
): Promise<void> {
  const agentId = await controlAgentId(controlBanner(context.page))
  const server = context.leapmuxServer
  const userId = server.adminUserId ?? await getUserId(server.hubUrl, server.adminToken)
  await waitForState({ agentId, userId })
  await context.page.reload()
  const restored = await waitForControlBanner(context.page)
  expect(await controlAgentId(restored), 'the restored question belongs to the original agent').toBe(agentId)
  await assertRestored(restored)
}

/** Require the selected native value in durable storage and in the restored question. */
export async function restoreQuestionSelection(context: ManagedNativeScenarioContext, banner: Locator, label: string, pageIndex?: number): Promise<void> {
  validateQuestionPageIndex(pageIndex)
  const selected = banner.getByTestId(`question-option-${label}`).locator('input')
  await expect(selected).toBeChecked()
  const value = await selected.inputValue()
  if (!value)
    throw new Error('The selected question option has no native value.')
  await restoreQuestionAfterStoredState(context, async ({ agentId, userId }) => {
    await waitForStoredEntry(
      context.page,
      accountStorageKey(userId, `${PREFIX_CONTROL_STATE}${agentId}:`),
      stored => isObject(stored) && isObject(stored.selections)
        && (pageIndex === undefined
          ? Object.values(stored.selections).some(values => Array.isArray(values) && values.includes(value))
          : stored.currentPage === pageIndex && Array.isArray(stored.selections[pageIndex]) && stored.selections[pageIndex].includes(value)),
      'the selected answer reaches durable storage before reload',
    )
  }, async (restored) => {
    await expect(restored.getByTestId(`question-option-${label}`).locator('input')).toBeChecked()
  })
}

/** Restore a typed answer only after its draft and optional page reach durable account storage. */
export async function restoreQuestionTextDraft(context: ManagedNativeScenarioContext, text: string, pageIndex?: number): Promise<void> {
  validateQuestionPageIndex(pageIndex)
  if (!text.trim())
    throw new Error('A question draft proof requires nonempty text.')
  await expect(composerEditor(context.page)).toHaveText(text)
  await restoreQuestionAfterStoredState(context, async ({ agentId, userId }) => {
    await waitForEditorDraft(context.page, userId, text)
    if (pageIndex !== undefined) {
      await waitForStoredEntry(
        context.page,
        accountStorageKey(userId, `${PREFIX_CONTROL_STATE}${agentId}:`),
        stored => isObject(stored) && stored.currentPage === pageIndex,
        'the question page reaches durable storage before reload',
      )
    }
  }, async () => {
    await expect(composerEditor(context.page)).toHaveText(text)
  })
}
