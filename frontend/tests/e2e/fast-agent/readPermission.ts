import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { answerControl, controlBanner } from '../helpers/ui'

/**
 * Approve the `read_text_file` request of a Fast Agent child if Fast Agent asks for one, and wait until `childWentOn`
 * reports that the child continued.
 *
 * Fast Agent asks for the approval of a child Read in some sessions and not in others, so a spec cannot script the
 * request. The poll waits for whichever comes first: the child continues, or the banner asks.
 */
export async function allowReadIfAsked(page: Page, childWentOn: () => Promise<boolean>): Promise<void> {
  const banner = controlBanner(page)
  await expect.poll(async () => {
    if (await childWentOn())
      return 'continued'
    return await banner.isVisible() ? 'asked' : 'waiting'
  }, { message: 'the Fast Agent child continues or asks to read its file' }).not.toBe('waiting')
  if (await childWentOn())
    return
  await expect(banner).toContainText('read_text_file')
  await answerControl(page, 'allow')
}
