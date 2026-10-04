/** Compare the retained native result with the result that reached the next model request. */
export function codewhaleBashModelMatches(workerText: string, modelText: string): boolean {
  const characters = Array.from(workerText)
  if (characters.length <= 48_000)
    return workerText === modelText
  const marker = '\n\n[... output truncated for context ...]\n\n'
  const maximum = 4000
  const remaining = maximum - Array.from(marker).length
  const head = Math.floor(remaining * 2 / 3)
  const tail = remaining - head
  const snippet = characters.slice(0, head).join('') + marker + characters.slice(-tail).join('')
  const projected = `[bash output compacted to protect context]\nSnippet: ${snippet}\n(Original: ${characters.length} chars, omitted: ${characters.length - maximum} chars.)`
  return projected === modelText
}
