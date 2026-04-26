import type { RenderContext } from '~/components/chat/messageRenderers'
import { render, screen } from '@solidjs/testing-library'
import ListTodo from 'lucide-solid/icons/list-todo'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { ToolUseLayout } from '~/components/chat/toolRenderers'
import { toolBodyContent, toolInputText } from '~/components/chat/toolStyles.css'
import { PreferencesProvider } from '~/context/PreferencesContext'

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

  it('bordered={false} omits left border', () => {
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

    const bodyWrapper = container.querySelector(`.${toolBodyContent}`)
    expect(bodyWrapper).not.toBeInTheDocument()
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
    // Should NOT wrap JSX title in toolInputText span
    const toolInputTextSpan = container.querySelector(`.${toolInputText}`)
    expect(toolInputTextSpan).not.toBeInTheDocument()
  })
})
