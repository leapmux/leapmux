import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { elicitationFieldKey } from '../../../src/components/chat/controls/elicitationForm'
import { accountStorageKey, PREFIX_CONTROL_STATE } from '../../../src/lib/browserStorage'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { readEntry, storageKeys } from './storage'

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
 * A reload that overtakes the commit loses that answer, and the form restores without it.
 * A fixed sleep cannot account for a busy host, so poll the stored row.
 * The caller lacks the agent ID, so inspect the control answer rows of the account.
 * Build the prefix with accountStorageKey and compare with startsWith. A regular expression would interpret metacharacters in the key.
 */
export async function waitForMcpProbeFormDraft(page: Page, userId: string): Promise<void> {
  const prefix = accountStorageKey(userId, PREFIX_CONTROL_STATE)
  await expect.poll(async () => {
    for (const key of await storageKeys(page)) {
      if (key.startsWith(prefix) && mcpProbeFormDraftSaved((await readEntry(page, key))?.v))
        return true
    }
    return false
  }, 'the probe form answers must be persisted before the reload').toBe(true)
}
