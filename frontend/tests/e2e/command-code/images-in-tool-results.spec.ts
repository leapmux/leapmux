import { copyFileSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { commandCodeTest, expect } from '../command-code-fixtures'
import { writeAttachmentFixture } from '../helpers/attachments'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { readToolCall } from '../helpers/providerToolCalls'
import { sendMessage } from '../helpers/ui'
import { nativeContext } from './scenarios'

commandCodeTest('shows exact image bytes from the actual native file tool before and after reload', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  const path = join(agent.workingDir, 'native-tool-image.png')
  copyFileSync(writeAttachmentFixture('image'), path)
  const bytes = readFileSync(path).toString('base64')
  const start = (await modelScript.status()).stepCount
  await modelScript.queue({ toolCalls: [readToolCall(context.provider, 'native-image-read', path)] }, { text: 'The actual native image reached the next model turn.' })
  await sendMessage(page, modelScript.prompt('Read the supplied native image file.'))
  await waitForNativeToolSteps(context, start + 2)
  const request = (await modelScript.status()).requests.find(record => record.stepIndex === start + 1)
  expect(JSON.stringify(request?.body)).toContain(`data:image/png;base64,${bytes}`)
  const result = page.locator('[data-testid="message-bubble"][data-tool-call-id="native-image-read"][data-tool-row-role="result"]:visible')
  const image = result.locator('img').first()
  await expect(image).toBeVisible()
  await expect(image).toHaveAttribute('src', `data:image/png;base64,${bytes}`)
  await page.reload()
  await expect(image).toBeVisible()
  await expect(image).toHaveAttribute('src', `data:image/png;base64,${bytes}`)
})
