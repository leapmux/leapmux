import { pickObject } from '~/lib/jsonPick'

/** Read the native renderer's inline output without its model-facing command record. */
export function codebuddyCommandOutput(block: Record<string, unknown> | undefined, text: string): string | undefined {
  // A persisted result carries its actual inline preview in the original wrapper.
  if (text.startsWith('<persisted-output>'))
    return undefined
  const meta = pickObject(block, '_meta')
  const response = pickObject(meta, 'rawResponse')
  // The CLI ignores late pipe data after process exit and can lose stderr.
  // Its empty failure record still states the command and the exit.
  // Keep that record intact.
  if (typeof response?.exitCode === 'number' && response.exitCode !== 0
    && /^Command: [^\r\n]+\nStdout: \(empty\)\nStderr: \(empty\)\nExit Code: -?\d+\nSignal: \(none\)$/u.test(text)) {
    return undefined
  }
  const renderer = pickObject(meta, 'renderer')
  return renderer?.type === 'text' && typeof renderer.value === 'string' ? renderer.value : undefined
}
