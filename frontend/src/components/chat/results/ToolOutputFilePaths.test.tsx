import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { ToolOutputFilePaths } from './ToolOutputFilePaths'

describe('ToolOutputFilePaths', () => {
  it.each([undefined, []])('renders nothing for absent paths %s', (paths) => {
    const { container } = render(() => <ToolOutputFilePaths paths={paths} />)
    expect(container.querySelector('[data-testid="tool-output-file-paths"]')).toBeNull()
  })

  it('renders each reported path in order without a link or file action', () => {
    const paths = ['/native/first.log', 'C:\\native\\結果.log', ' /native/with spaces/result.log ']
    const { container } = render(() => <ToolOutputFilePaths paths={paths} />)
    expect(container.textContent).toBe(paths.map(path => `Output file:${path}`).join(''))
    expect(container.querySelector('a')).toBeNull()
    expect(container.querySelector('button')).toBeNull()
  })

  it('keeps long path text and escapes HTML-like names', () => {
    const long = `/native/${'long-path-'.repeat(4000)}.log`
    const markup = '/native/<img src=x onerror=alert(1)>.log'
    const { container } = render(() => <ToolOutputFilePaths paths={[long, markup]} />)
    expect(container.textContent).toContain(long)
    expect(container.textContent).toContain(markup)
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('script')).toBeNull()
  })
})
