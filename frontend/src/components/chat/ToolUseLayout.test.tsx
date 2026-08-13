import type { RenderContext } from '~/components/chat/messageRenderers'
import { render, screen } from '@solidjs/testing-library'
import ListTodo from 'lucide-solid/icons/list-todo'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { ToolUseLayout } from '~/components/chat/toolRenderers'
import { toolBodyBorder, toolBodyContent, toolInputText } from '~/components/chat/toolStyles.css'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { classSelector } from '~/test-support/composedClass'

// jsdom does not provide ResizeObserver
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

function makeContext(overrides: Partial<RenderContext> = {}): RenderContext {
  return {
    onCopyJson: vi.fn(),
    jsonCopied: () => false,
    ...overrides,
  }
}

describe('toolUseLayout', () => {
  it('renders title and icon in header', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="3 tasks"
          context={makeContext()}
        />
      </PreferencesProvider>
    ))

    expect(container).toHaveTextContent('3 tasks')
    // Icon should be present as an SVG element
    expect(container.querySelector('svg')).toBeInTheDocument()
  })

  it('shows summary inside bordered area', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="Header"
          summary={<div data-testid="test-summary">Summary text</div>}
          context={makeContext()}
        />
      </PreferencesProvider>
    ))

    expect(container).toHaveTextContent('Summary text')
    expect(screen.getByTestId('test-summary')).toBeInTheDocument()
    // Summary should be inside the toolBodyContent wrapper (bordered area)
    const bodyWrapper = container.querySelector(`.${toolBodyContent}`)
    expect(bodyWrapper).toBeInTheDocument()
    expect(bodyWrapper!).toHaveTextContent('Summary text')
  })

  it('hides children when collapsed (default)', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="Header"
          expanded={false}
          onToggleExpand={vi.fn()}
          context={makeContext()}
        >
          <div data-testid="body-content">Body content</div>
        </ToolUseLayout>
      </PreferencesProvider>
    ))

    expect(container).not.toHaveTextContent('Body content')
    expect(screen.queryByTestId('body-content')).not.toBeInTheDocument()
  })

  it('shows children when expanded', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="Header"
          expanded={true}
          onToggleExpand={vi.fn()}
          context={makeContext()}
        >
          <div data-testid="body-content">Body content</div>
        </ToolUseLayout>
      </PreferencesProvider>
    ))

    expect(container).toHaveTextContent('Body content')
    expect(screen.getByTestId('body-content')).toBeInTheDocument()
  })

  it('alwaysVisible bypasses expand gating', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="Header"
          alwaysVisible={true}
          expanded={false}
          onToggleExpand={vi.fn()}
          context={makeContext()}
        >
          <div data-testid="body-content">Always visible body</div>
        </ToolUseLayout>
      </PreferencesProvider>
    ))

    expect(container).toHaveTextContent('Always visible body')
    expect(screen.getByTestId('body-content')).toBeInTheDocument()
  })

  it('applies left border by default', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="Header"
          summary={<div>Summary</div>}
          context={makeContext()}
        />
      </PreferencesProvider>
    ))

    const bodyWrapper = container.querySelector(`.${toolBodyContent}`)
    expect(bodyWrapper).toBeInTheDocument()
  })

  it('bordered={false} keeps the body indent but omits the visible left border', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="Header"
          bordered={false}
          expanded={true}
          onToggleExpand={vi.fn()}
          context={makeContext()}
        >
          <div>Body</div>
        </ToolUseLayout>
      </PreferencesProvider>
    ))

    // The indent wrapper (`toolBodyContent`) is always present when
    // there is body content; only the border-color decoration
    // (`toolBodyBorder`) is gated by `bordered`.
    const bodyWrapper = container.querySelector(`.${toolBodyContent}`)
    expect(bodyWrapper).toBeInTheDocument()
    expect(bodyWrapper?.className).not.toMatch(new RegExp(toolBodyBorder))
  })

  it('bordered={true} layers the visible border on top of the indent', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="Header"
          expanded={true}
          onToggleExpand={vi.fn()}
          context={makeContext()}
        >
          <div>Body</div>
        </ToolUseLayout>
      </PreferencesProvider>
    ))

    const bodyWrapper = container.querySelector(`.${toolBodyContent}`)
    expect(bodyWrapper).toBeInTheDocument()
    expect(bodyWrapper?.className).toMatch(new RegExp(toolBodyBorder))
  })

  it('renderIcon overrides the lucide icon', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          renderIcon={() => <span data-testid="custom-icon">X</span>}
          toolName="TestTool"
          title="Header"
          context={makeContext()}
        />
      </PreferencesProvider>
    ))
    const customIcon = container.querySelector('[data-testid="custom-icon"]')
    expect(customIcon).toBeInTheDocument()
    // The override sits in the icon slot, so its parent has no lucide
    // <svg> next to it (we'd see two glyphs if `icon` weren't ignored).
    expect(customIcon?.parentElement?.querySelector('svg')).toBeNull()
  })

  // The positive control for the test below. Without it, an absence assertion
  // proves nothing: a selector that matches no element ANYWHERE passes it. This
  // pins that the selector does find the wrapper when the title is a string.
  it('wraps a string title in a toolInputText span', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="3 tasks"
          context={makeContext()}
        />
      </PreferencesProvider>
    ))

    const wrapper = container.querySelector(classSelector(toolInputText))
    expect(wrapper).toBeInTheDocument()
    expect(wrapper).toHaveTextContent('3 tasks')
  })

  it('renders JSX title without toolInputText wrapper', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title={<span data-testid="custom-title">Custom JSX</span>}
          context={makeContext()}
        />
      </PreferencesProvider>
    ))

    expect(screen.getByTestId('custom-title')).toBeInTheDocument()
    expect(container).toHaveTextContent('Custom JSX')
    // `classSelector`, not `.${toolInputText}`: the style composes `clippedText`,
    // so it exports two space-separated class names and the template form is a
    // descendant selector that matches nothing and passes this assertion for
    // the wrong reason.
    const toolInputTextSpan = container.querySelector(classSelector(toolInputText))
    expect(toolInputTextSpan).not.toBeInTheDocument()
  })
})
