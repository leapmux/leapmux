import { isObject } from '../../../src/lib/jsonPick'

/** The installed executor keeps this tail length for the controlled ASCII fixture. */
export const AMP_NATIVE_ASCII_TAIL_LENGTH = 50_000

export type AmpNativeOutputLimit
  = | { output: string, prefixLinesOmitted: number }
    | { output: string, exitCode: number }

/** Verify the retained native output against the complete controlled output. */
export function ampNativeOutputLimit(text: string, completeOutput: string): AmpNativeOutputLimit {
  if (typeof completeOutput !== 'string' || completeOutput === '')
    throw new Error('The Amp native output proof requires the complete controlled output.')
  const value: unknown = JSON.parse(text)
  if (!isObject(value) || typeof value.output !== 'string')
    throw new Error('The Amp large shell result has no native output string.')
  if (value.output.length >= completeOutput.length || !completeOutput.endsWith(value.output))
    throw new Error('The Amp retained output differs from the controlled complete output.')
  if (Object.hasOwn(value, 'truncation')) {
    const truncation = isObject(value.truncation) ? value.truncation : undefined
    if (!truncation || typeof truncation.prefixLinesOmitted !== 'number'
      || !Number.isSafeInteger(truncation.prefixLinesOmitted) || truncation.prefixLinesOmitted <= 0) {
      throw new Error('The Amp large shell result has no exact native omitted-prefix count.')
    }
    return { output: value.output, prefixLinesOmitted: truncation.prefixLinesOmitted }
  }
  if (value.output !== completeOutput.slice(-AMP_NATIVE_ASCII_TAIL_LENGTH))
    throw new Error('The Amp native tail differs from the exact controlled tail.')
  if (typeof value.exitCode !== 'number' || !Number.isSafeInteger(value.exitCode))
    throw new Error('The Amp native tail has no valid exit code.')
  return { output: value.output, exitCode: value.exitCode }
}
