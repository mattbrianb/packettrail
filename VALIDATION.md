# PacketTrail validation

PacketTrail is Matthew Burnett's free, portable network monitor. Donations are optional.

## Build and identity

- Go module: packettrail; entry point: cmd/packettrail.
- Dashboard: http://127.0.0.1:47810/.
- Windows executable metadata: PacketTrail 1.2.0, Matthew Burnett.
- History: packettrail.db; demo history: packettrail-demo.db.
- The SVG and raster icons share a routed P-shaped trail, blue packet and orange hop.
- Source, website and downloads are maintained in the PacketTrail repository.

## Checks

Run build.ps1 for Go vet, tests and Windows x64, Linux x64/ARM64 and macOS Intel/Apple silicon builds. Windows is the primary platform; cross-compilation does not prove runtime support elsewhere.

The live DNS-cache test skips when Windows exposes no cache table and no error code. Full Windows traffic accounting requires administrator rights.

Website screenshots render the dashboard with deterministic synthetic fixtures and are labelled Demo data. They contain no personal traffic, hostnames or process paths. The website retains its dark/light themes, Plain/Engineer view switch, animated traffic demo and optional PayPal donation link.
