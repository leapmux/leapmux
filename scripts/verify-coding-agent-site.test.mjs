import { describe, expect, it } from 'bun:test'
import { codingAgentMatrixDimensions, verifyCodingAgentProviderCount, verifyCodingAgentSite } from './verify-coding-agent-site.mjs'

const SUPPORT_STATES = [
  { id: 'supported', symbol: '✅', label: 'Supported' },
  { id: 'agent-limit', symbol: '🚫', label: 'The agent does not offer it' },
  { id: 'leapmux-limit', symbol: '🚧', label: 'LeapMux does not support it yet' },
]
const CELL_DETAIL = 'Maintainer evidence that the website must never show.'
const PROVIDER_DETAIL = 'Provider evidence that stays with the maintainers.'

const features = {
  groups: [{ id: 'tools', label: 'Tools' }],
  features: [{ id: 'mcp-input-request', label: 'MCP input request', description: 'Collects requested fields.', showInMatrix: true, group: 'tools' }],
}
const checklist = {
  supportStates: SUPPORT_STATES,
  providerGroups: [[{
    id: 'pi',
    label: 'Pi',
    url: 'https://pi.dev/',
    icon: '/icons/agents/pi.svg',
    userNote: 'Provider note.',
    detailNote: PROVIDER_DETAIL,
  }]],
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
  extendedFeatures.features.push({ id: 'agent-questions', label: 'Agent questions', description: 'Asks the user.', showInMatrix: true, group: 'conversation' })
  const extendedChecklist = structuredClone(checklist)
  extendedChecklist.cells['agent-questions'] = { pi: { support: 'supported', userNote: '', detailNote: '' } }
  const extendedHtml = html
    .replace('</tbody>', `</tbody>${SECOND_GROUP_BODY}`)
    .replace('</dl>', '<dt id="feature-agent-questions">Agent questions</dt><dd>Asks the user.</dd></dl>')
  return { features: extendedFeatures, checklist: extendedChecklist, html: extendedHtml }
}

function withHiddenFeature() {
  const extendedFeatures = structuredClone(features)
  extendedFeatures.features.push({ id: 'basic-chat', label: 'Basic chat', description: 'Receives a prompt and returns an answer.', showInMatrix: false })
  const extendedChecklist = structuredClone(checklist)
  extendedChecklist.cells['basic-chat'] = { pi: { support: 'agent-limit', userNote: '', detailNote: 'A test-only detail note that must stay off the website.' } }
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
    const extended = { providerGroups: [[{ id: 'a' }], [{ id: 'b' }, { id: 'c' }]] }
    expect(verifyCodingAgentProviderCount('<span data-agent-provider-count>3</span>', extended)).toEqual([])
    expect(verifyCodingAgentProviderCount('<span data-agent-provider-count>1</span>', extended))
      .toContain('provider count 1 is "1", expected 3')
  })

  it('rejects a page without the generated count', () => {
    expect(verifyCodingAgentProviderCount('<p>Three providers.</p>', checklist))
      .toContain('the page contains no generated provider count')
  })
})
