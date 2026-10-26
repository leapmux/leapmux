import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { nativeUserStrings } from '../helpers/attachmentModelProbe'
import { proveNativeAudioAttachments } from '../helpers/nativeAudioAttachments'

geminiTest('delivers WAV and FLAC with and without prompts and retains the recordings after reload', async ({ native }) => {
  const [first] = await proveNativeAudioAttachments(native, 'google-generative-language')
  if (!first)
    throw new Error('The Gemini audio proof made no model request.')
  // The initial prompted turn declares the recording once, as the original WAV proof required.
  expect(nativeUserStrings(first.body).filter(value => value === 'audio/wav')).toHaveLength(1)
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/498.
