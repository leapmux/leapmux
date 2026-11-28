import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'
import { codingAgentMatrixDimensions, findDuplicateTopH1, findOrphanH2Headings, verifyBuiltSiteArtifacts, verifyCodingAgentProviderCount, verifyCodingAgentSite, verifyCodingAgentTicker, verifyInternalAnchors, verifyReverseProxySamples } from './verify-coding-agent-site.mjs'

const require = createRequire(new URL('../frontend/package.json', import.meta.url))
const { JSDOM } = require('jsdom')

const SUPPORT_STATES = [
  { id: 'supported', symbol: '✅', label: 'Supported' },
  { id: 'agent-limit', symbol: '🚫', label: 'The agent does not offer it' },
  { id: 'leapmux-limit', symbol: '🚧', label: 'LeapMux does not support it yet' },
]
const CELL_DETAIL = 'Maintainer evidence that the website must never show.'
const PROVIDER_DETAIL = 'Provider evidence that stays with the maintainers.'

const features = {
  groups: [
    { id: 'tools', label: 'Tools' },
  ],
  features: [
    {
      id: 'mcp-input-request',
      label: 'MCP input request',
      description: 'Collects requested fields.',
      showInMatrix: true,
      group: 'tools',
    },
  ],
}
const checklist = {
  supportStates: SUPPORT_STATES,
  providerGroups: [
    [
      {
        id: 'pi',
        label: 'Pi',
        url: 'https://pi.dev/',
        icon: '/icons/agents/pi.svg',
        userNote: 'Provider note.',
        detailNote: PROVIDER_DETAIL,
      },
    ],
  ],
  cells: {
    'mcp-input-request': {
      pi: { support: 'agent-limit', userNote: 'Cell-specific **value**. See [the issue](https://pi.dev/issues/1).', detailNote: CELL_DETAIL },
    },
  },
}
const html = `
<p>LeapMux supports <span data-agent-provider-count>1</span> providers.</p>
<div class="provider-logos"><a href="https://pi.dev/">
  <img src="/icons/agents/pi.svg" alt="Pi"><span class="provider-hover-label">Pi</span>
</a></div>
<ul class="feature-matrix-legend" role="list"><li><span aria-hidden="true">✅</span> Supported</li><li><span aria-hidden="true">🚫</span> The agent does not offer it</li><li><span aria-hidden="true">🚧</span> LeapMux does not support it yet</li></ul>
<div class="feature-matrix"><table aria-label="Coding agent feature matrix, part 1 of 1">
  <thead><tr><th>Feature</th><th><a href="https://pi.dev/">
    <img src="/icons/agents/pi.svg" alt="Pi"><span class="provider-hover-label">Pi</span>
  </a><a href="#note-provider-pi">†</a></th></tr></thead>
  <tbody><tr class="feature-group-header"><th scope="rowgroup" colspan="2">Tools</th></tr>
    <tr><th scope="row"><a href="#feature-mcp-input-request">MCP input request</a></th>
    <td><span role="img" aria-label="The agent does not offer it">🚫</span><a href="#note-pi-mcp-input-request">†</a></td></tr></tbody>
</table></div>
<section class="feature-matrix-definitions"><dl>
  <dt id="feature-mcp-input-request">MCP input request</dt><dd>Collects requested fields.</dd>
</dl></section>
<section class="feature-matrix-notes">
  <div id="note-provider-pi"><strong>Pi:</strong><p>Provider note.</p></div>
  <div id="note-pi-mcp-input-request"><strong><span aria-hidden="true">🚫</span> Pi — MCP input request:</strong><p>Cell-specific <strong>value</strong>. See <a href="https://pi.dev/issues/1">the issue</a>.</p></div>
</section>
`

const SECOND_GROUP_BODY = `<tbody><tr class="feature-group-header"><th scope="rowgroup">Conversation</th>
    <td class="feature-group-logo" aria-hidden="true"><span class="provider-link"><img src="/icons/agents/pi.svg" alt=""><span class="provider-hover-label">Pi</span></span></td></tr>
    <tr><th scope="row"><a href="#feature-agent-questions">Agent questions</a></th><td><span role="img" aria-label="Supported">✅</span></td></tr></tbody>`

/** A second published feature in a second group, so the repeated group header exists. */
function withSecondGroup() {
  const extendedFeatures = structuredClone(features)
  extendedFeatures.groups.push({ id: 'conversation', label: 'Conversation' })
  extendedFeatures.features.push({
    id: 'agent-questions',
    label: 'Agent questions',
    description: 'Asks the user.',
    showInMatrix: true,
    group: 'conversation',
  })
  const extendedChecklist = structuredClone(checklist)
  extendedChecklist.cells['agent-questions'] = {
    pi: { support: 'supported', userNote: '', detailNote: '' },
  }
  const extendedHtml = html
    .replace('</tbody>', `</tbody>${SECOND_GROUP_BODY}`)
    .replace('</dl>', '<dt id="feature-agent-questions">Agent questions</dt><dd>Asks the user.</dd></dl>')
  return { features: extendedFeatures, checklist: extendedChecklist, html: extendedHtml }
}

function withHiddenFeature() {
  const extendedFeatures = structuredClone(features)
  extendedFeatures.features.push({
    id: 'basic-chat',
    label: 'Basic chat',
    description: 'Receives a prompt and returns an answer.',
    showInMatrix: false,
  })
  const extendedChecklist = structuredClone(checklist)
  extendedChecklist.cells['basic-chat'] = {
    pi: { support: 'agent-limit', userNote: '', detailNote: 'A test-only detail note that must stay off the website.' },
  }
  return { features: extendedFeatures, checklist: extendedChecklist }
}

describe('verifyCodingAgentSite', () => {
  it('accepts the generated table, legend, definitions, and user notes', () => {
    expect(verifyCodingAgentSite(html, features, checklist)).toEqual([])
  })

  it('rejects a missing provider count', () => {
    const invalid = html.replace('<span data-agent-provider-count>1</span>', 'one')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('the page contains no generated provider count')
  })

  it.each(['0', '2', '26', '', '01', '1 provider'])('rejects an incorrect provider count: %s', (count) => {
    const invalid = html.replace('data-agent-provider-count>1', `data-agent-provider-count>${count}`)
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain(`provider count 1 is ${JSON.stringify(count)}, expected 1`)
  })

  it('checks every provider count on the page', () => {
    const invalid = `${html}<span data-agent-provider-count>26</span>`
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('provider count 2 is "26", expected 1')
  })

  it('accepts multiple provider counts from the same source', () => {
    const valid = `${html}<span data-agent-provider-count>1</span>`
    expect(verifyCodingAgentSite(valid, features, checklist)).toEqual([])
  })

  it('preserves code examples with angle brackets and command-line flags', () => {
    const expected = structuredClone(checklist)
    expected.cells['mcp-input-request'].pi.userNote = 'Call `tools.<name>(arguments)` with `--no-extensions`.'
    const valid = html.replace('Cell-specific <strong>value</strong>. See <a href="https://pi.dev/issues/1">the issue</a>.', 'Call <code>tools.&lt;name&gt;(arguments)</code> with <code>--no-extensions</code>.')
    expect(verifyCodingAgentSite(valid, features, expected)).toEqual([])
    const missing = valid.replace('tools.&lt;name&gt;(arguments)', 'tools.(arguments)')
    expect(verifyCodingAgentSite(missing, features, expected)).toContain('cell note for pi/mcp-input-request differs from source')
  })

  it('omits test-only rows, definitions, and their notes from the rendered count', () => {
    const extended = withHiddenFeature()
    expect(verifyCodingAgentSite(html, extended.features, extended.checklist)).toEqual([])
    expect(codingAgentMatrixDimensions(extended.features, extended.checklist)).toEqual({ features: 1, cells: 1 })
  })

  it('rejects a test-only row in the published table', () => {
    const extended = withHiddenFeature()
    const invalid = html.replace('</tbody>', '<tr><th scope="row"><a href="#feature-basic-chat">Basic chat</a></th><td>✅</td></tr></tbody>')
    expect(verifyCodingAgentSite(invalid, extended.features, extended.checklist))
      .toContain('table 1 has 2 feature rows, expected 1')
  })

  it('rejects a test-only feature definition on the website', () => {
    const extended = withHiddenFeature()
    const invalid = html.replace('</dl>', '<dt id="feature-basic-chat">Basic chat</dt><dd>Receives a prompt and returns an answer.</dd></dl>')
    expect(verifyCodingAgentSite(invalid, extended.features, extended.checklist))
      .toContain('hidden feature basic-chat appears in feature definitions')
  })

  it('rejects a test-only cell note on the website', () => {
    const extended = withHiddenFeature()
    const invalid = `${html}<div id="note-pi-basic-chat">A test-only cell note.</div>`
    expect(verifyCodingAgentSite(invalid, extended.features, extended.checklist))
      .toContain('hidden cell note for pi/basic-chat appears on the website')
  })

  it('rejects a changed evidence URL with the same visible text', () => {
    expect(verifyCodingAgentSite(html.replace('https://pi.dev/issues/1', 'https://pi.dev/wrong'), features, checklist))
      .toContain('cell note for pi/mcp-input-request differs from source')
  })

  it('rejects a changed provider note', () => {
    expect(verifyCodingAgentSite(html.replace('<p>Provider note.</p>', '<p>Provider.</p>'), features, checklist))
      .toContain('provider note for pi differs from source')
  })

  it('rejects a changed cell note', () => {
    expect(verifyCodingAgentSite(html.replace('<strong>value</strong>', '<strong>other</strong>'), features, checklist))
      .toContain('cell note for pi/mcp-input-request differs from source')
  })

  it('rejects a missing hover label', () => {
    expect(verifyCodingAgentSite(html.replace('<span class="provider-hover-label">Pi</span>', ''), features, checklist))
      .toContain('provider logo 1 has no hover label')
  })

  it('rejects a feature cell without a scoped row header', () => {
    const invalid = html.replace(
      '<th scope="row"><a href="#feature-mcp-input-request">MCP input request</a></th>',
      '<td><a href="#feature-mcp-input-request">MCP input request</a></td>',
    )
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 has no row header for mcp-input-request')
  })

  it('rejects a matrix table without an accessible name', () => {
    const invalid = html.replace(' aria-label="Coding agent feature matrix, part 1 of 1"', '')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 has no accessible name')
  })

  it('rejects a note link without a target', () => {
    const invalid = html.replace('<div id="note-pi-mcp-input-request">', '<div id="elsewhere">')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('matrix note link has no target: note-pi-mcp-input-request')
  })
})

describe('verifyCodingAgentSite support states', () => {
  it('rejects a cell that shows the symbol of another state', () => {
    const invalid = html.replace('aria-label="The agent does not offer it">🚫</span>', 'aria-label="The agent does not offer it">🚧</span>')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 has wrong support for pi/mcp-input-request')
  })

  it('rejects a cell whose label differs from its state', () => {
    const invalid = html.replace('aria-label="The agent does not offer it"', 'aria-label="LeapMux does not support it yet"')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 has wrong support for pi/mcp-input-request')
  })

  it('rejects a cell whose symbol has no image role and label', () => {
    const invalid = html.replace('<span role="img" aria-label="The agent does not offer it">🚫</span>', '🚫')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 has wrong support for pi/mcp-input-request')
  })

  it('rejects a cell that shows two symbols', () => {
    const invalid = html.replace('aria-label="The agent does not offer it">🚫</span>', 'aria-label="The agent does not offer it">🚫✅</span>')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 has wrong support for pi/mcp-input-request')
  })

  it('rejects a page without the legend', () => {
    const invalid = html.replace(/<ul class="feature-matrix-legend"[^>]*>[\s\S]*?<\/ul>/, '')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('the page contains no support legend')
  })

  it('rejects a legend entry that differs from its state', () => {
    const invalid = html.replace('<li><span aria-hidden="true">🚫</span> The agent does not offer it</li>', '<li><span aria-hidden="true">🚫</span> Not offered</li>')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('legend entry 2 reads "🚫 Not offered", expected "🚫 The agent does not offer it"')
  })

  it('rejects a legend with a missing or an extra entry', () => {
    const missing = html.replace('<li><span aria-hidden="true">🚧</span> LeapMux does not support it yet</li>', '')
    expect(verifyCodingAgentSite(missing, features, checklist))
      .toContain('expected 3 legend entries, found 2')
    const extra = html.replace('</ul>', '<li>Extra</li></ul>')
    expect(verifyCodingAgentSite(extra, features, checklist))
      .toContain('expected 3 legend entries, found 4')
  })

  it('rejects a legend symbol that screen readers announce twice', () => {
    const invalid = html.replace('<li><span aria-hidden="true">✅</span> Supported</li>', '<li><span>✅</span> Supported</li>')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('legend entry 1 must hide its symbol from assistive technology')
  })

  it('rejects a legend that lost its list role, because list-style none drops the role in Safari', () => {
    const invalid = html.replace('<ul class="feature-matrix-legend" role="list">', '<ul class="feature-matrix-legend">')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('the support legend must have the list role')
  })

  it('rejects a note label whose symbol screen readers announce as an emoji name', () => {
    const invalid = html.replace('<strong><span aria-hidden="true">🚫</span> Pi', '<strong>🚫 Pi')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('note label of pi/mcp-input-request must hide the symbol of its state from assistive technology')
  })

  it('rejects a note label that shows the symbol of another state', () => {
    const invalid = html.replace('<strong><span aria-hidden="true">🚫</span> Pi', '<strong><span aria-hidden="true">🚧</span> Pi')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('note label of pi/mcp-input-request must hide the symbol of its state from assistive technology')
  })

  it('rejects a table name that calls a part of the roster a group, because a group is a set of features', () => {
    const invalid = html.replace('feature matrix, part 1 of 1', 'feature matrix, group 1 of 1')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 names itself a group, but a group is a set of features')
  })
})

describe('verifyCodingAgentSite user notes', () => {
  it('rejects a cell with a user note and no note link', () => {
    const invalid = html.replace('<a href="#note-pi-mcp-input-request">†</a>', '')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 omits the cell note for pi/mcp-input-request')
  })

  it('rejects a note link for a cell that has no user note', () => {
    const second = withSecondGroup()
    const invalid = second.html.replace('<span role="img" aria-label="Supported">✅</span>', '<span role="img" aria-label="Supported">✅</span><a href="#note-pi-agent-questions">†</a>')
    expect(verifyCodingAgentSite(invalid, second.features, second.checklist))
      .toContain('table 1 links a note for pi/agent-questions that has no user note')
  })

  it('rejects a provider with a user note and no note link', () => {
    const invalid = html.replace('<a href="#note-provider-pi">†</a>', '')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 omits the provider note for pi')
  })

  it('rejects the detail note of a cell on the page', () => {
    expect(verifyCodingAgentSite(`${html}<p>${CELL_DETAIL}</p>`, features, checklist))
      .toContain('detail note of pi/mcp-input-request appears on the website')
  })

  it('rejects the detail note of a provider on the page', () => {
    expect(verifyCodingAgentSite(`${html}<p>${PROVIDER_DETAIL}</p>`, features, checklist))
      .toContain('detail note of provider pi appears on the website')
  })

  it('rejects the detail note of a hidden feature on the page', () => {
    const extended = withHiddenFeature()
    expect(verifyCodingAgentSite(`${html}<p>A test-only detail note that must stay off the website.</p>`, extended.features, extended.checklist))
      .toContain('detail note of pi/basic-chat appears on the website')
  })

  it('finds a detail note that the page breaks over lines or wraps in markup', () => {
    const wrapped = `${html}<p>Maintainer evidence that the\n  <em>website</em> must never show.</p>`
    expect(verifyCodingAgentSite(wrapped, features, checklist))
      .toContain('detail note of pi/mcp-input-request appears on the website')
  })

  it('accepts a detail note that equals its user note, because the page shows only the user note', () => {
    const same = structuredClone(checklist)
    same.cells['mcp-input-request'].pi.detailNote = same.cells['mcp-input-request'].pi.userNote
    expect(verifyCodingAgentSite(html, features, same)).toEqual([])
  })

  it('ignores a detail note shorter than 15 characters, which can occur by chance', () => {
    const short = structuredClone(checklist)
    short.cells['mcp-input-request'].pi.detailNote = 'Collects'
    expect(verifyCodingAgentSite(html, features, short)).toEqual([])
  })
})

describe('verifyCodingAgentSite detail note leaks', () => {
  const FLAG_SENTENCE = 'Version 1.2 of the agent ignores the `--flag` option, as the [upstream issue](https://pi.dev/issues/9) shows.'
  const FLAG_SENTENCE_RENDERED = 'Version 1.2 of the agent ignores the <code>--flag</code> option, as the <a href="https://pi.dev/issues/9">upstream issue</a> shows.'
  const PROTOCOL_SENTENCE = 'The maintainers checked the native protocol on 2026-10-05 and found no such field.'
  const SHORT_SENTENCE = 'The browser spec confirms the refusal.'
  const detailed = (detailNote, userNote) => {
    const next = structuredClone(checklist)
    next.cells['mcp-input-request'].pi.detailNote = detailNote
    if (userNote !== undefined)
      next.cells['mcp-input-request'].pi.userNote = userNote
    return next
  }
  const leak = 'detail note of pi/mcp-input-request appears on the website'
  /** Put content in the notes section, which is one of the elements that the shortcode renders. */
  const inMatrix = content => html.replace(/<\/section>\s*$/, `${content}</section>`)

  it('finds a detail note that the page renders as Markdown', () => {
    const next = detailed(`${FLAG_SENTENCE} ${PROTOCOL_SENTENCE} ${SHORT_SENTENCE}`)
    const rendered = inMatrix(`<p>${FLAG_SENTENCE_RENDERED} ${PROTOCOL_SENTENCE} ${SHORT_SENTENCE}</p>`)
    expect(verifyCodingAgentSite(rendered, features, next)).toContain(leak)
  })

  it('finds a detail note that the page prints as Markdown source', () => {
    const next = detailed(`${FLAG_SENTENCE} ${PROTOCOL_SENTENCE}`)
    expect(verifyCodingAgentSite(`${html}<pre>${FLAG_SENTENCE} ${PROTOCOL_SENTENCE}</pre>`, features, next)).toContain(leak)
  })

  it('finds a copy that misses the last sentence', () => {
    const next = detailed(`${FLAG_SENTENCE} ${PROTOCOL_SENTENCE} ${SHORT_SENTENCE}`)
    const partial = inMatrix(`<p>${FLAG_SENTENCE_RENDERED} ${PROTOCOL_SENTENCE}</p>`)
    expect(verifyCodingAgentSite(partial, features, next)).toContain(leak)
  })

  it('finds one sentence of at least 40 characters', () => {
    const next = detailed(`${FLAG_SENTENCE} ${PROTOCOL_SENTENCE} ${SHORT_SENTENCE}`)
    expect(verifyCodingAgentSite(inMatrix(`<p>${PROTOCOL_SENTENCE}</p>`), features, next)).toContain(leak)
  })

  it('finds a paragraph of short sentences that the page copies', () => {
    const paragraph = 'Short one here. Short two here. Short three here.'
    const next = detailed(`${PROTOCOL_SENTENCE}\n\n${paragraph}`)
    expect(verifyCodingAgentSite(inMatrix(`<p>${paragraph}</p>`), features, next)).toContain(leak)
  })

  it('ignores a sentence under 40 characters, which can occur by chance', () => {
    const next = detailed(`${PROTOCOL_SENTENCE} ${SHORT_SENTENCE}`)
    expect(verifyCodingAgentSite(inMatrix(`<p>${SHORT_SENTENCE}</p>`), features, next)).toEqual([])
  })

  it('accepts a detail sentence that the user note of the cell also holds, because the page shows that note', () => {
    const shared = 'The agent sends no compaction result, so LeapMux shows no notice.'
    const next = detailed(`${shared} ${PROTOCOL_SENTENCE}`, shared)
    const page = html.replace('Cell-specific <strong>value</strong>. See <a href="https://pi.dev/issues/1">the issue</a>.', shared)
    expect(verifyCodingAgentSite(page, features, next)).toEqual([])
  })

  it('accepts a detail sentence that the provider note holds', () => {
    const shared = 'The provider note states a long sentence that a cell detail note repeats.'
    const next = detailed(`${shared} ${PROTOCOL_SENTENCE}`)
    next.providerGroups[0][0].userNote = shared
    const page = html.replace('<p>Provider note.</p>', `<p>${shared}</p>`)
    expect(verifyCodingAgentSite(page, features, next)).toEqual([])
  })

  it('accepts a detail sentence that the hand-written docs prose around the matrix also holds', () => {
    const next = detailed(`${PROTOCOL_SENTENCE} ${SHORT_SENTENCE}`)
    expect(verifyCodingAgentSite(`${html}<ul><li>${PROTOCOL_SENTENCE}</li></ul>`, features, next)).toEqual([])
  })

  it('accepts a detail sentence that a feature definition also holds', () => {
    const next = detailed(`${PROTOCOL_SENTENCE} ${SHORT_SENTENCE}`)
    const described = structuredClone(features)
    described.features[0].description = PROTOCOL_SENTENCE
    const page = html.replace('<dd>Collects requested fields.</dd>', `<dd>${PROTOCOL_SENTENCE}</dd>`)
    expect(verifyCodingAgentSite(page, described, next)).toEqual([])
  })

  it('still finds a whole detail note in the docs prose around the matrix', () => {
    const next = detailed(`${PROTOCOL_SENTENCE} ${SHORT_SENTENCE}`)
    expect(verifyCodingAgentSite(`${html}<p>${PROTOCOL_SENTENCE} ${SHORT_SENTENCE}</p>`, features, next)).toContain(leak)
  })

  it('reports each leaking note once', () => {
    const next = detailed(`${PROTOCOL_SENTENCE} ${FLAG_SENTENCE}`)
    const leaking = inMatrix(`<p>${PROTOCOL_SENTENCE}</p><p>${FLAG_SENTENCE_RENDERED}</p>`)
    expect(verifyCodingAgentSite(leaking, features, next).filter(error => error === leak)).toHaveLength(1)
  })
})

describe('verifyCodingAgentSite feature groups', () => {
  it('accepts a group header on each group, with logos on every group after the first', () => {
    const second = withSecondGroup()
    expect(verifyCodingAgentSite(second.html, second.features, second.checklist)).toEqual([])
  })

  it('rejects a table with a missing group body', () => {
    const second = withSecondGroup()
    expect(verifyCodingAgentSite(html.replace('</dl>', '<dt id="feature-agent-questions">Agent questions</dt><dd>Asks the user.</dd></dl>'), second.features, second.checklist))
      .toContain('table 1 has 1 feature groups, expected 2')
  })

  it('rejects a group header that is not a row group header', () => {
    const invalid = html.replace('<th scope="rowgroup" colspan="2">Tools</th>', '<td colspan="2">Tools</td>')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 group 1 has no row group header')
  })

  it('rejects a wrong group name', () => {
    const invalid = html.replace('>Tools</th>', '>Utilities</th>')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 group 1 is named "Utilities", expected "Tools"')
  })

  it('rejects a first group header that does not span the table', () => {
    const invalid = html.replace('colspan="2"', 'colspan="1"')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 group 1 header spans 1 columns, expected 2')
  })

  it('rejects logos on the first group header, because the table head shows them', () => {
    const invalid = html.replace('<th scope="rowgroup" colspan="2">Tools</th>', '<th scope="rowgroup">Tools</th><td aria-hidden="true"><img src="/icons/agents/pi.svg" alt=""></td>')
    expect(verifyCodingAgentSite(invalid, features, checklist))
      .toContain('table 1 group 1 header must not repeat the provider logos')
  })

  it('rejects a repeated header with too few logos', () => {
    const second = withSecondGroup()
    const invalid = second.html.replace(/<td class="feature-group-logo"[\s\S]*?<\/span><\/span><\/td>/, '')
    expect(verifyCodingAgentSite(invalid, second.features, second.checklist))
      .toContain('table 1 group 2 header has 0 logos, expected 1')
  })

  it('rejects a repeated header logo that is a link, because the table head owns the links', () => {
    const second = withSecondGroup()
    const invalid = second.html.replace('<span class="provider-link"><img src="/icons/agents/pi.svg" alt="">', '<a class="provider-link" href="https://pi.dev/"><img src="/icons/agents/pi.svg" alt="">')
      .replace('Pi</span></span></td></tr>', 'Pi</span></a></td></tr>')
    expect(verifyCodingAgentSite(invalid, second.features, second.checklist))
      .toContain('table 1 group 2 header logo 1 must not be a link')
  })

  it('rejects a repeated header logo with alternative text, which would announce the provider twice', () => {
    const second = withSecondGroup()
    const invalid = second.html.replace('<img src="/icons/agents/pi.svg" alt=""><span class="provider-hover-label">Pi</span></span></td></tr>', '<img src="/icons/agents/pi.svg" alt="Pi"><span class="provider-hover-label">Pi</span></span></td></tr>')
    expect(verifyCodingAgentSite(invalid, second.features, second.checklist))
      .toContain('table 1 group 2 header logo 1 must have an empty alt')
  })

  it('rejects a repeated header cell that screen readers can reach', () => {
    const second = withSecondGroup()
    const invalid = second.html.replace('<td class="feature-group-logo" aria-hidden="true">', '<td class="feature-group-logo">')
    expect(verifyCodingAgentSite(invalid, second.features, second.checklist))
      .toContain('table 1 group 2 header logo 1 must be hidden from assistive technology')
  })

  it('rejects a repeated header logo with the wrong icon or hover label', () => {
    const second = withSecondGroup()
    expect(verifyCodingAgentSite(second.html.replace('<img src="/icons/agents/pi.svg" alt=""><span class="provider-hover-label">Pi</span></span></td></tr>', '<img src="/icons/agents/other.svg" alt=""><span class="provider-hover-label">Pi</span></span></td></tr>'), second.features, second.checklist))
      .toContain('table 1 group 2 header logo 1 has the wrong icon')
    expect(verifyCodingAgentSite(second.html.replace('alt=""><span class="provider-hover-label">Pi</span></span></td></tr>', 'alt=""><span class="provider-hover-label">Other</span></span></td></tr>'), second.features, second.checklist))
      .toContain('table 1 group 2 header logo 1 has the wrong hover label')
  })

  it('rejects a feature row under the wrong group', () => {
    const second = withSecondGroup()
    const row = '<tr><th scope="row"><a href="#feature-agent-questions">Agent questions</a></th><td><span role="img" aria-label="Supported">✅</span></td></tr>'
    const invalid = second.html.replace(row, '').replace('</tbody>', `${row}</tbody>`)
    expect(verifyCodingAgentSite(invalid, second.features, second.checklist))
      .toContain('table 1 group 1 holds rows [mcp-input-request, agent-questions], expected [mcp-input-request]')
  })
})

describe('verifyCodingAgentProviderCount', () => {
  it('accepts a count without a feature table', () => {
    expect(verifyCodingAgentProviderCount('<span data-agent-provider-count>1</span>', checklist)).toEqual([])
  })

  it('counts providers from every source group', () => {
    const extended = {
      providerGroups: [
        [
          { id: 'a' },
        ],
        [
          { id: 'b' },
          { id: 'c' },
        ],
      ],
    }
    expect(verifyCodingAgentProviderCount('<span data-agent-provider-count>3</span>', extended)).toEqual([])
    expect(verifyCodingAgentProviderCount('<span data-agent-provider-count>1</span>', extended))
      .toContain('provider count 1 is "1", expected 3')
  })

  it('rejects a page without the generated count', () => {
    expect(verifyCodingAgentProviderCount('<p>Three providers.</p>', checklist))
      .toContain('the page contains no generated provider count')
  })
})

describe('verifyCodingAgentTicker', () => {
  const tickerHtml = `
    <div class="agent-ticker" role="region" aria-label="Supported coding agents" style="--agent-ticker-count: 1;">
      <div class="agent-ticker-viewport">
        <div class="agent-ticker-track">
          <ul class="agent-ticker-list" role="list">
            <li class="agent-ticker-item">
              <a class="agent-ticker-link" href="https://pi.dev/" title="Pi">
                <img src="/icons/agents/pi.svg" width="24" height="24" alt="Pi">
              </a>
            </li>
          </ul>
          <ul class="agent-ticker-list" role="list" aria-hidden="true">
            <li class="agent-ticker-item">
              <a class="agent-ticker-link" href="https://pi.dev/" tabindex="-1" title="Pi">
                <img src="/icons/agents/pi.svg" width="24" height="24" alt="">
              </a>
            </li>
          </ul>
        </div>
      </div>
    </div>
  `

  it('accepts a dynamic ticker matching every source provider', () => {
    expect(verifyCodingAgentTicker(tickerHtml, checklist)).toEqual([])
  })

  it('rejects a page without a ticker', () => {
    expect(verifyCodingAgentTicker('<h1>Hello</h1>', checklist))
      .toContain('the page contains no coding-agent ticker')
  })

  it('rejects a ticker with a mismatched count CSS variable', () => {
    expect(verifyCodingAgentTicker(tickerHtml.replace('--agent-ticker-count: 1;', '--agent-ticker-count: 99;'), checklist))
      .toContain('ticker count property is "99", expected "1"')
  })

  it('rejects a ticker with a missing or mismatched provider', () => {
    expect(verifyCodingAgentTicker(tickerHtml.replace('src="/icons/agents/pi.svg" width="24" height="24" alt="Pi"', 'src="/icons/agents/other.svg" width="24" height="24" alt="Pi"'), checklist))
      .toContain('ticker provider 1 differs from pi')
    expect(verifyCodingAgentTicker(tickerHtml.replace('href="https://pi.dev/" title="Pi"', 'href="https://example.com/" title="Pi"'), checklist))
      .toContain('ticker provider 1 has the wrong URL')
  })

  it('accepts a dynamic ticker with light/dark icon pairs', () => {
    const themedHtml = tickerHtml
      .replace('<img src="/icons/agents/pi.svg" width="24" height="24" alt="Pi">', '<img class="agent-ticker-icon-light" src="/icons/agents/pi.svg" width="24" height="24" alt="Pi"><img class="agent-ticker-icon-dark" src="/icons/agents/dark/pi.svg" width="24" height="24" alt="Pi">')
      .replace('<img src="/icons/agents/pi.svg" width="24" height="24" alt="">', '<img class="agent-ticker-icon-light" src="/icons/agents/pi.svg" width="24" height="24" alt=""><img class="agent-ticker-icon-dark" src="/icons/agents/dark/pi.svg" width="24" height="24" alt="">')
    expect(verifyCodingAgentTicker(themedHtml, checklist)).toEqual([])
    expect(verifyCodingAgentTicker(themedHtml.replace('/icons/agents/dark/pi.svg', '/icons/agents/dark/wrong.svg'), checklist))
      .toContain('ticker provider 1 has a mismatched dark icon')
  })

  it('rejects an accessible or focusable duplicate loop track', () => {
    expect(verifyCodingAgentTicker(tickerHtml.replace('aria-hidden="true"', ''), checklist))
      .toContain('duplicate ticker track must be hidden from assistive technology')
    expect(verifyCodingAgentTicker(tickerHtml.replace('tabindex="-1"', ''), checklist))
      .toContain('duplicate ticker provider 1 must not be keyboard focusable')
    expect(verifyCodingAgentTicker(tickerHtml.replace('alt=""', 'alt="Pi"'), checklist))
      .toContain('duplicate ticker provider 1 must have an empty alt')
  })
})

describe('findOrphanH2Headings', () => {
  it('flags an H2 heading followed directly by another H2 heading or EOF', () => {
    const md = [
      '# Title',
      '',
      '## Empty section',
      '',
      '## Populated section',
      '',
      'Some text.',
      '',
      '## Trailing empty',
    ].join('\n')

    expect(findOrphanH2Headings(md)).toEqual([
      { line: 3, heading: '## Empty section' },
      { line: 9, heading: '## Trailing empty' },
    ])
  })

  it('allows an H2 heading followed by an H3 subsection and ignores headings inside fenced code blocks', () => {
    const md = [
      '## Run your first parallel session',
      '',
      '### Launch LeapMux in solo mode',
      '',
      '```bash',
      '## comment line one',
      '## comment line two',
      '```',
    ].join('\n')

    expect(findOrphanH2Headings(md)).toEqual([])
  })
})

describe('verifyBuiltSiteArtifacts', () => {
  function createMockConfigReference() {
    const metrics = Array.from({ length: 21 }, (_, index) => ({
      name: `leapmux_metric_${index + 1}`,
      type: 'Counter',
      labels: [],
      meaning: `Metric ${index + 1}.`,
      group: 'HTTP and RPC metrics',
    }))
    return {
      schema_version: 1,
      source: '/docs/admin/configuration-reference/',
      sections: [
        {
          id: 'hub-configuration',
          title: 'Hub configuration',
          groups: [
            {
              id: 'server-options',
              title: 'Server options',
              columns: ['config_key'],
              entries: [
                { config_key: 'listen' },
              ],
            },
          ],
        },
        {
          id: 'worker-configuration',
          title: 'Worker configuration',
          groups: [
            {
              id: 'worker-options',
              title: 'Worker options',
              columns: ['config_key'],
              entries: [
                { config_key: 'name' },
              ],
            },
          ],
        },
        {
          id: 'storage-backends',
          title: 'Storage backends',
          groups: [
            {
              id: 'sqlite-default',
              title: 'SQLite (default)',
              columns: ['config_key'],
              entries: [
                { config_key: 'storage.sqlite.path' },
              ],
            },
          ],
        },
        {
          id: 'prometheus-metrics',
          title: 'Prometheus metrics',
          groups: [
            {
              id: 'http-and-rpc-metrics',
              title: 'HTTP and RPC metrics',
              columns: ['metric'],
              entries: [
                { metric: 'leapmux_metric_1' },
              ],
            },
          ],
        },
      ],
      metrics,
    }
  }

  function createMockPublicDir() {
    const dir = mkdtempSync(join(tmpdir(), 'leapmux-site-artifacts-'))
    const dirs = [
      'docs/getting-started/quick-start',
      'docs/getting-started/concepts',
      'docs/admin/configuration',
      'docs/admin/encryption-and-data',
      'docs/admin/recover',
      'docs/admin/reverse-proxy',
      'docs/admin/starting-and-stopping',
      'docs/recipes/parallel-agents',
      'docs/reference/faq',
      'docs/reference/supported-agents',
      'docs/reference/commands',
      'download',
      'changelog',
      'pagefind',
      'vendor/asciinema-player',
    ]
    for (const relDir of dirs)
      mkdirSync(join(dir, relDir), { recursive: true })

    writeFileSync(join(dir, 'docs/getting-started/quick-start/index.md'), [
      '# Quick Start',
      '',
      '## Run your first parallel session',
      '',
      '### Launch LeapMux in solo mode',
      '### Create a workspace and start your first agent',
      '### Open a second agent and a shell terminal side by side',
      '### Send prompts, tune settings, and inspect diffs',
      '',
      'Done.',
    ].join('\n'))
    writeFileSync(join(dir, 'docs/admin/encryption-and-data/index.md'), [
      '# Encryption & Data',
      '',
      '### Generate a new active key version',
      '### Restart the Hub',
      '### Re-encrypt existing persistent rows',
      '### Remove the retired key version (optional)',
      '### Stop the Hub and affected Workers',
      '### Restore the database and matching `encryption.key`',
      '### Start the Hub and verify Workers',
    ].join('\n'))
    writeFileSync(join(dir, 'docs/admin/reverse-proxy/index.md'), [
      '# Reverse Proxy',
      '',
      '### Bind the Hub to loopback',
      '### Set the public URL, secure cookies, and trusted proxies',
      '### Register Workers against the external HTTPS URL',
      '### Verify HTTPS, HTTP/2, and `/metrics` protection',
      '### Register and start a Worker through the proxy',
      '',
      '```nginx',
      'proxy_set_header Upgrade $http_upgrade;',
      'proxy_set_header Host $host;',
      'return 301 https://$host$request_uri;',
      'proxy_set_header X-Forwarded-Proto $scheme;',
      '```',
    ].join('\n'))
    writeFileSync(join(dir, 'docs/admin/recover/index.md'), [
      '# Recovery',
      '',
      '> **Warning:** First paragraph.',
      '>',
      '> Passing the top-level directory creates an orphan database.',
    ].join('\n'))
    writeFileSync(join(dir, 'download/index.md'), '# Download LeapMux\n\nClean markdown.\n')
    writeFileSync(join(dir, 'changelog/index.md'), '# Changelog\n\n## 2026-11-01 — Release\n')
    writeFileSync(join(dir, 'docs/admin/starting-and-stopping/index.md'), [
      '# Starting and Stopping',
      '',
      '### Stop and disable the old service',
      '### Move or restore the directory',
      '### Start the instance at the new path',
    ].join('\n'))
    writeFileSync(join(dir, 'docs/reference/faq/index.md'), '# FAQ\n\nSupported: Pi\n')
    writeFileSync(join(dir, 'docs/reference/supported-agents/index.md'), '# Supported Agents\n\n### Coding agent feature matrix\n')
    writeFileSync(join(dir, 'docs/reference/commands/index.md'), '# Commands\n\nUse <workspace>.\n')
    writeFileSync(join(dir, 'llms.txt'), '# LeapMux\n- [Command Reference](https://leapmux.dev/docs/reference/commands/index.md)\n- [Changelog (Overview)](https://leapmux.dev/changelog/index.md)\n')
    writeFileSync(join(dir, 'llms-small.txt'), '# LeapMux (Core Index)\n- [Concepts & Topologies](https://leapmux.dev/docs/getting-started/concepts/index.md)\n')
    writeFileSync(join(dir, 'llms-full.txt'), [
      '# LeapMux — Full Documentation Guides',
      '# Quick Start',
      '# Configuration Guide',
      '# Configuration Reference',
      '# Security & Authentication',
      '# Troubleshooting',
      'proxy_set_header Upgrade $http_upgrade;',
    ].join('\n\n'))
    writeFileSync(join(dir, 'config-reference.json'), JSON.stringify(createMockConfigReference()))
    writeFileSync(join(dir, 'index.html'), [
      '<meta property="og:image:alt" content="LeapMux">',
      '<meta property="og:locale" content="en_US">',
      '<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/hack-font@3.3.0/build/web/hack.css" integrity="sha384-test" crossorigin="anonymous">',
      '<style>.lm-cli-cast-mount .ap-player.asciinema-player-theme-leapmux{--term-color-foreground:rgb(34 32 30);--term-color-15:#b8bdbe;}</style>',
      '<video muted loop playsinline preload="none"></video>',
    ].join('\n'))
    writeFileSync(join(dir, 'docs/admin/index.html'), '<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"CollectionPage"},{"@type":"BreadcrumbList"}]}</script>')
    writeFileSync(join(dir, 'docs/getting-started/concepts/index.html'), '<script src="https://cdn.jsdelivr.net/npm/mermaid@11.12.2/dist/mermaid.min.js" integrity="sha384-test" crossorigin="anonymous"></script>')
    writeFileSync(join(dir, '_headers'), '/*.txt\n  Content-Type: text/plain; charset=utf-8\n\n/*.md\n  Content-Type: text/markdown; charset=utf-8\n')
    writeFileSync(join(dir, 'docs/admin/configuration/index.html'), '<meta property="article:section" content="Administration">')
    writeFileSync(join(dir, 'docs/recipes/parallel-agents/index.html'), '<meta property="article:section" content="Workflow Recipes">')
    writeFileSync(join(dir, 'pagefind/pagefind.js'), '// pagefind')
    writeFileSync(join(dir, 'vendor/asciinema-player/asciinema-player.min.js'), '// asciinema')
    writeFileSync(join(dir, 'vendor/asciinema-player/asciinema-player.css'), '/* asciinema */')
    return dir
  }

  it('accepts clean Markdown companions, llms.txt, llms-full.txt, config-reference.json, OG metadata, and pruned Pagefind output', () => {
    const dir = createMockPublicDir()
    try {
      expect(verifyBuiltSiteArtifacts(dir, checklist)).toEqual([])
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })

  it('rejects unexpanded shortcodes, leaked comments, escaped entities, duplicate OG alt, and FlexSearch JSON', () => {
    const dir = createMockPublicDir()
    try {
      writeFileSync(join(dir, 'docs/reference/commands/index.md'), '<!-- The command tree generates -->\n{{< card >}} &lt;id&gt;\n')
      writeFileSync(join(dir, 'index.html'), '<meta property="og:image:alt" content="LeapMux — LeapMux">')
      writeFileSync(join(dir, 'en.search-data.json'), '{}')
      const errors = verifyBuiltSiteArtifacts(dir, checklist)
      expect(errors).toContain('docs/reference/commands/index.md: contains unexpanded Hugo shortcode')
      expect(errors).toContain('docs/reference/commands/index.md: leaks internal generated-file HTML comment')
      expect(errors).toContain('docs/reference/commands/index.md: contains unescaped HTML angle-bracket entities')
      expect(errors).toContain('index.html: og:image:alt duplicates site title')
      expect(errors).toContain('site/public/en.search-data.json: monolithic FlexSearch JSON should not be emitted when Pagefind is enabled')
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })

  it('rejects erased step headings and orphan empty H2 headings', () => {
    const dir = createMockPublicDir()
    try {
      writeFileSync(join(dir, 'docs/getting-started/quick-start/index.md'), '# Quick Start\n\n## Run your first parallel session\n\n## Where to go next\n\nDone.\n')
      const errors = verifyBuiltSiteArtifacts(dir, checklist)
      expect(errors).toContain('docs/getting-started/quick-start/index.md: orphan empty heading "## Run your first parallel session" at line 3')
      expect(errors).toContain('docs/getting-started/quick-start/index.md: missing expected step heading "### Launch LeapMux in solo mode"')
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })

  it('rejects invalid llms-full.txt, malformed config-reference.json, unpruned Pagefind UI assets, and wrong article:section metadata', () => {
    const dir = createMockPublicDir()
    try {
      writeFileSync(join(dir, 'llms-full.txt'), '# Quick Start\n{{% steps %}}\n')
      writeFileSync(join(dir, 'config-reference.json'), JSON.stringify({ schema_version: 1, sections: [], metrics: [] }))
      writeFileSync(join(dir, 'pagefind/pagefind-ui.js'), '// unused stock UI')
      writeFileSync(join(dir, 'docs/admin/configuration/index.html'), '<meta property="article:section" content="Documentation">')
      writeFileSync(join(dir, 'docs/recipes/parallel-agents/index.html'), '<meta property="article:section" content="Documentation">')
      const errors = verifyBuiltSiteArtifacts(dir, checklist)
      expect(errors).toContain('llms-full.txt: contains unexpanded Hugo shortcode')
      expect(errors).toContain('llms-full.txt: missing core guide heading "# Troubleshooting"')
      expect(errors).toContain('config-reference.json: missing populated section "hub-configuration"')
      expect(errors).toContain('config-reference.json: found 0 unique leapmux_ metrics (0 total), expected 21')
      expect(errors).toContain('site/public/pagefind/pagefind-ui.js: unused Pagefind stock UI asset should be pruned')
      expect(errors).toContain('docs/admin/configuration/index.html: article:section is "Documentation", expected "Administration"')
      expect(errors).toContain('docs/recipes/parallel-agents/index.html: article:section is "Documentation", expected "Workflow Recipes"')
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })

  it('rejects mangled nginx variables, broken callouts, raw HTML in Markdown, escaped entities in llms.txt, overlapping _headers, missing SRI, autoplay video, and wrong section JSON-LD', () => {
    const dir = createMockPublicDir()
    try {
      writeFileSync(join(dir, 'docs/admin/reverse-proxy/index.md'), '# Reverse Proxy\n\n### Bind the Hub to loopback\n### Set the public URL, secure cookies, and trusted proxies\n### Register Workers against the external HTTPS URL\n### Verify HTTPS, HTTP/2, and `/metrics` protection\n### Register and start a Worker through the proxy\n\nproxy_set_header Upgrade ;\n')
      writeFileSync(join(dir, 'docs/admin/recover/index.md'), '# Recovery\n\n> **Warning:** First paragraph.\n\nPassing the top-level directory.\n')
      writeFileSync(join(dir, 'download/index.md'), '# Download\n<div class="hero"><script>alert(1)</script></div>\n')
      writeFileSync(join(dir, 'llms.txt'), '# LeapMux\n- [Concepts &amp; Topologies](https://leapmux.dev/docs/reference/commands/index.md)\n')
      writeFileSync(join(dir, 'llms-small.txt'), '# Core\n- [It&#39;s escaped](https://leapmux.dev/)\n')
      writeFileSync(join(dir, 'llms-full.txt'), '# Quick Start\n# Control Command Reference\n')
      writeFileSync(join(dir, 'index.html'), [
        '<meta property="og:image:alt" content="LeapMux">',
        '<meta property="og:locale" content="en">',
        '<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/hack-font@3.3.0/build/web/hack.css">',
        '<video autoplay muted loop playsinline></video>',
      ].join('\n'))
      writeFileSync(join(dir, 'docs/admin/index.html'), '<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"TechArticle"},{"@type":"BreadcrumbList"}]}</script>')
      mkdirSync(join(dir, 'docs/admin/security'), { recursive: true })
      writeFileSync(join(dir, 'docs/admin/security/index.html'), '<meta http-equiv="refresh" content="0; url=https://leapmux.dev/docs/admin/security-and-auth/">')
      writeFileSync(join(dir, 'docs/getting-started/concepts/index.html'), '<script src="https://cdn.jsdelivr.net/npm/mermaid@11.12.2/dist/mermaid.min.js"></script>')
      writeFileSync(join(dir, '_headers'), '/*.md\n  Content-Type: text/markdown\n  Vary: Accept\n\n/skills/*\n  Content-Type: text/markdown\n')
      const errors = verifyBuiltSiteArtifacts(dir, checklist)
      expect(errors).toContain('docs/admin/reverse-proxy/index.md: missing nginx variable $http_upgrade')
      expect(errors).toContain('docs/admin/recover/index.md: multi-paragraph callout lost blockquote prefix on continuation paragraph')
      expect(errors).toContain('download/index.md: leaks raw <script> tag')
      expect(errors).toContain('download/index.md: leaks raw <div> tag')
      expect(errors).toContain('llms.txt: missing /changelog/index.md entry')
      expect(errors).toContain('llms.txt: contains HTML-escaped entities')
      expect(errors).toContain('llms-small.txt: contains HTML-escaped entities')
      expect(errors).toContain('llms-full.txt: must exclude Control Command Reference (llms_full: false)')
      expect(errors).toContain('index.html: og:locale is "en", expected "en_US"')
      expect(errors).toContain('index.html: contains <video autoplay> attribute; video playback must be gated by JS')
      expect(errors).toContain('index.html: missing generated asciinema-player terminal theme from contracts/theme-default.json')
      expect(errors).toContain('index.html: external CDN asset https://cdn.jsdelivr.net/npm/hack-font@3.3.0/build/web/hack.css lacks sha384 integrity')
      expect(errors).toContain('docs/admin/index.html: JSON-LD @type is "TechArticle", expected "CollectionPage"')
      expect(errors).toContain('docs/admin/security/index.html: contains meta-refresh redirect (Hugo alias); pre-release docs must not keep old URLs working')
      expect(errors).toContain('docs/getting-started/concepts/index.html: external CDN asset https://cdn.jsdelivr.net/npm/mermaid@11.12.2/dist/mermaid.min.js lacks sha384 integrity')
      expect(errors).toContain('_headers: must define disjoint /*.txt and /*.md rules')
      expect(errors).toContain('_headers: contains overlapping /skills/* or /agent-guide.md rule that duplicates Content-Type on Cloudflare')
      expect(errors).toContain('_headers: must not emit Vary: Accept for static assets without content negotiation')
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })

  it('rejects duplicate H1 in download/index.md and broken backticks in download, reverse-proxy, and glossary HTML', () => {
    const dir = createMockPublicDir()
    try {
      mkdirSync(join(dir, 'docs/reference/glossary'), { recursive: true })
      writeFileSync(join(dir, 'download/index.md'), '# Download LeapMux\n\n# Download LeapMux\n')
      writeFileSync(join(dir, 'download/index.html'), '<p id="lm-download-desc">macOS (`.dmg`)</p>')
      writeFileSync(join(dir, 'docs/admin/reverse-proxy/index.html'), '<td><code>!Path(\\</code>/metrics`)</td>')
      writeFileSync(join(dir, 'docs/reference/glossary/index.html'), '<p><code>Ctrl+\\`` (or </code>Ctrl)</p>')
      const errors = verifyBuiltSiteArtifacts(dir, checklist)
      expect(errors).toContain('download/index.md: duplicates top-level H1 "Download LeapMux" at line 3')
      expect(errors).toContain('download/index.html: #lm-download-desc contains unrendered Markdown backticks')
      expect(errors).toContain('docs/admin/reverse-proxy/index.html: broken backslash-escaped backtick code span in Traefik row')
      expect(errors).toContain('docs/reference/glossary/index.html: broken backslash-escaped backtick code span in Quake-mode terminal')
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })

  it('uses CSS subgrid instead of display: contents on the hero h1', () => {
    const customCss = readFileSync(resolve(import.meta.dirname, '../site/assets/css/custom.css'), 'utf8')
    expect(customCss).not.toMatch(/\.hero-headline-with-ticker\s+h1\s*\{[^}]*display\s*:\s*contents\b/)
    expect(customCss).toMatch(/\.hero-headline-with-ticker\s+h1\s*\{[^}]*grid-template-columns\s*:\s*subgrid\b[^}]+grid-template-rows\s*:\s*subgrid\b/)
    expect(customCss).not.toContain('--term-color-')
  })

  it('rejects weak SRI hashes, missing crossorigin, malformed JSON-LD, missing nginx vars in llms-full.txt, and missing asciinema vendor assets', () => {
    const dir = createMockPublicDir()
    try {
      writeFileSync(join(dir, 'index.html'), [
        '<meta property="og:image:alt" content="LeapMux">',
        '<meta property="og:locale" content="en_US">',
        '<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/hack-font@3.3.0/build/web/hack.css" integrity="sha256-weak">',
        '<style>.lm-cli-cast-mount .ap-player.asciinema-player-theme-leapmux{--term-color-15:#b8bdbe;}</style>',
      ].join('\n'))
      writeFileSync(join(dir, 'docs/admin/index.html'), '<script type="application/ld+json">{broken json</script>')
      writeFileSync(join(dir, 'llms-full.txt'), '# Quick Start\n# Configuration Guide\n# Configuration Reference\n# Security & Authentication\n# Troubleshooting\n')
      rmSync(join(dir, 'vendor/asciinema-player/asciinema-player.min.js'))
      const errors = verifyBuiltSiteArtifacts(dir, checklist)
      expect(errors).toContain('index.html: external CDN asset https://cdn.jsdelivr.net/npm/hack-font@3.3.0/build/web/hack.css lacks sha384 integrity')
      expect(errors).toContain('index.html: external CDN asset https://cdn.jsdelivr.net/npm/hack-font@3.3.0/build/web/hack.css lacks crossorigin="anonymous"')
      expect(errors).toContain('docs/admin/index.html: JSON-LD @type is null, expected "CollectionPage"')
      expect(errors).toContain('llms-full.txt: missing nginx variable $http_upgrade')
      expect(errors).toContain('site/public/vendor/asciinema-player/asciinema-player.min.js: missing locked asciinema-player bundle asset')
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})

describe('table-filter.js behavior', () => {
  const tableFilterSource = readFileSync(resolve(import.meta.dirname, '../site/assets/js/table-filter.js'), 'utf8')

  function mountTableFilter(bodyHtml, url = 'https://leapmux.dev/docs/test/') {
    const dom = new JSDOM(`<!doctype html><html><body>${bodyHtml}</body></html>`, {
      url,
      runScripts: 'dangerously',
    })
    dom.window.history.replaceState({ kept: 42 }, '', url)
    dom.window.eval(tableFilterSource)
    return dom
  }

  function widgetMarkup(attrs = '') {
    return `<div class="lm-table-filter" data-table-filter ${attrs}>
      <div class="lm-table-filter-bar">
        <input class="lm-table-filter-input" type="search">
        <button class="lm-table-filter-clear" type="button" hidden>Clear</button>
        <span class="lm-table-filter-count"></span>
      </div>
      <div class="lm-table-filter-pills"></div>
      <div class="lm-table-filter-empty" hidden>No matches</div>
    </div>`
  }

  it('filters rows, toggles category pills, preserves sibling prose wrappers, and keeps history.state on reset', () => {
    const dom = mountTableFilter(`
      <main class="content">
        ${widgetMarkup()}
        <h2>Hub</h2>
        <div class="prose-and-table">
          <p class="keep-prose">Important note beside table.</p>
          <table><tbody>
            <tr><td>listen</td><td>127.0.0.1:4327</td></tr>
            <tr><td>storage</td><td>sqlite</td></tr>
          </tbody></table>
        </div>
        <h2>Worker</h2>
        <div class="table-only">
          <table><tbody>
            <tr><td>hub_url</td><td>https://hub.example</td></tr>
          </tbody></table>
        </div>
      </main>
    `)
    try {
      const { document, Event, window } = dom.window
      const input = document.querySelector('.lm-table-filter-input')
      const clearBtn = document.querySelector('.lm-table-filter-clear')
      const countEl = document.querySelector('.lm-table-filter-count')
      const emptyEl = document.querySelector('.lm-table-filter-empty')
      const pills = Array.from(document.querySelectorAll('.lm-table-filter-pill'))

      expect(countEl.textContent).toBe('Showing 3 of 3 entries')
      expect(pills.map(p => p.textContent)).toEqual(['All', 'Hub', 'Worker'])

      // Filter to a row in Worker: Hub's wrapper with sibling prose must not be hidden.
      input.value = 'hub_url'
      input.dispatchEvent(new Event('input'))
      expect(countEl.textContent).toBe('Showing 1 of 3 entries')
      expect(emptyEl.hidden).toBe(true)
      expect(document.querySelector('.prose-and-table').hidden).toBe(false)

      // Zero matches shows the empty state.
      input.value = 'nonexistent-key'
      input.dispatchEvent(new Event('input'))
      expect(countEl.textContent).toBe('Showing 0 of 3 entries')
      expect(emptyEl.hidden).toBe(false)

      // Clear resets rows immediately and preserves history.state.
      clearBtn.click()
      expect(countEl.textContent).toBe('Showing 3 of 3 entries')
      expect(window.history.state).toEqual({ kept: 42 })

      // Clicking a category pill filters to that section and toggles aria-pressed.
      pills[1].click()
      expect(pills[1].getAttribute('aria-pressed')).toBe('true')
      expect(countEl.textContent).toBe('Showing 2 of 3 entries')
    }
    finally {
      dom.window.close()
    }
  })

  it('hides widgets with an invalid scope selector without breaking sibling widgets and isolates URL params', () => {
    const dom = mountTableFilter(`
      <main class="content">
        ${widgetMarkup('data-table-filter-scope="[invalid"')}
        ${widgetMarkup('id="w2"')}
        <table><tbody><tr><td>alpha</td></tr><tr><td>beta</td></tr></tbody></table>
      </main>
    `, 'https://leapmux.dev/docs/test/?q2=alpha')
    try {
      const { document } = dom.window
      const [badWidget, goodWidget] = document.querySelectorAll('[data-table-filter]')
      expect(badWidget.hidden).toBe(true)
      expect(goodWidget.hidden).toBe(false)
      expect(goodWidget.querySelector('.lm-table-filter-input').value).toBe('alpha')
      expect(goodWidget.querySelector('.lm-table-filter-count').textContent).toBe('Showing 1 of 2 entries')
    }
    finally {
      dom.window.close()
    }
  })
})

describe('docs content regression guards', () => {
  it('detects duplicate top-level H1 headings outside code fences', () => {
    expect(findDuplicateTopH1('# Download LeapMux\n\n# Download LeapMux\n')).toEqual([
      { line: 3, heading: 'Download LeapMux' },
    ])
    expect(findDuplicateTopH1('# Title\n\n```md\n# Title\n```\n\n~~~md\n# Title\n~~~\n')).toEqual([])
  })

  it('verifies shipped reverse-proxy sample configs strip Forwarded and decode HAProxy /metrics', () => {
    const dir = resolve(import.meta.dirname, '../site/content/docs/admin/reverse-proxy')
    expect(verifyReverseProxySamples(dir)).toEqual([])
  })

  it('rejects reverse-proxy configs that omit Forwarded stripping, append X-Forwarded-For, or skip HAProxy url_dec', () => {
    const dir = mkdtempSync(join(tmpdir(), 'leapmux-proxy-test-'))
    try {
      writeFileSync(join(dir, 'nginx.conf'), 'proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n')
      writeFileSync(join(dir, 'haproxy.cfg'), 'option forwardfor\nhttp-request deny deny_status 404 if { path /metrics }\n')
      writeFileSync(join(dir, 'apache.conf'), 'RequestHeader set X-Forwarded-Proto "https"\n')
      writeFileSync(join(dir, 'Caddyfile'), 'reverse_proxy 127.0.0.1:4327\n')
      writeFileSync(join(dir, 'traefik-dynamic.yml'), 'http:\n  routers:\n    leapmux:\n      service: leapmux\n')
      const errors = verifyReverseProxySamples(dir)
      expect(errors).toContain('reverse-proxy/nginx.conf: must strip client-supplied Forwarded header')
      expect(errors).toContain('reverse-proxy/nginx.conf: must set X-Forwarded-For from $remote_addr instead of $proxy_add_x_forwarded_for')
      expect(errors).toContain('reverse-proxy/haproxy.cfg: must strip client-supplied Forwarded header')
      expect(errors).toContain('reverse-proxy/haproxy.cfg: must set X-Forwarded-For from %[src] instead of appending with option forwardfor')
      expect(errors).toContain('reverse-proxy/haproxy.cfg: must decode percent-encoded paths (path,url_dec /metrics) before denying /metrics')
      expect(errors).toContain('reverse-proxy/apache.conf: must unset client-supplied Forwarded header')
      expect(errors).toContain('reverse-proxy/apache.conf: must unset client-supplied X-Forwarded-For header')
      expect(errors).toContain('reverse-proxy/Caddyfile: must strip client-supplied Forwarded header with header_up -Forwarded')
      expect(errors).toContain('reverse-proxy/traefik-dynamic.yml: must attach a strip-forwarded middleware clearing Forwarded')
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })

  it('rejects broken internal #anchor links and accepts valid ones', () => {
    const dir = mkdtempSync(join(tmpdir(), 'leapmux-anchors-test-'))
    try {
      mkdirSync(join(dir, 'docs/a'), { recursive: true })
      mkdirSync(join(dir, 'docs/b'), { recursive: true })
      writeFileSync(
        join(dir, 'docs/a/index.html'),
        [
          '<h2 id=ok-anchor>OK</h2>',
          '<a href="/docs/b/#good">ok</a>',
          '<a href="/docs/b/#missing">bad</a>',
          '<a href="/docs/gone/#sec">missing page</a>',
          '<a href="#ok-anchor">self</a>',
          '<a href="#">empty fragment</a>',
          '<a href="https://example.com/#external">external</a>',
        ].join(''),
      )
      writeFileSync(join(dir, 'docs/b/index.html'), '<h2 id="good">Good</h2>')
      expect(verifyInternalAnchors(dir)).toEqual([
        '/docs/a/: broken internal anchor link "/docs/b/#missing" (missing #missing on /docs/b/)',
        '/docs/a/: broken internal anchor link "/docs/gone/#sec" (target page /docs/gone/ not found)',
      ])
    }
    finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})
