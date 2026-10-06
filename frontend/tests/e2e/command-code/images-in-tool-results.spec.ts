import { copyFileSync } from 'node:fs'
import { join } from 'node:path'
import { commandCodeTest, expect } from '../command-code-fixtures'
import { writeAttachmentFixture } from '../helpers/attachments'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { readToolCall } from '../helpers/providerToolCalls'
import { toolCallRow } from '../helpers/ui'
import { readCommandCodeNativeImage } from './nativeImage'

commandCodeTest('shows exact image bytes from the actual native file tool before and after reload', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const path = join(agent.workingDir, 'native-tool-image.png')
  copyFileSync(writeAttachmentFixture('image'), path)
  const { resultRequest } = await runNativeToolTurn(native, {
    toolCalls: [readToolCall(native.provider, 'native-image-read', path)],
    prompt: 'Read the supplied native image file.',
    answer: 'The actual native image reached the next model turn.',
  })
  // The native tool compresses the file before it attaches the image, so its result states the bytes
  // that the model receives and the browser draws. The bytes of the file are not those bytes.
  const nativeImage = readCommandCodeNativeImage(await readNativeMessageSnapshot(native, agent.id), 'native-image-read')
  const source = `data:${nativeImage.mediaType};base64,${nativeImage.data}`
  expect(JSON.stringify(resultRequest.body).includes(source), 'the next model request carries the compressed image').toBe(true)
  const image = toolCallRow(native.page, 'native-image-read').locator('img').first()
  await expect(image).toBeVisible()
  await expect(image).toHaveAttribute('src', source)
  await native.page.reload()
  await expect(image).toBeVisible()
  await expect(image).toHaveAttribute('src', source)
})
