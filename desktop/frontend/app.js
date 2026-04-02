// Current selected mode.
let selectedMode = 'solo';

// Saved config (populated during init).
let savedConfig = null;

// Whether the container UI is currently visible.
let uiVisible = false;

// Full Disk Access state.
let fullDiskAccessGranted = true;
let fullDiskAccessPollTimer = null;

// DOM references.
const container = document.querySelector('.container');
const hubUrlSection = document.getElementById('hubUrlSection');
const hubUrlInput = document.getElementById('hubUrl');
const connectBtn = document.getElementById('connectBtn');
const spinner = document.getElementById('spinner');
const errorMsg = document.getElementById('errorMsg');
const errorMsgText = document.getElementById('errorMsgText');
const versionEl = document.getElementById('version');
const fullDiskAccessSection = document.getElementById('fullDiskAccessSection');

// Fade the container in. Returns a promise that resolves after the transition.
function fadeIn() {
  if (uiVisible) return Promise.resolve();
  uiVisible = true;
  container.classList.add('visible');
  return new Promise(function (resolve) {
    container.addEventListener('transitionend', resolve, { once: true });
  });
}

// Fade the container out. Returns a promise that resolves after the transition.
function fadeOut() {
  if (!uiVisible) return Promise.resolve();
  uiVisible = false;
  container.classList.remove('visible');
  return new Promise(function (resolve) {
    container.addEventListener('transitionend', resolve, { once: true });
  });
}

// Check whether the Hub URL looks like a valid http(s) URL.
function isValidHubUrl(value) {
  var s = value.trim();
  if (!s) return false;
  // Accept with explicit scheme or bare hostname (we prepend https:// later).
  if (!/^https?:\/\//i.test(s)) {
    s = 'https://' + s;
  }
  try {
    var u = new URL(s);
    return u.protocol === 'http:' || u.protocol === 'https:';
  } catch (_) {
    return false;
  }
}

// Update the Connect button's enabled state based on the current mode and input.
function updateConnectBtn() {
  if (selectedMode === 'distributed') {
    connectBtn.disabled = !isValidHubUrl(hubUrlInput.value);
  } else {
    connectBtn.disabled = !fullDiskAccessGranted;
  }
}

// Check Full Disk Access and update status UI.
async function checkFullDiskAccess() {
  var prev = fullDiskAccessGranted;
  try {
    fullDiskAccessGranted = await window.go.main.App.CheckFullDiskAccess();
  } catch (_) {
    fullDiskAccessGranted = true;
  }

  if (fullDiskAccessGranted && !prev) {
    stopFullDiskAccessPoll();
    restartApp();
    return;
  }

  fullDiskAccessSection.classList.toggle('visible', !fullDiskAccessGranted);
  updateConnectBtn();
}

// Open System Settings to the Full Disk Access pane.
function openFullDiskAccessSettings() {
  window.go.main.App.OpenFullDiskAccessSettings();
}

// Restart the app.
function restartApp() {
  window.go.main.App.Restart();
}

// Start polling for Full Disk Access status.
function startFullDiskAccessPoll() {
  stopFullDiskAccessPoll();
  fullDiskAccessPollTimer = setInterval(checkFullDiskAccess, 1000);
}

// Stop polling for Full Disk Access status.
function stopFullDiskAccessPoll() {
  if (fullDiskAccessPollTimer !== null) {
    clearInterval(fullDiskAccessPollTimer);
    fullDiskAccessPollTimer = null;
  }
}

// Select a connection mode (solo or distributed).
function selectMode(mode) {
  selectedMode = mode;

  document.querySelectorAll('.mode-card').forEach((card) => {
    card.classList.toggle('selected', card.dataset.mode === mode);
  });

  hubUrlSection.classList.toggle('visible', mode === 'distributed');

  if (mode === 'solo') {
    checkFullDiskAccess().then(function () {
      if (!fullDiskAccessGranted) {
        startFullDiskAccessPoll();
      }
    });
  } else {
    fullDiskAccessSection.classList.remove('visible');
    stopFullDiskAccessPoll();
  }

  clearError();
  updateConnectBtn();
}

// Listen for input changes on the Hub URL field.
hubUrlInput.addEventListener('input', function () {
  clearError();
  updateConnectBtn();
});

// Show an error message.
function showError(msg) {
  errorMsgText.textContent = msg;
  errorMsg.classList.add('visible');
}

// Clear error message.
function clearError() {
  errorMsgText.textContent = '';
  errorMsg.classList.remove('visible');
}

// Set loading state.
function setLoading(loading) {
  spinner.classList.toggle('visible', loading);
  if (loading) {
    connectBtn.disabled = true;
    clearError();
  } else {
    updateConnectBtn();
  }
}

// Animate the window from its current size to the target size over the given
// duration (ms), keeping it centered. Uses an ease-out cubic curve.
function animateWindowResize(targetW, targetH, durationMs) {
  const rt = window.runtime;
  if (!rt || !rt.WindowGetSize) {
    return Promise.resolve();
  }

  return rt.WindowGetSize().then(function (cur) {
    const startW = cur.w;
    const startH = cur.h;

    // Nothing to do if already at the target size.
    if (startW === targetW && startH === targetH) {
      return;
    }

    return new Promise(function (resolve) {
      const startTime = performance.now();

      function step(now) {
        const elapsed = now - startTime;
        const t = Math.min(elapsed / durationMs, 1);
        // Ease-out cubic: 1 - (1 - t)^3
        const eased = 1 - Math.pow(1 - t, 3);

        const w = Math.round(startW + (targetW - startW) * eased);
        const h = Math.round(startH + (targetH - startH) * eased);

        rt.WindowSetSize(w, h);
        rt.WindowCenter();

        if (t < 1) {
          requestAnimationFrame(step);
        } else {
          resolve();
        }
      }

      requestAnimationFrame(step);
    });
  });
}

// Perform the post-connection transition: fade out UI if visible, resize, navigate.
async function navigateAfterConnect(url) {
  if (uiVisible) {
    await fadeOut();
  }

  var targetW = 1280;
  var targetH = 800;
  if (savedConfig && savedConfig.window_width > 0 && savedConfig.window_height > 0) {
    targetW = savedConfig.window_width;
    targetH = savedConfig.window_height;
  }
  await animateWindowResize(targetW, targetH, 400);

  window.location.href = url;
}

// Connect to LeapMux. If showUI is true, the UI is already visible and loading
// indicators are managed here. If false, this is a silent auto-connect attempt.
async function connect(showUI) {
  if (showUI) {
    setLoading(true);
  }

  try {
    let url;
    if (selectedMode === 'solo') {
      url = await window.go.main.App.ConnectSolo();
    } else {
      url = await window.go.main.App.ConnectDistributed(hubUrlInput.value.trim());
    }

    await navigateAfterConnect(url);
  } catch (err) {
    // On failure, ensure the UI is visible so the user can see the error and retry.
    await fadeIn();
    setLoading(false);
    showError(err.message || String(err));
  }
}

// Animate the window back to the launcher dimensions (matching main.go defaults).
function resizeToLauncher() {
  return animateWindowResize(900, 680, 400);
}

// Initialize on load: restore saved config, display version, and auto-connect
// if the user has previously connected successfully.
async function init() {
  // Handle the switchMode action: stop the backend, clear saved mode, and
  // show the mode selection UI without auto-connecting.
  var params = new URLSearchParams(window.location.search);
  if (params.get('action') === 'switchMode') {
    // Remove the query string so reloads don't re-trigger the action.
    history.replaceState(null, '', window.location.pathname);

    try {
      await window.go.main.App.SwitchMode();
    } catch (_) {
      // Best-effort; the config may already be cleared.
    }

    await resizeToLauncher();

    // Load the config for pre-filling UI (hub_url, saved window size, etc.)
    // but skip auto-connect.
    try {
      savedConfig = await window.go.main.App.GetConfig();
      if (savedConfig && savedConfig.hub_url) {
        hubUrlInput.value = savedConfig.hub_url;
      }
    } catch (_) {
      // Ignore.
    }

    try {
      var ver = await window.go.main.App.GetVersion();
      if (ver) {
        versionEl.textContent = 'v' + ver;
      }
    } catch (_) {
      // Ignore.
    }

    await checkFullDiskAccess();
    if (!fullDiskAccessGranted) {
      startFullDiskAccessPoll();
    }
    fadeIn();
    return;
  }

  try {
    const config = await window.go.main.App.GetConfig();
    if (config && config.mode) {
      savedConfig = config;
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

  if (savedConfig) {
    // For solo mode, check Full Disk Access before auto-connecting.
    if (savedConfig.mode === 'solo') {
      await checkFullDiskAccess();
      if (!fullDiskAccessGranted) {
        startFullDiskAccessPoll();
        fadeIn();
        return;
      }
    }

    // Returning user: try auto-connect silently. Show the UI only if it takes
    // longer than 1 second (e.g. solo mode startup).
    var showTimer = setTimeout(function () {
      setLoading(true);
      fadeIn();
    }, 1000);

    // connect() will fade in on error, or navigate on success.
    await connect(false);
    clearTimeout(showTimer);
  } else {
    // First launch: check Full Disk Access for the default solo mode.
    await checkFullDiskAccess();
    if (!fullDiskAccessGranted) {
      startFullDiskAccessPoll();
    }
    // Show the mode selection UI immediately.
    fadeIn();
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
