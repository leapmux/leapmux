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
    threadChildCount: 0,
    threadExpanded: false,
    onToggleThread: vi.fn(),
    onCopyJson: vi.fn(),
    jsonCopied: false,
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

    expect(container.textContent).toContain('3 tasks')
    // Icon should be present as an SVG element
    expect(container.querySelector('svg')).not.toBeNull()
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

    expect(container.textContent).toContain('Summary text')
    expect(screen.getByTestId('test-summary')).not.toBeNull()
    // Summary should be inside the toolBodyContent wrapper (bordered area)
    const bodyWrapper = container.querySelector(`.${toolBodyContent}`)
    expect(bodyWrapper).not.toBeNull()
    expect(bodyWrapper!.textContent).toContain('Summary text')
  })

  it('hides children when collapsed (default)', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="Header"
          context={makeContext({ threadExpanded: false })}
        >
          <div data-testid="body-content">Body content</div>
        </ToolUseLayout>
      </PreferencesProvider>
    ))

    expect(container.textContent).not.toContain('Body content')
    expect(screen.queryByTestId('body-content')).toBeNull()
  })

  it('shows children when expanded', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="Header"
          context={makeContext({ threadExpanded: true })}
        >
          <div data-testid="body-content">Body content</div>
        </ToolUseLayout>
      </PreferencesProvider>
    ))

    expect(container.textContent).toContain('Body content')
    expect(screen.getByTestId('body-content')).not.toBeNull()
  })

  it('alwaysVisible bypasses expand gating', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="Header"
          alwaysVisible={true}
          context={makeContext({ threadExpanded: false })}
        >
          <div data-testid="body-content">Always visible body</div>
        </ToolUseLayout>
      </PreferencesProvider>
    ))

    expect(container.textContent).toContain('Always visible body')
    expect(screen.getByTestId('body-content')).not.toBeNull()
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
    expect(bodyWrapper).not.toBeNull()
  })

  it('bordered={false} omits left border', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="Header"
          bordered={false}
          context={makeContext({ threadExpanded: true })}
        >
          <div>Body</div>
        </ToolUseLayout>
      </PreferencesProvider>
    ))

    const bodyWrapper = container.querySelector(`.${toolBodyContent}`)
    expect(bodyWrapper).toBeNull()
  })

  it('shows ControlResponseTag when approved', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="Header"
          context={makeContext({
            childControlResponse: { action: 'approved', comment: '' },
          })}
        />
      </PreferencesProvider>
    ))

    expect(container.textContent).toContain('Approved')
  })

  it('shows ControlResponseTag when rejected', () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <ToolUseLayout
          icon={ListTodo}
          toolName="TestTool"
          title="Header"
          context={makeContext({
            childControlResponse: { action: 'rejected', comment: 'Needs changes' },
          })}
        />
      </PreferencesProvider>
    ))

    expect(container.textContent).toContain('Rejected: Needs changes')
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

    expect(screen.getByTestId('custom-title')).not.toBeNull()
    expect(container.textContent).toContain('Custom JSX')
    // Should NOT wrap JSX title in toolInputText span
    const toolInputTextSpan = container.querySelector(`.${toolInputText}`)
    expect(toolInputTextSpan).toBeNull()
  })
})
