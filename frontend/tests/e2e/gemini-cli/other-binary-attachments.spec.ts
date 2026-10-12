import { Buffer } from 'node:buffer'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { exerciseAttachmentDelivery, nativeUserStrings } from '../helpers/attachmentModelProbe'
import { createTestDirectory } from '../helpers/runDirectory'

geminiTest('delivers a valid WAV with its exact native MIME type and decoded bytes', async ({ native }) => {
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
  // Gemini sends the file as a Google `inlineData` part with the exact WAV type and bytes.
  const request = await exerciseAttachmentDelivery(native, 'binary', 'native-audio.wav', { fixturePath: file, protocol: 'google-generative-language', binaryMediaType: 'audio/wav' })
  // Gemini sends the file once: the user content declares exactly one audio part.
  expect(nativeUserStrings(request.body).filter(value => value === 'audio/wav')).toHaveLength(1)
})
