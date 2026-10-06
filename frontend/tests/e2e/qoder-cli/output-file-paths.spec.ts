import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { qoderTest } from '../qoder-fixtures'
import { readQoderNativeOutput } from './outputFilePaths'

qoderTest('keeps the native shell output path and exact preview after reload', async ({ native }, testInfo) => {
  await captureNativeToolOutput(native, testInfo, {
    output: computedNativeToolOutput({ lineCount: 6000, padding: 48 }),
    callId: 'native-qoder-cli-output-path',
    proof: capture => proveNativeOutputReceipt(capture, testInfo, readQoderNativeOutput),
  })
})
