/** Wrap literal text in a Markdown code fence that the text cannot close. */
export function literalCodeBlock(text: string): string {
  let longest = 0
  for (const run of text.matchAll(/`+/g))
    longest = Math.max(longest, run[0].length)
  const fence = '`'.repeat(Math.max(3, longest + 1))
  return `${fence}\n${text}\n${fence}`
}
