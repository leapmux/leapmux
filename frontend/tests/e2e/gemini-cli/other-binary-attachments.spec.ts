import { Buffer } from 'node:buffer'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { geminiTest } from '../gemini-fixtures'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { createTestDirectory } from '../helpers/runDirectory'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from '../helpers/ui'

geminiTest('delivers a valid WAV with its exact native MIME type and decoded bytes', async ({ page, modelScript, authenticatedGeminiWorkspace }) => {
  void authenticatedGeminiWorkspace
  const bytes = Buffer.alloc(76)
  bytes.write('RIFF', 0)
  bytes.writeUInt32LE(bytes.length - 8, 4)
  bytes.write('WAVE', 8)
  bytes.write('fmt ', 12)
  bytes.writeUInt32LE(16, 16)
  bytes.writeUInt16LE(1, 20)
  bytes.writeUInt16LE(1, 22)
  bytes.writeUInt32LE(8000, 24)
  bytes.writeUInt32LE(16000, 28)
  bytes.writeUInt16LE(2, 32)
  bytes.writeUInt16LE(16, 34)
  bytes.write('data', 36)
  bytes.writeUInt32LE(32, 40)
  for (let index = 44; index < bytes.length; index += 2)
    bytes.writeInt16LE((index - 44) * 256 - 4096, index)
  const file = join(createTestDirectory('gemini-native-audio-'), 'native-audio.wav')
  writeFileSync(file, bytes)
  await modelScript.queue({ text: 'The native WAV reached the model.' })
  await expectAttachmentOutcome(page, 'binary', { supported: true, fileName: 'native-audio.wav', fixturePath: file })
  await sendWithAttachment(page, modelScript.prompt('Read the valid WAV.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const request = status.requests.find(row => row.stepIndex === 0)
  expect(request?.protocol).toBe('google-generative-language')
  if (!request || !isObject(request.body) || !Array.isArray(request.body.contents))
    throw new Error('The native WAV reached no Google model request.')
  const mediaParts = request.body.contents.filter(isObject).filter(row => row.role === 'user').flatMap(row => Array.isArray(row.parts) ? row.parts.filter(isObject) : []).map(part => part.inlineData).filter(isObject)
  const audio = mediaParts.filter(part => part.mimeType === 'audio/wav')
  expect(audio).toHaveLength(1)
  expect(typeof audio[0]?.data).toBe('string')
  expect(Buffer.from(String(audio[0]?.data), 'base64')).toEqual(bytes)
  await expectUserMessage(page, 'native-audio.wav')
  await expect(assistantBubbles(page).filter({ hasText: 'The native WAV reached the model.' })).toBeVisible()
})
