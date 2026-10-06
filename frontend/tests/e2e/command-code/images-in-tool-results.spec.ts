import { copyFileSync } from 'node:fs'
import { join } from 'node:path'
import { commandCodeTest, expect } from '../command-code-fixtures'
import { writeAttachmentFixture } from '../helpers/attachments'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { readToolCall } from '../helpers/providerToolCalls'
import { sendMessage } from '../helpers/ui'
import { readCommandCodeNativeImage } from './nativeImage'
import { nativeContext } from './scenarios'

commandCodeTest('shows exact image bytes from the actual native file tool before and after reload', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  const path = join(agent.workingDir, 'native-tool-image.png')
  copyFileSync(writeAttachmentFixture('image'), path)
  const start = (await modelScript.status()).stepCount
  await modelScript.queue({ toolCalls: [readToolCall(context.provider, 'native-image-read', path)] }, { text: 'The actual native image reached the next model turn.' })
  await sendMessage(page, modelScript.prompt('Read the supplied native image file.'))
  await waitForNativeToolSteps(context, start + 2)
  // The native tool compresses the file before it attaches the image, so its result states the bytes
  // that the model receives and the browser draws. The bytes of the file are not those bytes.
  const native = readCommandCodeNativeImage(await readNativeMessageSnapshot(context, agent.id), 'native-image-read')
  const source = `data:${native.mediaType};base64,${native.data}`
  const request = (await modelScript.status()).requests.find(record => record.stepIndex === start + 1)
  expect(JSON.stringify(request?.body)).toContain(source)
  const result = page.locator('[data-testid="message-bubble"][data-tool-call-id="native-image-read"][data-tool-row-role="result"]:visible')
  const image = result.locator('img').first()
  await expect(image).toBeVisible()
  await expect(image).toHaveAttribute('src', source)
  await page.reload()
  await expect(image).toBeVisible()
  await expect(image).toHaveAttribute('src', source)
})
