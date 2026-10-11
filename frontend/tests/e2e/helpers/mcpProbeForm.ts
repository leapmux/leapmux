import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { elicitationFieldKey } from '../../../src/components/chat/controls/elicitationForm'
import { accountStorageKey, PREFIX_CONTROL_STATE } from '../../../src/lib/browserStorage'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { MCP_FORM_SERVER_NAME } from './mcpFormServer'
import { nativeTextStep, nativeToolOutcome } from './nativeScenario'
import { mcpToolCall } from './providerToolCalls'
import { waitForStoredEntry } from './storage'
import { controlActions, messageBubbles, sendMessage, waitForAgentIdle } from './ui'

/** Fill the disposable MCP server's form with values that expose false and zero. */
export async function fillMcpProbeForm(page: Page): Promise<Locator> {
  const form = page.getByTestId('elicitation-form').filter({ visible: true })
  await expect(form).toBeVisible()
  await form.getByLabel('Count *').fill('0')
  await form.getByRole('button', { name: 'Enabled *', exact: true }).click()
  await page.getByRole('menuitemradio', { name: 'No', exact: true }).click()
  await form.getByRole('button', { name: 'Color *', exact: true }).click()
  await page.getByRole('menuitemradio', { name: 'Blue', exact: true }).click()
  return form
}

/**
 * The answers that `fillMcpProbeForm` gives, as the saved control answer stores them.
 * Each value is the JSON text of the field value: the count 0, the boolean false, and the color constant "b" (Blue).
 */
const MCP_PROBE_FORM_CHOICES: Readonly<Record<string, string>> = {
  [elicitationFieldKey('count')]: '0',
  [elicitationFieldKey('enabled')]: 'false',
  [elicitationFieldKey('color')]: '"b"',
}

/** Whether a saved control answer holds every answer that `fillMcpProbeForm` gives. */
export function mcpProbeFormDraftSaved(record: unknown): boolean {
  const choices = isObject(record) ? pickObject(record, 'choices') : null
  return choices !== null && Object.entries(MCP_PROBE_FORM_CHOICES).every(([key, value]) => choices[key] === value)
}

/**
 * Wait until the answers of `fillMcpProbeForm` reach durable browser storage.
 * Call it before a `page.reload()` that must restore the form.
 *
 * The page saves each answer through a write queue. The last click of the fill returns before the queue commits its row.
 * A reload that overtakes the commit loses that answer, and the form restores without it. So the wait polls the stored
 * rows through `waitForStoredEntry`. The caller lacks the agent ID, so the wait reads every control answer row of the
 * account.
 */
export async function waitForMcpProbeFormDraft(page: Page, userId: string): Promise<void> {
  await waitForStoredEntry(page, accountStorageKey(userId, PREFIX_CONTROL_STATE), mcpProbeFormDraftSaved, 'the probe form answers must be persisted before the reload')
}

/** Require the answers of `fillMcpProbeForm` in the form that the page restored after a reload. */
export async function expectMcpProbeFormRestored(form: Locator): Promise<void> {
  await expect(form.getByLabel('Count *')).toHaveValue('0')
  await expect(form.getByRole('button', { name: 'Enabled *', exact: true })).toHaveText('No')
  await expect(form.getByRole('button', { name: 'Color *', exact: true })).toHaveText('Blue')
}

/** What one round trip of the probe form runs. */
export interface McpProbeFormRoundTrip {
  /** The ID of the native call of the `ask` tool. */
  callId: string
  /** Answer the permission request of the MCP tool, for a provider that asks before the tool runs. */
  approveTool?: () => Promise<void>
  /** Reload the page after the fill, and require the restored answers before the submit. */
  reloadBeforeSubmit: boolean
}

/**
 * Round-trip the form of the probe server through the browser, and return the model request that holds the result:
 *
 * - Call the `ask` tool of the probe form server.
 * - Answer its form with values that expose false and zero.
 * - Require the accepted answers in the tool result that the model reads.
 *
 * The probe server returns `FORM_ROUND_TRIP_OK` only for the exact answers of `fillMcpProbeForm`.
 */
export async function exerciseMcpProbeFormRoundTrip(
  context: Pick<ManagedNativeScenarioContext, 'page' | 'modelScript' | 'provider' | 'textStep' | 'readToolResult' | 'leapmuxServer'>,
  options: McpProbeFormRoundTrip,
): Promise<MockModelRequestRecord> {
  const { page, modelScript } = context
  const userId = context.leapmuxServer.adminUserId
  if (options.reloadBeforeSubmit && !userId)
    throw new Error('A reload of the probe form needs the account that saves its answers.')
  const start = await modelScript.queue(
    { toolCalls: [mcpToolCall(context.provider, options.callId, { server: MCP_FORM_SERVER_NAME, tool: 'ask', input: {} })] },
    nativeTextStep(context, 'The MCP probe form completed.'),
  )
  await sendMessage(page, modelScript.prompt(`Call the ${MCP_FORM_SERVER_NAME} ask tool exactly once.`))
  await modelScript.waitForSteps(start + 1)
  await options.approveTool?.()
  const form = await fillMcpProbeForm(page)
  if (options.reloadBeforeSubmit && userId) {
    // The page saves the last answer through a write queue. A reload that overtakes the write drops that answer.
    await waitForMcpProbeFormDraft(page, userId)
    await page.reload()
    await expectMcpProbeFormRestored(form)
  }
  await controlActions(page).getByRole('button', { name: 'Approve', exact: true }).click()
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  const request = await modelScript.requestAt(start + 1)
  expect((await nativeToolOutcome(context, request, options.callId)).text, 'the model reads the accepted answers of the form').toContain('FORM_ROUND_TRIP_OK')
  await expect(messageBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_OK' }).first()).toBeVisible()
  await expect(form).toHaveCount(0)
  return request
}
