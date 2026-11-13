import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { markdownContent } from '~/components/chat/markdownEditor/markdownContent.css'
import { MarkdownFileView } from '~/components/fileviewer/MarkdownFileView'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { splitPaneMarkdown } from './FileViewer.css'

function renderFile(text: string, mode: 'render' | 'split') {
  const content = new TextEncoder().encode(text)
  return render(() => (
    <MarkdownFileView content={content} filePath="README.md" totalSize={content.length} mode={mode} />
  ))
}

// The stand-in Worker of the jsdom tests renders the Markdown, so each test waits
// for its reply before it reads the body.
describe('MarkdownFileView', () => {
  it('renders the file as Markdown in render mode', async () => {
    const { container } = renderFile('# Read me\n\n- one\n- two\n', 'render')
    await settleWorkerReplies()

    const body = container.querySelector(`.${markdownContent}`)
    expect(body?.querySelector('h1')?.textContent).toBe('Read me')
    expect(body?.querySelectorAll('li')).toHaveLength(2)
  })

  it('renders the Markdown pane beside the source in split mode', async () => {
    const { container } = renderFile('# Split\n', 'split')
    await settleWorkerReplies()

    const pane = container.querySelector(`.${splitPaneMarkdown}`)
    expect(pane?.classList.contains(markdownContent)).toBe(true)
    expect(pane?.querySelector('h1')?.textContent).toBe('Split')
  })

  it('renders a large file in full as Markdown, with no plain-text display', async () => {
    const sections = Array.from({ length: 2_000 }, (_, i) => `## Section ${i}\n\n${'text '.repeat(20)}`)
    const { container } = renderFile(sections.join('\n\n'), 'render')
    await settleWorkerReplies()

    const headings = container.querySelectorAll('h2')
    expect(headings).toHaveLength(2_000)
    expect(headings[1_999]?.textContent).toBe('Section 1999')
    expect(container.querySelector('pre')).toBeNull()
  })
})
