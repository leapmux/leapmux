import { commandCodeTest } from '../command-code-fixtures'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { readCommandCodeNativeOutput } from './outputFilePaths'

commandCodeTest('keeps the native output path and exact inline preview after reload', async ({ native }, testInfo) => {
  await captureNativeToolOutput(native, testInfo, {
    output: computedNativeToolOutput({ lineCount: 8000, padding: 30 }),
    callId: 'native-output-path',
    proof: capture => proveNativeOutputReceipt(capture, testInfo, readCommandCodeNativeOutput),
  })
})
