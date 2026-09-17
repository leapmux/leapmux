import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { readFileResultFromContent } from '../ir/readFileResult'
import { EMPTY_RESULT_NOTICE } from './emptyResultNotice'
import { ReadFileResultBody } from './readFileResult'

describe('ReadFileResultBody with no content', () => {
  // Cursor refuses to read a large file and returns an ACP `rawOutput.content` of
  // "". The row used to answer `Empty file`, which is a claim about the FILE that
  // LeapMux has no evidence for -- the file was 4.8 MB. See RL-037.
  it('states that the result carried nothing, not that the file is empty', () => {
    const { container } = render(() => (
      <ReadFileResultBody source={readFileResultFromContent({ content: '' })} />
    ))
    expect(container.textContent).toContain(EMPTY_RESULT_NOTICE)
    expect(container.textContent).not.toContain('Empty file')
  })

  // A provider that DID say something keeps saying it. The notice is the last
  // resort, never a replacement for a message the result carried.
  it('prefers whatever the result did carry', () => {
    const source = {
      ...readFileResultFromContent({ content: '' }),
      fallbackContent: 'File content (5040000 characters) exceeds maximum allowed characters',
    }
    const { container } = render(() => <ReadFileResultBody source={source} />)
    expect(container.textContent).toContain('exceeds maximum allowed characters')
    expect(container.textContent).not.toContain(EMPTY_RESULT_NOTICE)
  })

  it('renders real content rather than the notice', () => {
    const { container } = render(() => (
      <ReadFileResultBody source={readFileResultFromContent({ content: 'alpha' })} />
    ))
    expect(container.textContent).toContain('alpha')
    expect(container.textContent).not.toContain(EMPTY_RESULT_NOTICE)
  })
})
