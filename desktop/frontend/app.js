// Current selected mode.
let selectedMode = 'solo';

// DOM references.
const hubUrlSection = document.getElementById('hubUrlSection');
const hubUrlInput = document.getElementById('hubUrl');
const connectBtn = document.getElementById('connectBtn');
const spinner = document.getElementById('spinner');
const errorMsg = document.getElementById('errorMsg');
const versionEl = document.getElementById('version');

// Select a connection mode (solo or distributed).
function selectMode(mode) {
  selectedMode = mode;

  document.querySelectorAll('.mode-card').forEach((card) => {
    card.classList.toggle('selected', card.dataset.mode === mode);
  });

  hubUrlSection.classList.toggle('visible', mode === 'distributed');
  clearError();
}

// Show an error message.
function showError(msg) {
  errorMsg.textContent = msg;
  errorMsg.classList.add('visible');
}

// Clear error message.
function clearError() {
  errorMsg.textContent = '';
  errorMsg.classList.remove('visible');
}

// Set loading state.
function setLoading(loading) {
  connectBtn.disabled = loading;
  spinner.classList.toggle('visible', loading);
  if (loading) {
    clearError();
  }
}

// Connect to LeapMux.
async function connect() {
  setLoading(true);

  try {
    let url;
    if (selectedMode === 'solo') {
      url = await window.go.main.App.ConnectSolo();
    } else {
      const hubUrl = hubUrlInput.value.trim();
      if (!hubUrl) {
        showError('Please enter a Hub URL.');
        setLoading(false);
        return;
      }
      url = await window.go.main.App.ConnectDistributed(hubUrl);
    }

    // Navigate the WebView to the LeapMux URL.
    window.location.href = url;
  } catch (err) {
    showError(err.message || String(err));
    setLoading(false);
  }
}

// Initialize on load: restore saved config and display version.
async function init() {
  try {
    const config = await window.go.main.App.GetConfig();
    if (config && config.mode) {
      selectMode(config.mode);
      if (config.mode === 'distributed' && config.hub_url) {
        hubUrlInput.value = config.hub_url;
      }
    }
  } catch (_) {
    // Ignore config load errors.
  }

  try {
    const ver = await window.go.main.App.GetVersion();
    if (ver) {
      versionEl.textContent = 'v' + ver;
    }
  } catch (_) {
    // Ignore version fetch errors.
  }
}

// Wait for Wails runtime to be ready.
document.addEventListener('DOMContentLoaded', function () {
  if (window.go && window.go.main) {
    init();
  } else {
    // Wails injects the runtime asynchronously; poll briefly.
    const check = setInterval(function () {
      if (window.go && window.go.main) {
        clearInterval(check);
        init();
      }
    }, 50);
  }
});
