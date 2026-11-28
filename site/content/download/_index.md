---
title: "Download LeapMux"
description: "Download LeapMux Desktop or the standalone leapmux binary for macOS, Linux, Windows, or Docker."
layout: hextra-home
---

<div class="hx:mt-6 hx:mb-6">
  <h1 class="hx:text-4xl hx:font-bold hx:tracking-tight hx:mb-3">Download LeapMux</h1>
  <p class="hx:text-lg hx:text-gray-600 hx:dark:text-gray-400">
    Run LeapMux Desktop for one-click solo mode on your laptop, or install the standalone binary or Docker image for headless servers and remote Workers.
  </p>
</div>

<div class="lm-download-hero" id="lm-download-hero">
  <div class="lm-download-hero-main">
    <div class="lm-download-hero-eyebrow" id="lm-download-eyebrow">Latest release on GitHub</div>
    <h2 class="lm-download-hero-title" id="lm-download-title">Download LeapMux Desktop</h2>
    <p class="lm-download-hero-desc" id="lm-download-desc">
      Includes the native desktop workspace, local solo mode, and bundled CLI on macOS (`.dmg`), Debian/Ubuntu (`.deb`), and Windows (`.msi`).
    </p>
    <div class="lm-download-hero-actions">
      <a class="lm-btn-primary" id="lm-download-primary" href="https://github.com/leapmux/leapmux/releases">Download from GitHub Releases</a>
      <a class="lm-btn-secondary" href="/docs/getting-started/installation/">Full Installation Guide</a>
      <a class="lm-btn-secondary" href="/docs/getting-started/quick-start/">5-Minute Quick Start</a>
    </div>
  </div>
</div>

<script>
(() => {
  const ua = navigator.userAgent || '';
  const platform = (navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || '';
  const hint = (platform + ' ' + ua).toLowerCase();
  const eyebrow = document.getElementById('lm-download-eyebrow');
  const title = document.getElementById('lm-download-title');
  const desc = document.getElementById('lm-download-desc');
  const btn = document.getElementById('lm-download-primary');
  if (!eyebrow || !title || !desc || !btn) return;
  if (hint.includes('mac')) {
    eyebrow.textContent = 'Detected: macOS';
    title.textContent = 'Download LeapMux for macOS';
    const binPath = document.createElement('code');
    binPath.textContent = '/usr/local/bin';
    desc.replaceChildren(
      'Signed and notarized .dmg installer for Apple Silicon (arm64). Offers to install the leapmux CLI symlink at ',
      binPath,
      '.',
    );
    btn.textContent = 'Download macOS .dmg (GitHub Releases)';
  } else if (hint.includes('win')) {
    eyebrow.textContent = 'Detected: Windows';
    title.textContent = 'Download LeapMux for Windows';
    desc.textContent = 'Per-user .msi installer for Windows x64. Installs cli\\leapmux.exe and adds it to your user PATH automatically.';
    btn.textContent = 'Download Windows .msi (GitHub Releases)';
  } else if (hint.includes('linux')) {
    eyebrow.textContent = 'Detected: Linux';
    title.textContent = 'Download LeapMux for Linux';
    desc.textContent = 'Use the .deb package for Debian/Ubuntu (includes /usr/bin/leapmux). The .AppImage is self-contained and bundles no CLI — install the standalone tarball alongside it if you use leapmux CLI or Agent Skills.';
    btn.textContent = 'Download Linux .deb / .AppImage (GitHub Releases)';
  }
})();
</script>

<div class="content lm-download-content">

## Desktop Installers & Bundled CLI

<div class="lm-table-scroll">

| Platform | Package | Architecture | Bundled `leapmux` CLI |
|---|---|---|---|
| **macOS** | `.dmg` | Apple Silicon (`arm64`) | Yes (offers `/usr/local/bin/leapmux` on first Solo workspace) |
| **Linux (Debian / Ubuntu)** | `.deb` | `amd64`, `arm64` | Yes (`/usr/bin/leapmux`) |
| **Linux (Portable)** | `.AppImage` | `amd64`, `arm64` | **No** — install the standalone tarball if you use the CLI or Agent Skills |
| **Windows** | `.msi` | `x64` | Yes (`cli\leapmux.exe` on user `PATH`) |

</div>

## Standalone Binary & Docker

Need a headless Hub, a remote Worker on a Linux/GPU devbox, or a browser-only solo instance?

{{< tabs >}}
{{< tab name="macOS / Linux Tarball" selected=true >}}
Download the release archive for your OS and architecture from [GitHub Releases](https://github.com/leapmux/leapmux/releases), extract it, and start solo mode:

```sh
tar -xzf leapmux_<version>_<os>_<arch>.tar.gz
cd leapmux_<version>_<os>_<arch>
./leapmux up
```

macOS and Linux tarballs include both `leapmux` and its short `lmx` symlink. Open `http://127.0.0.1:4327` in your browser, and run `./leapmux down` to stop.
{{< /tab >}}
{{< tab name="Windows Zip" >}}
Download `leapmux_<version>_windows_amd64.zip` from [GitHub Releases](https://github.com/leapmux/leapmux/releases), extract it, and enter the extracted version folder:

```powershell
Expand-Archive .\leapmux_<version>_windows_amd64.zip -DestinationPath .\leapmux
cd .\leapmux\leapmux_<version>_windows_amd64
.\leapmux.exe up
```

Open `http://127.0.0.1:4327` in your browser, and run `.\leapmux.exe down` to stop.
{{< /tab >}}
{{< tab name="GitHub CLI (gh)" >}}
Download the latest release archive directly with the GitHub CLI (`gh`):

```sh
gh release download --repo leapmux/leapmux --pattern 'leapmux_*_linux_amd64.tar.gz'
```

Replace `linux_amd64.tar.gz` with `darwin_arm64.tar.gz`, `linux_arm64.tar.gz`, or `windows_amd64.zip` for your platform. While LeapMux is in pre-release, pass a tag from `gh release list --repo leapmux/leapmux` before `--pattern`.
{{< /tab >}}
{{< tab name="Docker" >}}
Run a single-container instance (Hub + Worker) with persistent storage:

```sh
docker run -d --name leapmux \
  -p 127.0.0.1:4327:4327 \
  -v leapmux-data:/data \
  -e LEAPMUX_MODE=dev \
  ghcr.io/leapmux/leapmux:latest
```

Use `LEAPMUX_MODE=dev` here because `dev` binds `:4327` inside the container, making `-p 127.0.0.1:4327:4327` reachable from your host. In contrast, `solo` binds `127.0.0.1:4327` loopback-only inside the container.

See [Installation](/docs/getting-started/installation/) and [Production Deployment Checklist](/docs/admin/production-checklist/) for reverse-proxy TLS and multi-user Hub deployments.
{{< /tab >}}
{{< /tabs >}}

## Next Steps

- [Quick Start](/docs/getting-started/quick-start/) — launch two coding agents in parallel git worktrees in under five minutes.
- [Concepts & Topologies](/docs/getting-started/concepts/) — choose between Solo Desktop, Solo Daemon, and Distributed Hub + remote Workers.
- [Supported Agents](/docs/reference/supported-agents/) — check all {{< agent-provider-count >}} coding agent providers and their E2E-verified capabilities.

</div>
