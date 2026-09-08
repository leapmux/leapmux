import type { UntrustedLinkConfirm } from './untrustedLinks'
import { activateUntrustedLink, classifyUntrustedLink, linkLabelFromText } from './untrustedLinks'

/**
 * The attribute that marks an anchor as untrusted: an agent wrote its text and
 * its address, so the two may disagree on purpose.
 *
 * `rehypeExternalLinks` puts it on every anchor that survives the markdown
 * hardening pass, which is why no render path has to remember it. The few
 * agent-authored anchors that markdown does not build -- a web-search result,
 * a tool title, an image link -- set it themselves.
 *
 * An anchor WITHOUT it is first-party, and the app's own copy is not a
 * deception risk. `AboutDialog` is the reason the default runs this way round:
 * its licence link reads "Functional Source License, Version 1.1" over a
 * leapmux.dev address, which is an honest label and a mismatch at once.
 */
export const UNTRUSTED_LINK_ATTRIBUTE = 'data-untrusted-link'

/**
 * Route every click on an untrusted anchor under `root` through the same
 * policy a terminal hyperlink takes.
 *
 * Returns the function that removes the listeners.
 *
 * It intercepts NOTHING that the policy would open anyway. That is deliberate
 * on two counts: the existing route stays in charge of the ordinary link (the
 * opener plugin's own listener under the desktop shell, the browser otherwise),
 * and `window.open` keeps the user activation that a popup blocker looks for.
 * Only a link that needs a prompt, or that must not open at all, is taken over
 * -- and `preventDefault` is what takes it over, because the opener plugin's
 * injected listener begins by returning on `event.defaultPrevented`.
 *
 * A modified click (a new tab, a background tab) and a middle click are held
 * to the same rule. Each one is a way to open the link, so each one is a way
 * around the prompt, and `auxclick` is where a browser reports the middle
 * button.
 */
export function interceptUntrustedLinkClicks(
  root: HTMLElement,
  confirm: UntrustedLinkConfirm,
): () => void {
  const onClick = (event: MouseEvent) => {
    if (event.defaultPrevented)
      return
    const anchor = event.composedPath().find(
      (node): node is HTMLAnchorElement => node instanceof HTMLAnchorElement,
    )
    if (!anchor?.hasAttribute(UNTRUSTED_LINK_ATTRIBUTE))
      return
    // `href` over `getAttribute('href')`: the property is already resolved
    // against the document, so a relative address arrives here as the absolute
    // one the click would actually open.
    const uri = anchor.href
    const label = linkLabelFromText(anchor.textContent ?? '')
    if (classifyUntrustedLink(uri, label).kind === 'open')
      return

    event.preventDefault()
    void activateUntrustedLink(uri, label, confirm)
  }

  root.addEventListener('click', onClick, { capture: true })
  root.addEventListener('auxclick', onClick, { capture: true })
  return () => {
    root.removeEventListener('click', onClick, { capture: true })
    root.removeEventListener('auxclick', onClick, { capture: true })
  }
}
