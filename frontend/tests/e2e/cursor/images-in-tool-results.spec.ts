import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { cursorGenerateImageToolCall } from '../helpers/providerToolCalls'
import { expectToolRowImage, toolResultImageForName, toolRows, writeToolImage } from '../helpers/toolImages'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest('draws the native GenerateImage result before and after reload', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
  const workingDir = authenticatedCursorWorkspace.workingDir
  if (!workingDir)
    throw new Error('the Cursor image test needs a working directory')
  const fileName = writeToolImage(workingDir, 'cursor-native')
  const path = join(workingDir, fileName)
  const imageData = readFileSync(path).toString('base64')
  await modelScript.queue({
    toolCalls: [cursorGenerateImageToolCall('cursor-image-1', 'A teal square', path, imageData)],
    text: 'The image is ready.',
  })
  await sendMessage(page, modelScript.prompt('Generate the teal square image.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expect(toolRows(page).filter({ hasText: 'A teal square' }).first()).toBeVisible()
  await expectToolRowImage(page, 'A teal square')
  await expect(await toolResultImageForName(page, 'A teal square')).toHaveAttribute('alt', 'A teal square')

  await page.reload()
  await expect(toolRows(page).filter({ hasText: 'A teal square' }).first()).toBeVisible()
  await expectToolRowImage(page, 'A teal square')
  await expect(await toolResultImageForName(page, 'A teal square')).toHaveAttribute('alt', 'A teal square')
  await expect(page.locator('[data-testid="result-divider"]:visible').last()).toBeVisible()
})
