// Ambient types for the external captcha providers' browser SDKs, loaded
// on demand from their CDNs by TurnstileField / RecaptchaV3Field. Only
// the surface those components use is declared. This file is a script
// (no imports/exports), so every declaration below is global already.
// Turnstile: https://developers.cloudflare.com/turnstile/client-side-rendering/

interface TurnstileRenderOptions {
  'sitekey': string
  'action'?: string
  'theme'?: 'auto' | 'light' | 'dark'
  'size'?: 'normal' | 'flexible' | 'compact'
  'appearance'?: 'always' | 'execute' | 'interaction-only'
  'callback'?: (token: string) => void
  'error-callback'?: (errorCode: number | string) => void
  'expired-callback'?: () => void
  'timeout-callback'?: () => void
}

interface Turnstile {
  render: (container: HTMLElement | string, options: TurnstileRenderOptions) => string
  reset: (widgetId?: string) => void
  remove: (widgetId?: string) => void
  getResponse: (widgetId?: string) => string | undefined
  ready: (callback: () => void) => void
}

// reCAPTCHA v3: https://developers.google.com/recaptcha/docs/v3

interface Grecaptcha {
  ready: (callback: () => void) => void
  execute: (siteKey: string, options: { action: string }) => Promise<string>
}

interface Window {
  turnstile?: Turnstile
  grecaptcha?: Grecaptcha
}
