export type ToastType = 'danger' | 'success'

export function showToast(message: string, type: ToastType = 'danger') {
  const variant = type === 'success' ? 'success' : 'danger'

  const toast = document.createElement('output')
  toast.setAttribute('data-variant', variant)
  toast.style.display = 'flex'
  toast.style.alignItems = 'start'
  toast.style.gap = 'var(--space-3)'

  const msgEl = document.createElement('div')
  msgEl.className = 'toast-message'
  msgEl.style.flex = '1'
  msgEl.textContent = message
  toast.appendChild(msgEl)

  const closeBtn = document.createElement('button')
  closeBtn.setAttribute('data-close', '')
  closeBtn.textContent = '\u00D7'
  closeBtn.onclick = () => toast.remove()
  toast.appendChild(closeBtn)

  window.ot.toastEl(toast, {
    placement: 'bottom-right',
    duration: 3000,
  })
}
