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
    </Switch>
  )
}
