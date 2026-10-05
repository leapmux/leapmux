/**
 * Removes the escapes that the Markdown serializer adds to ordinary prose and that
 * protect nothing. The editor does this on the way out.
 *
 * The serializer (`mdast-util-to-markdown` and its GFM extensions) escapes a
 * character when some reader could take it for markup. A rule does not look at the
 * characters around it, or it looks too little. The AGENT is not a Markdown reader.
 * Each needless backslash reaches the agent as text, and the agent uses the text as
 * given. A live census asked ten providers for a file named `blank-new.txt`. The
 * message that they received said `blank-new\.txt`. Two of them created a file with
 * the backslash in its name. That is the right action for the name that they got.
 * See RL-021.
 *
 * Two escapes are needless. A measurement shows each one, not an assumption.
 *
 * ## A dot after `w`
 *
 * `mdast-util-gfm-autolink-literal` carries this rule:
 *
 * ```js
 * { character: '.', before: '[Ww]', after: '[\\-.\\w]', inConstruct: 'phrasing' }
 * ```
 *
 * The rule escapes EVERY dot that follows a `w` or a `W` and precedes a word
 * character. It does this so that a bare `www.example.com` cannot become an
 * autolink on a second read. The rule cannot tell `www.` from the end of an ordinary
 * word, so `new.txt`, `raw.json`, `show.me` and `draw.io` all leave the editor with
 * a backslash in them.
 *
 * A URL never reaches this rule. The parser turns a typed `www.example.com` into a
 * LINK node, and that node serializes as `[www.example.com](http://www.example.com)`.
 * The escape lands only on text that is not a URL.
 *
 * ## An underscore inside a word
 *
 * `mdast-util-to-markdown` carries this rule:
 *
 * ```js
 * { character: '_', inConstruct: 'phrasing', notInConstruct: fullPhrasingSpans }
 * ```
 *
 * The rule escapes EVERY `_` of ordinary text, so a typed `COMMANDCODE_MODE_CONTEXT`
 * left the editor as `COMMANDCODE\_MODE\_CONTEXT`. A run of underscores that has a
 * letter or a digit on both sides can neither open nor close emphasis. The
 * CommonMark flanking rules say this: such a run is both left-flanking and
 * right-flanking, and it has no punctuation next to it. A `_` that has whitespace,
 * punctuation or a line edge on one side can delimit emphasis. Its escape stays.
 *
 * ## What stays
 *
 * A backslash of the reader survives as two characters. The document holds what the
 * reader typed, so a literal backslash before a dot serializes as `\\.`. A literal
 * backslash before an underscore leaves `\\\_`, and the escape of that underscore
 * has a backslash on its left, not a letter. This module matches a SINGLE
 * backslash that has the right neighbors, so both cases stay.
 *
 * Code is the exception, and it is why this is not one regular expression. The
 * serializer writes a fenced block and an inline code span verbatim, so a backslash
 * inside them is the reader's own. The scan below skips both.
 */

/** A dot that the autolink rule escaped: one backslash, after `w`, before a word character. */
const ESCAPED_DOT = /(W)\\\.(?=[\w.-])/gi

/**
 * A run of underscores that the serializer escaped one by one, with a letter or a
 * digit on both sides. The look-behind and the look-ahead leave the neighbors out of
 * the match.
 */
const ESCAPED_INTRAWORD_UNDERSCORES = /(?<=[\p{L}\p{N}])(?:\\_)+(?=[\p{L}\p{N}])/gu

/** The opening or closing line of a fenced code block, at the start of a line. */
const FENCE_LINE = /^[ \t]{0,3}(`{3,}|~{3,})/

function stripSegment(text: string): string {
  return text
    .replace(ESCAPED_DOT, '$1.')
    .replace(ESCAPED_INTRAWORD_UNDERSCORES, run => run.replaceAll('\\', ''))
}

/**
 * Rewrites one line and leaves its inline code spans untouched.
 *
 * A backtick run opens a span that ends at the next run of the SAME length. This is
 * the CommonMark rule. A run with no partner opens nothing, so the rest of the line
 * is ordinary text.
 */
function stripLine(line: string): string {
  let out = ''
  let index = 0
  while (index < line.length) {
    const tick = line.indexOf('`', index)
    if (tick === -1) {
      out += stripSegment(line.slice(index))
      break
    }
    out += stripSegment(line.slice(index, tick))
    let runEnd = tick
    while (runEnd < line.length && line[runEnd] === '`')
      runEnd += 1
    const run = line.slice(tick, runEnd)
    const close = line.indexOf(run, runEnd)
    // CommonMark wants an EXACT match. A longer run at the closing position means
    // that the span stays open to the end of the line, so searching further for
    // another partner is wrong.
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

/** Removes the needless escapes of the serializer outside code spans and fenced blocks. */
export function stripNeedlessEscapes(markdown: string): string {
  const lines = markdown.split('\n')
  let fence = ''
  const out = lines.map((line) => {
    const match = FENCE_LINE.exec(line)
    // The fence group is mandatory in FENCE_LINE, so a match always fills it;
    // `?? ''` is the type-level guard alone.
    const marker = match?.[1] ?? ''
    if (fence) {
      // Inside a fence: a line that opens with the same fence character and is at
      // least as long closes it. Everything here is verbatim either way.
      if (match && marker[0] === fence[0] && marker.length >= fence.length)
        fence = ''
      return line
    }
    if (match) {
      fence = marker
      return line
    }
    return stripLine(line)
  })
  return out.join('\n')
}
