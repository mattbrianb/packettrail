# PacketTrail development

PacketTrail is Matt Burnett's free, portable network monitor with optional donations.

- Read PRODUCT.md for product constraints and DESIGN.md for the app and website's visual rules.
- The Go entry point is cmd/packettrail. The embedded dashboard is internal/web/static; docs contains the static download website.
- Keep the PacketTrail name, module imports, platform identities, capture session, build artifacts and release URLs consistent.
- Default dashboard: http://127.0.0.1:47810/. Preserve loopback-only binding and cross-site request protections.
- PacketTrail uses packettrail.db and packettrail-demo.db. Keep live and demo history separate.
- Preserve Matt Burnett's copyright and all third-party licence notices.
- Keep the SVG logo geometry and tools/mkicon raster renderer in sync. Regenerate winres/icon.png, internal/platform/assets/icon.png, docs/assets/icon.png and the Windows resource object when changing it.
- Run go vet ./... and go test ./... for Go changes. build.ps1 builds Windows x64, Linux x64/ARM64 and macOS Intel/Apple silicon; release.ps1 packages Windows and Linux.
- Windows is the primary supported platform. Cross-compilation does not establish runtime support on Linux or macOS.
- Never commit runtime databases, downloaded GeoIP files, logs, browser profiles, toolchains or build output.

- For website work, preserve its existing layout, Ghost Fibers background, Plain/Engineer switch, themes and optional donation links. `node tools/screenshots.mjs` regenerates sanitised dashboard captures and checks desktop/mobile website layouts.
