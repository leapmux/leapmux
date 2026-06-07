---
title: Legal
type: docs
weight: 28
---

License, trademark, third-party attribution, and privacy information for LeapMux. This page is informational and does not replace the full license text or professional legal advice.

## License

LeapMux is licensed under the **Functional Source License, Version 1.1, Apache 2.0 Future License (FSL-1.1-ALv2)**. In short:

- You can use, modify, and distribute the software.
- There are certain limitations on competitive use.
- The license automatically converts to Apache 2.0 two years after each release is first made available.

This summary is not a substitute for the license itself — see the full [`LICENSE.md`](https://github.com/leapmux/leapmux/blob/main/LICENSE.md) for the authoritative terms.

LeapMux is © Event Loop, Inc.

## Trademarks and disclaimer

All product names, logos, and trademarks are the property of their respective owners. LeapMux is not affiliated with, endorsed by, or sponsored by Anomaly, Anthropic, Anysphere, Apple, Block, Cognition, Don Ho, Earendil, GitHub, Google, JetBrains, Kilo Code, Microsoft, OpenAI, Sublime HQ, Zed Industries, or any other third party. Coding agent, editor, and IDE icons are used solely to indicate compatibility and are reproduced here for identification purposes only.

## Third-party licenses

LeapMux bundles open-source components, each under its own license. The complete list of dependencies and their license texts is generated into [`NOTICE.md`](https://github.com/leapmux/leapmux/blob/main/NOTICE.md) in the repository (also shipped as `NOTICE.html` and bundled into the release artifacts).

## Privacy

LeapMux is **self-hosted software**, not a hosted service. You — or your organization — run the Hub and Workers; the LeapMux project does not operate a central service that receives your data, and the software contains no third-party analytics or "phone-home" telemetry. The only metrics LeapMux emits are exposed on a local Prometheus `/metrics` endpoint on the Hub, scraped only by infrastructure you control.

What data is stored, where it lives, and what is end-to-end encrypted is described in [Security & Threat Model](/docs/23-security-and-threat-model/) and [Encryption & Data](/docs/22-encryption-and-data/). Because LeapMux is self-hosted, the privacy and data-handling practices of any particular deployment are determined by whoever operates that Hub.

> **Note:** If [Event Loop, Inc.](https://eventloop.io/) offers a hosted version of LeapMux, that service's privacy policy is provided separately and governs data you submit to it. This page describes the self-hosted open-source software.
