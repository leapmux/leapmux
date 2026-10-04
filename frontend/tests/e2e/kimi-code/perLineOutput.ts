import type { NativeToolOutput } from '../helpers/nativeToolOutput'
import { randomUUID } from 'node:crypto'

/** Force native per-line shaping while keeping the complete middle marker outside the model's preview. */
export function computedKimiPerLineOutputFileOutput(): NativeToolOutput {
  const prefix = `KIMIPERLINE${randomUUID().replaceAll('-', '')}`
  const firstMarker = `${prefix}-first`
  const omittedMarker = `${prefix}-middle-77`
  const lastMarker = `${prefix}-complete-42`
  const source = [
    `const outputFilePrefix = ${JSON.stringify(prefix)};`,
    'const completeOutput = [outputFilePrefix + "-first" + "x".repeat(30000), "y".repeat(30000) + outputFilePrefix + "-middle-" + (70 + 7), outputFilePrefix + "-complete-" + (40 + 2)].join("\\n");',
  ].join(' ')
  const text = `${firstMarker}${'x'.repeat(30_000)}\n${'y'.repeat(30_000)}${omittedMarker}\n${lastMarker}`
  return { source, text, firstMarker, omittedMarker, lastMarker }
}
