# Native webview libraries

These are the prebuilt `libwebview` shared libraries for macOS and Linux. They draw PacketTrail's native window through the system WebKit.

- **Source:** [webview/webview](https://github.com/webview/webview) 0.12.0, MIT licence, Copyright (c) 2017 Serge Zaitsev and the webview contributors.
- **Built by:** the public CI of [abemedia/go-webview](https://github.com/abemedia/go-webview) (MIT, Copyright (c) 2025 Adam Bouqdib).
- **Copied from:** the go-webview module, version `v0.0.0-20250327021345-7b06ad397f16`, folder `embedded/`.

| File | Used on | Needs at runtime |
|---|---|---|
| `darwin_arm64/libwebview.dylib` | macOS, Apple silicon | WebKit (always present) |
| `darwin_amd64/libwebview.dylib` | macOS, Intel | WebKit (always present) |
| `linux_amd64/libwebview.so` | Linux x64 | GTK 3 and WebKitGTK 4.1 (`libwebkit2gtk-4.1-0`) |
| `linux_arm64/libwebview.so` | Linux ARM64 | GTK 3 and WebKitGTK 4.1 |

PacketTrail embeds the file for its platform. At runtime it writes it to its own data folder (`data/lib`), checks that it matches the embedded bytes, and loads it with purego, so no cgo is needed.

If it cannot load, for example because WebKitGTK is missing, PacketTrail opens the dashboard in a Chromium-based browser's app window, or else the default browser.

To update these files, copy them from a newer go-webview release and update the version above.
