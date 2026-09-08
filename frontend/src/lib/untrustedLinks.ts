import { openExternalUrl } from '~/api/platformBridge'
import { createLogger } from './logger'

const log = createLogger('untrustedLinks')

/**
 * The schemes an untrusted link may point at. A click on anything else does
 * nothing at all.
 *
 * Neither surface's text is the app's own. An OSC 8 hyperlink carries whatever
 * URI a program printed into a terminal, and a markdown anchor carries
 * whatever an agent wrote, so `file:`, `javascript:` and every custom
 * application scheme are refused rather than handed to the operating system.
 *
 * Each surface also refuses them upstream, and this is the gate that holds
 * when one of those stops: xterm drops a non-http(s) URI in `OscLinkProvider`,
 * but only while `ILinkHandler.allowNonHttpProtocols` stays unset, and
 * `rehypeExternalLinks` unwraps the anchor entirely, but only for what runs
 * through the markdown pipeline.
 */
const ALLOWED_LINK_PROTOCOLS: readonly string[] = ['http:', 'https:']

/**
 * A label that reads as an address of its own, rather than as prose: it holds
 * no whitespace, and it either opens with a scheme or carries a dot inside its
 * first segment. That is what the confirm prompt exists to expose -- a link
 * that shows `google.com` and opens `https://www.google.com`.
 *
 * The dot test accepts `README.md` too, and that is the deliberate trade. The
 * alternative is a list of top-level domains, which cannot separate the two:
 * `md`, `io`, `sh`, `rs` and `py` are all real country codes AND all common
 * file extensions. So the wider test wins, at the cost of one extra click on a
 * filename -- and the prompt says the text LOOKS LIKE an address, which stays
 * true of a filename.
 *
 * A label that holds whitespace (`Click here`, `PR #472`) states no
 * destination at all, so it cannot misstate one. It still prompts, without the
 * arming.
 */
const ADDRESS_SCHEME = /^[a-z][a-z0-9+.-]*:\/\/./i

function looksLikeAddress(label: string): boolean {
  if (label.length === 0 || /\s/.test(label))
    return false
  if (ADDRESS_SCHEME.test(label))
    return true
  // The host part, up to the first path separator. Written as a split rather
  // than as one regex because the regex form needs two open-ended quantifiers
  // that can trade characters, and a hostile label would then cost polynomial
  // time to reject.
  const host = label.split('/', 1)[0]
  return host.includes('.') && !host.startsWith('.') && !host.endsWith('.')
}

/**
 * Hosts that `http:` reaches without putting anything on a network.
 *
 * Loopback traffic never leaves the machine, so the prompt's own sentence --
 * that what you send travels unencrypted -- is false for it. A development
 * terminal prints `http://localhost:3000` on almost every run, and a prompt
 * that fires there teaches the reader to click through the ones that matter.
 * RFC 6761 reserves the whole `.localhost` name, and `127.0.0.0/8` is loopback
 * in its entirety.
 */
function isLoopbackHost(url: URL): boolean {
  const host = url.hostname.toLowerCase()
  return host === 'localhost'
    || host.endsWith('.localhost')
    || host === '[::1]'
    || /^127\.\d+\.\d+\.\d+$/.test(host)
}

/** Where a link's visible text sits inside the logical line that holds it. */
export interface UntrustedLinkLabel {
  /** The text of the cells the clicked link range covers. */
  label: string
  /** Every row of the wrapped-line group, joined untrimmed. */
  logicalLine: string
  /** Offset of `label` in `logicalLine`. */
  labelStart: number
  /** End offset of `label` in `logicalLine`, exclusive. */
  labelEnd: number
  /** Offset in `logicalLine` where the clicked row starts. */
  rowStart: number
  /** Offset in `logicalLine` where the clicked row ends, exclusive. */
  rowEnd: number
}

/** What the app must do with a link the user clicked. */
export type UntrustedLinkAction
  = | { kind: 'block' }
    | { kind: 'open' }
    | ({ kind: 'confirm' } & UntrustedLinkRisk)

/** Why a link needs the user to confirm before it opens. */
export interface UntrustedLinkRisk {
  /** The address is `http:`, so nothing it carries is encrypted. */
  insecure: boolean
  /** The shown text does not spell the address behind it. */
  labelMismatch: boolean
  /**
   * The shown text, when it reads as an address of its own and disagrees with
   * the one behind it. Null in every other case. This is the deceptive one,
   * and the only one that arms the prompt's button.
   */
  misleadingLabel: string | null
}

/** What the confirm prompt shows the user. */
export interface UntrustedLinkConfirmRequest extends UntrustedLinkRisk {
  /** The address the link opens. */
  uri: string
  /** The text the terminal shows for it. Empty when the row is already gone. */
  label: string
}

/** Asks the user whether to open a link. Resolves false to refuse it. */
export type UntrustedLinkConfirm = (request: UntrustedLinkConfirmRequest) => Promise<boolean>

/**
 * The placement of a label that sits on one line and nothing else -- every
 * label outside a terminal.
 *
 * Only a terminal wraps one logical line over several rows and reports a link
 * once per row, so only a terminal needs the joined-line form that
 * `UntrustedLinkLabel` carries. A rendered anchor holds its whole text, and
 * the whitespace is collapsed because HTML indentation is not part of what the
 * reader sees.
 */
export function linkLabelFromText(text: string): UntrustedLinkLabel {
  const label = text.replace(/\s+/g, ' ').trim()
  return { label, logicalLine: label, labelStart: 0, labelEnd: label.length, rowStart: 0, rowEnd: label.length }
}

/**
 * Every spelling of `uri` that a label may honestly use.
 *
 * A terminal program routinely prints `example.com/x` for
 * `https://example.com/x`, and dropping the trailing slash of a bare origin is
 * just as common. Refusing those would put a prompt in front of the most
 * ordinary link there is, and a prompt that fires on everything teaches the
 * user to confirm without reading.
 *
 * Dropping the scheme cannot hide a downgrade, because `http:` raises
 * `insecure` on its own and prompts whatever the label says.
 *
 * A leading `www.` is NOT one of these spellings, deliberately. `www.foo.test`
 * and `foo.test` are two DNS names, and nothing makes them resolve to the same
 * server, so a label that adds or drops the prefix states an address that the
 * link does not open. That is the case the prompt exists for.
 */
function linkSpellings(uri: string): string[] {
  const spellings = new Set([uri, uri.replace(/\/$/, '')])
  for (const spelling of [...spellings]) {
    const bare = spelling.replace(/^https?:\/\//, '')
    if (bare)
      spellings.add(bare)
  }
  return [...spellings]
}

/** Whether the visible label spells the address behind it. */
function labelSpellsUri(placement: UntrustedLinkLabel, uri: string): boolean {
  const { label, logicalLine, labelStart, labelEnd, rowStart, rowEnd } = placement
  for (const spelling of linkSpellings(uri)) {
    if (label === spelling)
      return true
    // The label may be one row's fragment of an address the terminal wrapped.
    // Accept the fragment only where the WHOLE spelling is present and sits so
    // that the fragment is the part of it that fell on this row: it must start
    // where the spelling starts or at the row's left edge, and end where the
    // spelling ends or at the row's right edge.
    //
    // A logical line that merely CONTAINS the fragment somewhere else fails,
    // which is what stops a label from borrowing a harmless-looking substring
    // out of a hostile address.
    for (let at = logicalLine.indexOf(spelling); at !== -1; at = logicalLine.indexOf(spelling, at + 1)) {
      const end = at + spelling.length
      if (at > labelStart || end < labelEnd)
        continue
      if (at !== labelStart && labelStart !== rowStart)
        continue
      if (end !== labelEnd && labelEnd !== rowEnd)
        continue
      return true
    }
  }
  return false
}

/**
 * The URL-shaped word that the clicked label sits inside, when it disagrees
 * with the address behind it.
 *
 * It reads the whole logical line rather than the clicked range alone, because
 * a wrapped label reaches this function one row at a time: the deceptive
 * `https://good.example` arrives as the fragment `tps://good.example`, which
 * starts with no scheme and would read as an ordinary mismatch. A word that
 * does NOT overlap the clicked cells is somebody else's text on the same line,
 * so the overlap test is what keeps an unrelated address off this prompt.
 */
function misleadingLabelAt(placement: UntrustedLinkLabel, uri: string): string | null {
  const spellings = new Set(linkSpellings(uri))
  const words = /\S+/g
  for (let word = words.exec(placement.logicalLine); word !== null; word = words.exec(placement.logicalLine)) {
    const start = word.index
    const end = start + word[0].length
    if (end <= placement.labelStart || start >= placement.labelEnd)
      continue
    if (looksLikeAddress(word[0]) && !spellings.has(word[0]))
      return word[0]
  }
  return null
}

/** Decide what a click on `uri` may do, given the text the terminal shows for it. */
export function classifyUntrustedLink(uri: string, placement: UntrustedLinkLabel | null): UntrustedLinkAction {
  let parsed: URL
  try {
    parsed = new URL(uri)
  }
  catch {
    return { kind: 'block' }
  }
  if (!ALLOWED_LINK_PROTOCOLS.includes(parsed.protocol))
    return { kind: 'block' }

  const insecure = parsed.protocol === 'http:' && !isLoopbackHost(parsed)
  // A missing placement means the row scrolled out of the buffer, so the label
  // cannot be checked. An unchecked label is exactly what the prompt exists to
  // disclose, so it counts as a mismatch rather than as a pass.
  const labelMismatch = placement === null || !labelSpellsUri(placement, uri)
  const misleadingLabel = labelMismatch && placement ? misleadingLabelAt(placement, uri) : null
  if (!insecure && !labelMismatch)
    return { kind: 'open' }
  return { kind: 'confirm', insecure, labelMismatch, misleadingLabel }
}

/**
 * Open an untrusted link the user clicked -- a terminal hyperlink, an anchor
 * an agent wrote: refuse it, confirm it, or hand it to the browser.
 *
 * `confirm` asks the user. A caller with no prompt to show passes one that
 * resolves false, which fails CLOSED -- a link whose risk nobody can disclose
 * must not open by itself.
 */
export async function activateUntrustedLink(
  uri: string,
  placement: UntrustedLinkLabel | null,
  confirm: UntrustedLinkConfirm,
): Promise<void> {
  const action = classifyUntrustedLink(uri, placement)
  if (action.kind === 'block') {
    log.debug('refused a terminal link outside the allowed schemes', { uri })
    return
  }
  if (action.kind === 'confirm') {
    const approved = await confirm({
      uri,
      label: placement?.label ?? '',
      insecure: action.insecure,
      labelMismatch: action.labelMismatch,
      misleadingLabel: action.misleadingLabel,
    })
    if (!approved)
      return
  }
  await openExternalUrl(uri)
}
