import { beforeEach, describe, expect, it, vi } from 'vitest'
import { linkRange, terminalWith } from '~/test-support/xtermBuffer'
import { readTerminalLinkLabel } from './terminalLinkLabel'
import { activateUntrustedLink, classifyUntrustedLink, linkLabelFromText } from './untrustedLinks'

const openExternalUrl = vi.hoisted(() => vi.fn(async (_url: string) => {}))
vi.mock('~/api/platformBridge', () => ({ openExternalUrl }))

/**
 * The placement of a link whose visible text is exactly `label`, on a row of
 * its own. The shortest way to state "the label reads like this" for the
 * classifier tests, which do not care where the cells are.
 */
function placementOf(label: string, logicalLine = label) {
  const labelStart = logicalLine.indexOf(label)
  return {
    label,
    logicalLine,
    labelStart,
    labelEnd: labelStart + label.length,
    rowStart: 0,
    rowEnd: logicalLine.length,
  }
}

describe('classifyUntrustedLink', () => {
  it.each([
    ['file:///etc/passwd'],
    ['javascript:alert(1)'],
    ['vscode://file/etc/passwd'],
    ['mailto:someone@example.test'],
    ['tel:+15550100'],
    ['C:\\Windows\\system32'],
    [''],
  ])('blocks %s, which no click may open', (uri) => {
    expect(classifyUntrustedLink(uri, placementOf(uri))).toEqual({ kind: 'block' })
  })

  it('opens an https link whose text spells its own address', () => {
    const uri = 'https://example.test/path'

    expect(classifyUntrustedLink(uri, placementOf(uri))).toEqual({ kind: 'open' })
  })

  it.each([
    ['example.test/path', 'https://example.test/path'],
    ['https://example.test', 'https://example.test/'],
    ['example.test', 'https://example.test/'],
    ['www.google.com', 'https://www.google.com'],
    ['www.google.com', 'https://www.google.com/'],
    ['foo.com/bar', 'https://foo.com/bar'],
  ])('opens on %s, the ordinary shorthand for %s', (label, uri) => {
    expect(classifyUntrustedLink(uri, placementOf(label))).toEqual({ kind: 'open' })
  })

  it('still confirms the same shorthand over plaintext http', () => {
    // The shorthand hides the scheme, so the missing encryption is exactly
    // what the reader cannot see. It prompts on `insecure` alone.
    expect(classifyUntrustedLink('http://foo.com/bar', placementOf('foo.com/bar'))).toEqual({
      kind: 'confirm',
      insecure: true,
      labelMismatch: false,
      misleadingLabel: null,
    })
  })

  it('confirms an http link even when its text matches', () => {
    const uri = 'http://example.test/path'

    expect(classifyUntrustedLink(uri, placementOf(uri))).toEqual({
      kind: 'confirm',
      insecure: true,
      labelMismatch: false,
      misleadingLabel: null,
    })
  })

  it('confirms when the text names something other than the address', () => {
    expect(classifyUntrustedLink('https://example.test/path', placementOf('Click here'))).toEqual({
      kind: 'confirm',
      insecure: false,
      labelMismatch: true,
      misleadingLabel: null,
    })
  })

  it.each([
    ['https://good.example/login'],
    ['www.good.example'],
    ['HTTPS://Good.Example'],
  ])('reports %s as an impersonated address', (label) => {
    expect(classifyUntrustedLink('https://evil.example/steal', placementOf(label))).toEqual({
      kind: 'confirm',
      insecure: false,
      labelMismatch: true,
      misleadingLabel: label,
    })
  })

  it('reports both reasons at once', () => {
    const action = classifyUntrustedLink('http://evil.example', placementOf('https://good.example'))

    expect(action).toEqual({
      kind: 'confirm',
      insecure: true,
      labelMismatch: true,
      misleadingLabel: 'https://good.example',
    })
  })

  it.each([
    ['www.google.com', 'https://google.com'],
    ['https://www.google.com', 'https://google.com/'],
    ['www.paypal.com', 'https://paypal.com.evil.test/'],
  ])('warns on %s, which is not the DNS name that %s opens', (label, uri) => {
    // A `www.` that the address does not carry (or drops) is a different host,
    // and nothing makes the two resolve to the same server. The label is
    // URL-shaped, so this lands in the loudest category on purpose.
    expect(classifyUntrustedLink(uri, placementOf(label))).toEqual({
      kind: 'confirm',
      insecure: false,
      labelMismatch: true,
      misleadingLabel: label,
    })
  })

  it('warns the SAME way in both www directions', () => {
    // The mirror of the case above, and it must not be quieter: `google.com`
    // over `https://www.google.com` is the identical deception reversed.
    expect(classifyUntrustedLink('https://www.google.com', placementOf('google.com'))).toEqual({
      kind: 'confirm',
      insecure: false,
      labelMismatch: true,
      misleadingLabel: 'google.com',
    })
  })

  it.each([
    ['Click here'],
    ['PR #472'],
    ['docs'],
  ])('leaves %s unarmed, because it states no destination to misstate', (label) => {
    expect(classifyUntrustedLink('https://example.test/x', placementOf(label))).toEqual({
      kind: 'confirm',
      insecure: false,
      labelMismatch: true,
      misleadingLabel: null,
    })
  })

  it.each([
    ['http://localhost:3000/'],
    ['http://127.0.0.1:8080/x'],
    ['http://app.localhost/'],
    ['http://[::1]:9000/'],
  ])('opens %s with no prompt: loopback puts nothing on a network', (uri) => {
    expect(classifyUntrustedLink(uri, placementOf(uri))).toEqual({ kind: 'open' })
  })

  it('still confirms plaintext http to a host that is not loopback', () => {
    expect(classifyUntrustedLink('http://127.example.test/', placementOf('http://127.example.test/'))).toMatchObject({
      kind: 'confirm',
      insecure: true,
    })
  })

  it.each([
    ['..'],
    ['./x'],
    ['../up'],
    ['foo.'],
    ['.foo'],
    ['plainword'],
  ])('does not read %s as an address, so it prompts without arming', (label) => {
    // A dot alone is not a host. Each of these still MISMATCHES and still
    // prompts -- it just makes no claim about a destination, so the button
    // stays unarmed.
    expect(classifyUntrustedLink('https://example.test/x', placementOf(label))).toEqual({
      kind: 'confirm',
      insecure: false,
      labelMismatch: true,
      misleadingLabel: null,
    })
  })

  it('accepts a filename as address-shaped, the cost of having no TLD test', () => {
    // `md` is a real country code, so no list of top-level domains separates
    // `README.md` from `google.com`. It arms, and the prompt only claims the
    // text LOOKS LIKE an address -- which stays true here.
    const action = classifyUntrustedLink('https://example.test/readme', placementOf('README.md'))

    expect(action).toEqual({
      kind: 'confirm',
      insecure: false,
      labelMismatch: true,
      misleadingLabel: 'README.md',
    })
  })

  it('confirms when the row is gone and the text cannot be checked', () => {
    expect(classifyUntrustedLink('https://example.test/', null)).toEqual({
      kind: 'confirm',
      insecure: false,
      labelMismatch: true,
      misleadingLabel: null,
    })
  })

  it('opens a fragment of an address the terminal wrapped', async () => {
    const uri = 'https://example.test/abcdefgh'
    const terminal = await terminalWith(uri, 20)

    // Both rows carry a fragment of one honest label. Neither equals the
    // address, and neither may raise a prompt.
    expect(classifyUntrustedLink(uri, readTerminalLinkLabel(terminal, linkRange(1, 1, 20)))).toEqual({ kind: 'open' })
    expect(classifyUntrustedLink(uri, readTerminalLinkLabel(terminal, linkRange(2, 1, 9)))).toEqual({ kind: 'open' })
  })

  it('confirms a fragment that the address merely contains somewhere else', async () => {
    // The hostile shape the wrap carve-out has to survive: the address quotes
    // a harmless site, and the label puts that quote alone on the second row.
    // The clicked fragment IS part of the address as a string, and it must
    // still prompt, because the whole label is nowhere in that address.
    const uri = 'https://evil.example/?next=https://good.example'
    const terminal = await terminalWith('SEE BELOW FOR THE https://good.example', 20)

    const action = classifyUntrustedLink(uri, readTerminalLinkLabel(terminal, linkRange(2, 1, 18)))

    // And the wrapped fragment is still named as an impersonation: it is read
    // back out of the joined line, where the scheme it lost survives.
    expect(action).toEqual({
      kind: 'confirm',
      insecure: false,
      labelMismatch: true,
      misleadingLabel: 'https://good.example',
    })
  })

  it('ignores an address elsewhere on the line that the link does not cover', async () => {
    // `docs` is the link; the printed address beside it belongs to nobody.
    const terminal = await terminalWith('https://other.test/x see docs', 40)

    const action = classifyUntrustedLink('https://example.test/d', readTerminalLinkLabel(terminal, linkRange(1, 26, 29)))

    expect(action).toEqual({
      kind: 'confirm',
      insecure: false,
      labelMismatch: true,
      misleadingLabel: null,
    })
  })
})

describe('linkLabelFromText', () => {
  it('collapses the whitespace HTML indentation adds', () => {
    // `textContent` carries the source formatting of the markup around it, and
    // none of that is what the reader sees.
    expect(linkLabelFromText('\n      https://good.example\n    ').label).toBe('https://good.example')
    expect(linkLabelFromText('the\n  docs').label).toBe('the docs')
  })

  it('places the whole label, so a single-line label needs no wrap arithmetic', () => {
    expect(linkLabelFromText('docs')).toEqual({
      label: 'docs',
      logicalLine: 'docs',
      labelStart: 0,
      labelEnd: 4,
      rowStart: 0,
      rowEnd: 4,
    })
  })

  it('survives an anchor with no text at all', () => {
    // An image-only anchor. Empty cannot spell any address, so it prompts.
    expect(classifyUntrustedLink('https://example.test/', linkLabelFromText(''))).toMatchObject({
      kind: 'confirm',
      labelMismatch: true,
      misleadingLabel: null,
    })
  })
})

describe('activateUntrustedLink', () => {
  const confirm = vi.fn(async () => true)

  beforeEach(() => {
    openExternalUrl.mockClear()
    confirm.mockClear()
    confirm.mockResolvedValue(true)
  })

  it('opens a matching https link with no prompt', async () => {
    const uri = 'https://example.test/path'

    await activateUntrustedLink(uri, placementOf(uri), confirm)

    expect(confirm).not.toHaveBeenCalled()
    expect(openExternalUrl).toHaveBeenCalledWith(uri)
  })

  it('opens nothing and asks nothing for a blocked scheme', async () => {
    await activateUntrustedLink('file:///etc/passwd', placementOf('passwords'), confirm)

    expect(confirm).not.toHaveBeenCalled()
    expect(openExternalUrl).not.toHaveBeenCalled()
  })

  it('opens after the user approves, and states both the text and the address', async () => {
    await activateUntrustedLink('https://evil.example', placementOf('https://good.example'), confirm)

    expect(confirm).toHaveBeenCalledWith({
      uri: 'https://evil.example',
      label: 'https://good.example',
      insecure: false,
      labelMismatch: true,
      misleadingLabel: 'https://good.example',
    })
    expect(openExternalUrl).toHaveBeenCalledWith('https://evil.example')
  })

  it('opens nothing when the user refuses', async () => {
    confirm.mockResolvedValue(false)

    await activateUntrustedLink('http://example.test/', placementOf('http://example.test/'), confirm)

    expect(confirm).toHaveBeenCalled()
    expect(openExternalUrl).not.toHaveBeenCalled()
  })
})
