import type { LogLine } from './timingFixture'
import { Buffer } from 'node:buffer'
import { createHash } from 'node:crypto'
import { expect } from '@playwright/test'
import { GENERATION_TRACE_FIELD, GENERATION_TRACE_MARKER } from '../../../src/generated/contracts/generation-trace'
import { ASSEMBLED_MESSAGE } from '../../../src/generated/contracts/worker-vocab'

/** Match a complete native text-buffer receipt from the private Worker's output. */
export function hasNativeGenerationReceipt(lines: readonly LogLine[], text: string): boolean {
  if (!text)
    throw new Error('A native generation receipt requires nonempty text.')
  const digest = createHash('sha256').update(text, 'utf8').digest('hex')
  const bytes = Buffer.byteLength(text, 'utf8')
  return lines.some(({ json }) => json?.marker === GENERATION_TRACE_MARKER
    && typeof json[GENERATION_TRACE_FIELD.Scope] === 'string'
    && String(json[GENERATION_TRACE_FIELD.Scope]).trim() !== ''
    && json[GENERATION_TRACE_FIELD.Kind] === ASSEMBLED_MESSAGE.KindText
    && json[GENERATION_TRACE_FIELD.SHA256] === digest
    && json[GENERATION_TRACE_FIELD.Bytes] === bytes)
}

/** Wait for the Worker's receipt before interrupting a provider without a live counter. */
export async function waitForNativeGenerationReceipt(lines: readonly LogLine[], text: string): Promise<void> {
  await expect.poll(() => hasNativeGenerationReceipt(lines, text), {
    message: 'the private Worker buffered the exact partial native text',
  }).toBe(true)
}
