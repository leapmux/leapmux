import { isObject } from '../../../src/lib/jsonPick'

/** Read retained exec text after its authoritative native status header. */
export function codexNativeOutputExcerpt(text: string): string {
  const value: unknown = JSON.parse(text)
  if (!Array.isArray(value) || value.length < 2 || !isObject(value[0]) || value[0].type !== 'input_text'
    || typeof value[0].text !== 'string' || !/^Script completed\nWall time [\d.]+ seconds(?: \(code-mode [\d.]+ seconds; overhead -?[\d.]+ seconds\))?\nOutput:\n$/.test(value[0].text)) {
    throw new Error('The Codex native output limit requires the exact completed native exec header.')
  }
  return value.slice(1).map((block) => {
    if (!isObject(block) || block.type !== 'input_text' || typeof block.text !== 'string')
      throw new Error('The Codex native output limit contains unsupported native output content.')
    return block.text
  }).join('\n')
}
