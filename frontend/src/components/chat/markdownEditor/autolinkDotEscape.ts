/**
 * Undoes the one escape the GitHub Flavored Markdown autolink rule adds to ordinary
 * prose, on the way out of the editor.
 *
 * `mdast-util-gfm-autolink-literal` carries this rule:
 *
 * ```js
 * { character: '.', before: '[Ww]', after: '[\\-.\\w]', inConstruct: 'phrasing' }
 * ```
 *
 * It escapes EVERY dot that follows a `w` or a `W` and precedes a word character, so
 * that a bare `www.example.com` cannot be re-read as an autolink. The rule cannot tell
 * `www.` from the end of an ordinary word, so `new.txt`, `raw.json`, `show.me` and
 * `draw.io` all leave the editor with a backslash in them.
 *
 * That backslash reaches the AGENT. A live census asked ten providers for a file named
 * `blank-new.txt`; the message they received said `blank-new\.txt`, and two of them
 * created a file with the backslash in its name -- the right thing to do with the name
 * they were given. See RL-021.
 *
 * Two facts make the reversal safe, and both are measured rather than assumed.
 *
 * A URL never reaches this rule. The parser turns a typed `www.example.com` into a LINK
 * node, which serializes as `[www.example.com](http://www.example.com)`, so the escape
 * only ever lands on text that is not a URL.
 *
 * A backslash of the user's own survives as two characters. The document holds what the
 * reader typed, so a literal backslash before a dot serializes as `\\.` and this
 * function leaves it alone -- it matches a SINGLE backslash only.
 *
 * Code is the exception, and it is why this is not one regular expression. A fenced
 * block and an inline code span are written verbatim, so a backslash inside them is the
 * user's own and must stay. The scan below skips both.
 */

/** A dot escaped by the autolink rule: one backslash, after `w`, before a word character. */
const ESCAPED_DOT = /(W)\\\.(?=[\w.-])/gi

/** The opening or closing line of a fenced code block, at the start of a line. */
const FENCE_LINE = /^[ \t]{0,3}(`{3,}|~{3,})/

function unescapeSegment(text: string): string {
  return text.replace(ESCAPED_DOT, '$1.')
}

/**
 * Rewrites one line, leaving its inline code spans untouched.
 *
 * A backtick run opens a span that ends at the next run of the SAME length, which is
 * CommonMark's rule. A run with no partner opens nothing, so the rest of the line is
 * ordinary text.
 */
function unescapeLine(line: string): string {
  let out = ''
  let index = 0
  while (index < line.length) {
    const tick = line.indexOf('`', index)
    if (tick === -1) {
      out += unescapeSegment(line.slice(index))
      break
    }
    out += unescapeSegment(line.slice(index, tick))
    let runEnd = tick
    while (runEnd < line.length && line[runEnd] === '`')
      runEnd += 1
    const run = line.slice(tick, runEnd)
    const close = line.indexOf(run, runEnd)
    // A run whose closing partner would itself be part of a longer run does not close
    // the span, so keep looking is wrong here: CommonMark wants an EXACT match, and a
    // longer run at that position means this span stays open to the end of the line.
    const closesExactly = close !== -1 && line[close + run.length] !== '`'
    if (!closesExactly) {
      out += line.slice(tick)
      break
    }
    out += line.slice(tick, close + run.length)
    index = close + run.length
  }
  return out
}

/** Undoes the autolink rule's dot escape outside code spans and fenced blocks. */
export function unescapeAutolinkDots(markdown: string): string {
  const lines = markdown.split('\n')
  let fence = ''
  const out = lines.map((line) => {
    const match = FENCE_LINE.exec(line)
    if (fence) {
      // Inside a fence: a line that opens with the same fence character and is at
      // least as long closes it. Everything here is verbatim either way.
      if (match && match[1][0] === fence[0] && match[1].length >= fence.length)
        fence = ''
      return line
    }
    if (match) {
      fence = match[1]
      return line
    }
    return unescapeLine(line)
  })
  return out.join('\n')
}
