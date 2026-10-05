import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { WebFetchResultBody } from '../webFetchResult'

describe('fetch renderer', () => {
  checkKindModule({
    kind: 'fetch',
    request: { url: 'https://example.com' },
    minimalTitlePart: 'https://example.com',
    titlePart: 'example.com',
    result: { result: 'page text' },
    resultPart: 'page text',
  })

  it('marks the fetched page as returned output without marking the summary', () => {
    const { container } = render(() => <WebFetchResultBody source={{ result: 'page text', code: 200, codeText: 'OK' }} />)
    const outputs = container.querySelectorAll('[data-tool-output-preview]')
    expect(outputs).toHaveLength(1)
    expect(outputs[0]?.textContent).toBe('page text')
    expect(container.textContent).toContain('200 OK')
  })
})
