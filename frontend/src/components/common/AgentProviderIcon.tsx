import type { JSX } from 'solid-js'
import Bot from 'lucide-solid/icons/bot'
import { Match, Switch } from 'solid-js'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'

export function agentProviderLabel(provider?: AgentProvider): string {
  switch (provider) {
    case AgentProvider.CLAUDE_CODE: return 'Claude Code'
    case AgentProvider.CODEX: return 'Codex'
    case AgentProvider.GEMINI_CLI: return 'Gemini CLI'
    case AgentProvider.OPENCODE: return 'OpenCode'
    case AgentProvider.COPILOT_CLI: return 'Copilot CLI'
    case AgentProvider.CURSOR_CLI: return 'Cursor CLI'
    case AgentProvider.GOOSE_CLI: return 'Goose CLI'
    default: return 'Unknown'
  }
}

function iconStyle(size: number) {
  return {
    'flex-shrink': '0',
    'min-width': `${size}px`,
    'min-height': `${size}px`,
    'vertical-align': 'middle',
    'position': 'relative' as const,
    'top': '1px',
  }
}

function ClaudeCodeIcon(props: { size: number, class?: string }): JSX.Element {
  return (
    <svg
      fill-rule="evenodd"
      clip-rule="evenodd"
      height={props.size}
      width={props.size}
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      class={props.class}
      style={iconStyle(props.size)}
    >
      <path fill="#D97757" d="M20.998 10.949H24v3.102h-3v3.028h-1.487V20H18v-2.921h-1.487V20H15v-2.921H9V20H7.488v-2.921H6V20H4.487v-2.921H3V14.05H0V10.95h3V5h17.998v5.949zM6 10.949h1.488V8.102H6v2.847zm10.51 0H18V8.102h-1.49v2.847z" />
    </svg>
  )
}

function CodexIcon(props: { size: number, class?: string }): JSX.Element {
  return (
    <svg
      height={props.size}
      width={props.size}
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      class={props.class}
      style={iconStyle(props.size)}
    >
      <path d="M9.064 3.344a4.578 4.578 0 012.285-.312c1 .115 1.891.54 2.673 1.275.01.01.024.017.037.021a.09.09 0 00.043 0 4.55 4.55 0 013.046.275l.047.022.116.057a4.581 4.581 0 012.188 2.399c.209.51.313 1.041.315 1.595a4.24 4.24 0 01-.134 1.223.123.123 0 00.03.115c.594.607.988 1.33 1.183 2.17.289 1.425-.007 2.71-.887 3.854l-.136.166a4.548 4.548 0 01-2.201 1.388.123.123 0 00-.081.076c-.191.551-.383 1.023-.74 1.494-.9 1.187-2.222 1.846-3.711 1.838-1.187-.006-2.239-.44-3.157-1.302a.107.107 0 00-.105-.024c-.388.125-.78.143-1.204.138a4.441 4.441 0 01-1.945-.466 4.544 4.544 0 01-1.61-1.335c-.152-.202-.303-.392-.414-.617a5.81 5.81 0 01-.37-.961 4.582 4.582 0 01-.014-2.298.124.124 0 00.006-.056.085.085 0 00-.027-.048 4.467 4.467 0 01-1.034-1.651 3.896 3.896 0 01-.251-1.192 5.189 5.189 0 01.141-1.6c.337-1.112.982-1.985 1.933-2.618.212-.141.413-.251.601-.33.215-.089.43-.164.646-.227a.098.098 0 00.065-.066 4.51 4.51 0 01.829-1.615 4.535 4.535 0 011.837-1.388zm3.482 10.565a.637.637 0 000 1.272h3.636a.637.637 0 100-1.272h-3.636zM8.462 9.23a.637.637 0 00-1.106.631l1.272 2.224-1.266 2.136a.636.636 0 101.095.649l1.454-2.455a.636.636 0 00.005-.64L8.462 9.23z" fill="url(#lobe-icons-codex-fill)" />
      <defs>
        <linearGradient gradientUnits="userSpaceOnUse" id="lobe-icons-codex-fill" x1="12" x2="12" y1="3" y2="21">
          <stop stop-color="#B1A7FF" />
          <stop offset=".5" stop-color="#7A9DFF" />
          <stop offset="1" stop-color="#3941FF" />
        </linearGradient>
      </defs>
    </svg>
  )
}

function GeminiCliIcon(props: { size: number, class?: string }): JSX.Element {
  return (
    <svg
      height={props.size}
      width={props.size}
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      class={props.class}
      style={iconStyle(props.size)}
    >
      <path d="M20.616 10.835a14.147 14.147 0 01-4.45-3.001 14.111 14.111 0 01-3.678-6.452.503.503 0 00-.975 0 14.134 14.134 0 01-3.679 6.452 14.155 14.155 0 01-4.45 3.001c-.65.28-1.318.505-2.002.678a.502.502 0 000 .975c.684.172 1.35.397 2.002.677a14.147 14.147 0 014.45 3.001 14.112 14.112 0 013.679 6.453.502.502 0 00.975 0c.172-.685.397-1.351.677-2.003a14.145 14.145 0 013.001-4.45 14.113 14.113 0 016.453-3.678.503.503 0 000-.975 13.245 13.245 0 01-2.003-.678z" fill="#3186FF" />
      <path d="M20.616 10.835a14.147 14.147 0 01-4.45-3.001 14.111 14.111 0 01-3.678-6.452.503.503 0 00-.975 0 14.134 14.134 0 01-3.679 6.452 14.155 14.155 0 01-4.45 3.001c-.65.28-1.318.505-2.002.678a.502.502 0 000 .975c.684.172 1.35.397 2.002.677a14.147 14.147 0 014.45 3.001 14.112 14.112 0 013.679 6.453.502.502 0 00.975 0c.172-.685.397-1.351.677-2.003a14.145 14.145 0 013.001-4.45 14.113 14.113 0 016.453-3.678.503.503 0 000-.975 13.245 13.245 0 01-2.003-.678z" fill="url(#lobe-icons-gemini-fill-0)" />
      <path d="M20.616 10.835a14.147 14.147 0 01-4.45-3.001 14.111 14.111 0 01-3.678-6.452.503.503 0 00-.975 0 14.134 14.134 0 01-3.679 6.452 14.155 14.155 0 01-4.45 3.001c-.65.28-1.318.505-2.002.678a.502.502 0 000 .975c.684.172 1.35.397 2.002.677a14.147 14.147 0 014.45 3.001 14.112 14.112 0 013.679 6.453.502.502 0 00.975 0c.172-.685.397-1.351.677-2.003a14.145 14.145 0 013.001-4.45 14.113 14.113 0 016.453-3.678.503.503 0 000-.975 13.245 13.245 0 01-2.003-.678z" fill="url(#lobe-icons-gemini-fill-1)" />
      <path d="M20.616 10.835a14.147 14.147 0 01-4.45-3.001 14.111 14.111 0 01-3.678-6.452.503.503 0 00-.975 0 14.134 14.134 0 01-3.679 6.452 14.155 14.155 0 01-4.45 3.001c-.65.28-1.318.505-2.002.678a.502.502 0 000 .975c.684.172 1.35.397 2.002.677a14.147 14.147 0 014.45 3.001 14.112 14.112 0 013.679 6.453.502.502 0 00.975 0c.172-.685.397-1.351.677-2.003a14.145 14.145 0 013.001-4.45 14.113 14.113 0 016.453-3.678.503.503 0 000-.975 13.245 13.245 0 01-2.003-.678z" fill="url(#lobe-icons-gemini-fill-2)" />
      <defs>
        <linearGradient gradientUnits="userSpaceOnUse" id="lobe-icons-gemini-fill-0" x1="7" x2="11" y1="15.5" y2="12">
          <stop stop-color="#08B962" />
          <stop offset="1" stop-color="#08B962" stop-opacity="0" />
        </linearGradient>
        <linearGradient gradientUnits="userSpaceOnUse" id="lobe-icons-gemini-fill-1" x1="8" x2="11.5" y1="5.5" y2="11">
          <stop stop-color="#F94543" />
          <stop offset="1" stop-color="#F94543" stop-opacity="0" />
        </linearGradient>
        <linearGradient gradientUnits="userSpaceOnUse" id="lobe-icons-gemini-fill-2" x1="3.5" x2="17.5" y1="13.5" y2="12">
          <stop stop-color="#FABC12" />
          <stop offset=".46" stop-color="#FABC12" stop-opacity="0" />
        </linearGradient>
      </defs>
    </svg>
  )
}

function OpenCodeIcon(props: { size: number, class?: string }): JSX.Element {
  return (
    <svg
      height={props.size}
      width={props.size}
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      class={props.class}
      style={iconStyle(props.size)}
    >
      <path fill="#CFCECD" d="M8 6h8v12H8z" />
      <path fill="#656363" fill-rule="evenodd" d="M4 2h16v20H4zM8 6h8v12H8z" />
    </svg>
  )
}

function CopilotCliIcon(props: { size: number, class?: string }): JSX.Element {
  return (
    <svg
      height={props.size}
      width={props.size}
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      class={props.class}
      style={iconStyle(props.size)}
    >
      <path fill="#000000" fill-rule="evenodd" d="M19.245 5.364c1.322 1.36 1.877 3.216 2.11 5.817.622 0 1.2.135 1.592.654l.73.964c.21.278.323.61.323.955v2.62c0 .339-.173.669-.453.868C20.239 19.602 16.157 21.5 12 21.5c-4.6 0-9.205-2.583-11.547-4.258-.28-.2-.452-.53-.453-.868v-2.62c0-.345.113-.679.321-.956l.73-.963c.392-.517.974-.654 1.593-.654l.029-.297c.25-2.446.81-4.213 2.082-5.52 2.461-2.54 5.71-2.851 7.146-2.864h.198c1.436.013 4.685.323 7.146 2.864zm-7.244 4.328c-.284 0-.613.016-.962.05-.123.447-.305.85-.57 1.108-1.05 1.023-2.316 1.18-2.994 1.18-.638 0-1.306-.13-1.851-.464-.516.165-1.012.403-1.044.996a65.882 65.882 0 00-.063 2.884l-.002.48c-.002.563-.005 1.126-.013 1.69.002.326.204.63.51.765 2.482 1.102 4.83 1.657 6.99 1.657 2.156 0 4.504-.555 6.985-1.657a.854.854 0 00.51-.766c.03-1.682.006-3.372-.076-5.053-.031-.596-.528-.83-1.046-.996-.546.333-1.212.464-1.85.464-.677 0-1.942-.157-2.993-1.18-.266-.258-.447-.661-.57-1.108-.32-.032-.64-.049-.96-.05zm-2.525 4.013c.539 0 .976.426.976.95v1.753c0 .525-.437.95-.976.95a.964.964 0 01-.976-.95v-1.752c0-.525.437-.951.976-.951zm5 0c.539 0 .976.426.976.95v1.753c0 .525-.437.95-.976.95a.964.964 0 01-.976-.95v-1.752c0-.525.437-.951.976-.951zM7.635 5.087c-1.05.102-1.935.438-2.385.906-.975 1.037-.765 3.668-.21 4.224.405.394 1.17.657 1.995.657h.09c.649-.013 1.785-.176 2.73-1.11.435-.41.705-1.433.675-2.47-.03-.834-.27-1.52-.63-1.813-.39-.336-1.275-.482-2.265-.394zm6.465.394c-.36.292-.6.98-.63 1.813-.03 1.037.24 2.06.675 2.47.968.957 2.136 1.104 2.776 1.11h.044c.825 0 1.59-.263 1.995-.657.555-.556.765-3.187-.21-4.224-.45-.468-1.335-.804-2.385-.906-.99-.088-1.875.058-2.265.394zM12 7.615c-.24 0-.525.015-.84.044.03.16.045.336.06.526l-.001.159a2.94 2.94 0 01-.014.25c.225-.022.425-.027.612-.028h.366c.187 0 .387.006.612.028-.015-.146-.015-.277-.015-.409.015-.19.03-.365.06-.526a9.29 9.29 0 00-.84-.044z" />
    </svg>
  )
}

function GooseCliIcon(props: { size: number, class?: string }): JSX.Element {
  return (
    <svg
      height={props.size}
      width={props.size}
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      class={props.class}
      style={iconStyle(props.size)}
    >
      <path fill="#000" d="M17.873 3.808c-.58-.16-1.208-.048-1.748.192a4.44 4.44 0 00-1.452 1.056c-.78.828-1.332 1.884-1.668 2.988-.168.552-.288 1.116-.372 1.692-.504-.132-1.032-.168-1.548-.096a3.96 3.96 0 00-2.412 1.356c-.348.396-.624.852-.804 1.344a5.76 5.76 0 00-.3 1.512c-.048.756.012 1.524.18 2.268.168.744.444 1.464.84 2.112.384.636.888 1.2 1.5 1.62a4.14 4.14 0 002.052.684c.372.024.744-.012 1.104-.108.348-.096.684-.24.996-.432.624-.384 1.116-.936 1.44-1.572.336-.648.504-1.368.528-2.1.024-.732-.072-1.464-.264-2.172a8.88 8.88 0 00-.852-2.016c.456-.264.876-.588 1.248-.96.564-.564 1.032-1.224 1.356-1.944.324-.72.504-1.5.504-2.292 0-.396-.048-.792-.156-1.176a3.12 3.12 0 00-.492-.996 2.16 2.16 0 00-.828-.672 1.62 1.62 0 00-.552-.168l-.072-.012-.024-.004a1.44 1.44 0 00-.216-.024h-.024c.012 0 .024 0 .024-.012a.48.48 0 00-.168-.012l.048.012zm.216 1.2h.048l.024.012c.012 0 .024.012.036.012l.036.012a.84.84 0 01.336.3c.108.156.192.336.24.528.06.204.084.42.084.636 0 .576-.132 1.152-.384 1.692-.252.528-.624 1.008-1.068 1.416a5.64 5.64 0 01-.72.564c.024-.396.024-.792-.012-1.188a10.08 10.08 0 00-.168-1.416c-.096-.504-.24-1.008-.432-1.488a5.28 5.28 0 00-.36-.72c.3-.336.636-.624 1.008-.852.36-.24.756-.42 1.14-.492l.024-.012h.084l.048-.012.036.012zm-1.716 5.724c.312.54.564 1.116.744 1.716.18.612.276 1.248.264 1.884a3.48 3.48 0 01-.36 1.572c-.192.384-.48.72-.84.948a2.04 2.04 0 01-.648.276 2.16 2.16 0 01-.684.06 2.64 2.64 0 01-1.32-.456c-.408-.276-.744-.66-1.008-1.092a6.12 6.12 0 01-.66-1.752 6.48 6.48 0 01-.144-1.848c.024-.42.108-.84.252-1.236.12-.348.312-.672.564-.948.264-.288.588-.504.948-.636.372-.132.78-.168 1.176-.084.264.06.516.156.756.3a5.4 5.4 0 01.96 1.296z" />
    </svg>
  )
}

function CursorCliIcon(props: { size: number, class?: string }): JSX.Element {
  return (
    <svg
      fill="#000"
      fill-rule="evenodd"
      height={props.size}
      width={props.size}
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      class={props.class}
      style={iconStyle(props.size)}
    >
      <title>Cursor</title>
      <path d="M22.106 5.68 12.5.135a.998.998 0 0 0-.998 0L1.893 5.68a.84.84 0 0 0-.419.726v11.186c0 .3.16.577.42.727l9.607 5.547a.999.999 0 0 0 .998 0l9.608-5.547a.84.84 0 0 0 .42-.727V6.407a.84.84 0 0 0-.42-.726zm-.603 1.176L12.228 22.92c-.063.108-.228.064-.228-.061V12.34a.59.59 0 0 0-.295-.51l-9.11-5.26c-.107-.062-.063-.228.062-.228h18.55c.264 0 .428.286.296.514z" />
    </svg>
  )
}

export interface AgentProviderIconProps {
  provider?: AgentProvider
  size: number
  class?: string
}

export function AgentProviderIcon(props: AgentProviderIconProps): JSX.Element {
  return (
    <Switch fallback={<Bot size={props.size} class={props.class} style={iconStyle(props.size)} />}>
      <Match when={props.provider === AgentProvider.CLAUDE_CODE}>
        <ClaudeCodeIcon size={props.size} class={props.class} />
      </Match>
      <Match when={props.provider === AgentProvider.CODEX}>
        <CodexIcon size={props.size} class={props.class} />
      </Match>
      <Match when={props.provider === AgentProvider.GEMINI_CLI}>
        <GeminiCliIcon size={props.size} class={props.class} />
      </Match>
      <Match when={props.provider === AgentProvider.OPENCODE}>
        <OpenCodeIcon size={props.size} class={props.class} />
      </Match>
      <Match when={props.provider === AgentProvider.COPILOT_CLI}>
        <CopilotCliIcon size={props.size} class={props.class} />
      </Match>
      <Match when={props.provider === AgentProvider.CURSOR_CLI}>
        <CursorCliIcon size={props.size} class={props.class} />
      </Match>
      <Match when={props.provider === AgentProvider.GOOSE_CLI}>
        <GooseCliIcon size={props.size} class={props.class} />
      </Match>
    </Switch>
  )
}
