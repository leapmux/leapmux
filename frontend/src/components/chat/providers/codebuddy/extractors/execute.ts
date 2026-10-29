import { pickObject } from '~/lib/jsonPick'

/** Read the native renderer's inline output without its model-facing command record. */
export function codebuddyCommandOutput(block: Record<string, unknown> | undefined, text: string): string | undefined {
  // A persisted result carries its actual inline preview in the original wrapper.
  if (text.startsWith('<persisted-output>'))
    return undefined
  const meta = pickObject(block, '_meta')
  const response = pickObject(meta, 'rawResponse')
  // CodeBuddy 2.160.0 could lose stderr when the process exited before its pipe readers finished.
  // An empty failure record still carries the exit and stream status, with or without a command header.
  // Keep that record intact.
  if (typeof response?.exitCode === 'number' && response.exitCode !== 0
    && /^(?:Command: [^\r\n]+\n)?Stdout: \(empty\)\nStderr: \(empty\)\nExit Code: -?\d+\nSignal: \(none\)$/u.test(text)) {
    return undefined
  }
  const renderer = pickObject(meta, 'renderer')
  return renderer?.type === 'text' && typeof renderer.value === 'string'
    ? renderer.value
    : undefined
}
