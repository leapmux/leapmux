import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { readToolCall } from '../helpers/providerToolCalls'
import { runToolImageTurn } from '../helpers/toolImages'
import { chatScrollContainer, messageContents } from '../helpers/ui'

diracTest('receives a real native PNG read without a rendered tool-result image', async ({ native, authenticatedDiracWorkspace }) => {
  const { fileName, resultRequest } = await runToolImageTurn(native, {
    workingDir: authenticatedDiracWorkspace.workingDir,
    marker: 'dirac-native-read',
    toolCall: image => readToolCall(native.provider, 'dirac-image-read', image.path),
  })
  expect(nativeToolResult(resultRequest, 'dirac-image-read')).not.toBe('')
  const answer = messageContents(native.page).filter({ hasText: `I inspected ${fileName}.` }).first()
  const pictures = chatScrollContainer(native.page).locator('button[aria-label="Open image"]')
  await expect(answer).toBeVisible()
  await expect(pictures).toHaveCount(0)
  await native.page.reload()
  // A zero count right after the reload passes before the transcript loads, so the answer must show first.
  await expect(answer).toBeVisible()
  await expect(pictures).toHaveCount(0)
})
