# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Two audiences carry equal weight:
- **Network and IT people:** engineers, sysadmins, support technicians and power users who troubleshoot connections and want ASN, loss, RTT, peering and MTR detail.
- **Everyday Windows users** who want to know what is using their internet.

The job is the same for both: on their own PC, right now, find which app is using the network, where the traffic goes, and whether the problem is the network, the path or the computer.

## Product Purpose

PacketTrail is a free, portable network traffic monitor for Windows. It shows:
- every app's live and historical download and upload;
- every remote IP with its owner (ASN), country and city, plus the hostname the app looked up;
- path quality: RTT, loss and retransmits from Windows' own TCP statistics.

It also provides MTR, traceroute, ping and TCP checks to any address, background monitors, and a System tab that rules the PC in or out.

Success means two things:
- someone with a slow or busy connection catches the cause in the act within a minute of starting the app;
- a technician gets numbers they can paste into a ticket.

## Positioning

PacketTrail is one self-contained executable with nothing to install and nothing sent to the cloud. It combines per-app bandwidth with network-operator context: who owns each IP, which networks and peering exchanges the traffic crosses, and path quality. That pairing is what an ISP network engineer reaches for. It is free with no account, ads or telemetry, and the source is open.

## Operating Context

- Runs from a folder, USB stick or network share. Its `data` folder sits next to the exe.
- Answering Yes to the administrator (UAC) prompt gives full per-app byte counts; answering No still shows who talks to whom.
- It opens its own window (WebView2) on Windows, and the default browser on Linux and macOS.
- It is used while something is happening, such as a slow game, a stalled call or a saturated link, and during support work.

## Capabilities and Constraints

- **Platforms:** Windows 10/11 x64 is the primary target. Linux (x64, ARM64) and macOS (Apple silicon, Intel) builds ship but are untested on real machines.
- **Lookups:** location and ASN come offline from the DB-IP Lite databases (CC BY 4.0), about 135 MB, downloaded once on first start.
- **What leaves the PC:** reverse DNS to the user's own DNS server, anonymous PeeringDB lookups by AS number, the tests the user starts, and that GeoIP download.
- **Signing:** the exe is not code-signed, so Windows SmartScreen may warn on first run.
- **Distribution:** the website is static HTML/CSS/JS on GitHub Pages from `docs/`, with no build step. Downloads are GitHub Releases with version-less asset names, linked as `releases/latest/download/<name>`.
- **Cost:** no paid services, hosting or certificates.
- **Licence:** MIT. Donations go through PayPal (paypal.me/matthewbrianburnett).

## Brand Commitments

- **Name:** PacketTrail.
- **Publisher and copyright:** Matt Burnett.
- **Look:** the website uses the app's own theme. That means neutral dark and light surfaces and the app's data colours: download blue, upload orange, and status green, yellow and red.
  - A slight glow goes on data (lines, dots, markers), never on text.
  - Controls are flat, GNOME/Adwaita-like, with no gradients or gloss.
- **Front page:** must use the "Ghost Fibers" animated background (React Bits, MIT + Commons Clause), recoloured to the app's theme.
- **Logo:** A routed P-shaped trail with a blue packet and orange hop, shared by the app, executable and website.

## Evidence on Hand

- Real app screenshots in `docs/assets/`: Applications (dark and light), Remote IPs, Monitors and System.
  - They are captured from the real dashboard using deterministic, synthetic demo fixtures, with no personal traffic or system identity.
  - Any new capture must leave out the publisher's work identity (work VPN endpoint, work ASN).
- **Absent, never to be invented:** testimonials, user or download counts, ratings, press, customers and benchmarks.

## Product Principles

1. **Show the cause, not just the symptom:** which app, which remote network, which path.
2. **Private and portable by default:** nothing to install, nothing sent anywhere the user didn't start.
3. **Speak both languages:** a plain answer first, with the engineer's detail one click away.
4. **Free and honest:** no upsell and no claims beyond what the app does.
