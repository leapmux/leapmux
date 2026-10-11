/** Decode native XML text once. Encoded entity text must not decode twice. */
export function decodeNativeXmlText(text: string): string {
  return text.replace(/&(lt|gt|quot|apos|amp);/g, (_, entity: string) => {
    switch (entity) {
      case 'lt': return '<'
      case 'gt': return '>'
      case 'quot': return '"'
      case 'apos': return '\''
      case 'amp': return '&'
      default: throw new Error('The native XML contains an unsupported entity.')
    }
  })
}

/**
 * Encode text as the agents that write task notices encode it, so a test can match the text inside a notice. Letta
 * and Pi escape `&`, `<`, and `>`, and leave quotes as they are.
 */
export function encodeNativeXmlText(text: string): string {
  return text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
}

/** Read one exact XML field. A repeated field has no single authority. */
export function nativeXmlField(body: string, tag: string): string | undefined {
  if (!/^[a-z][a-z0-9-]*$/.test(tag))
    throw new Error('The native XML field requires a valid tag.')
  const fields = [...body.matchAll(new RegExp(`<${tag}>([\\s\\S]*?)</${tag}>`, 'g'))]
  if (fields.length > 1)
    throw new Error(`The native XML repeats ${tag}.`)
  const value = fields[0]?.[1]
  return value === undefined ? undefined : decodeNativeXmlText(value)
}
