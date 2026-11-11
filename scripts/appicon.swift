// Writes the icon macOS shows for a file or app bundle (what Finder and the
// Dock use) to a PNG, so a test can look at it.
//   swift scripts/appicon.swift /Applications/PacketTrail.app out.png
import AppKit

let args = CommandLine.arguments
guard args.count == 3 else {
    FileHandle.standardError.write("usage: appicon.swift <file-or-app> <out.png>\n".data(using: .utf8)!)
    exit(2)
}
let size = 256
let icon = NSWorkspace.shared.icon(forFile: args[1])
let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: size, pixelsHigh: size, bitsPerSample: 8,
                           samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB,
                           bytesPerRow: 0, bitsPerPixel: 0)!
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
icon.draw(in: NSRect(x: 0, y: 0, width: size, height: size))
NSGraphicsContext.restoreGraphicsState()
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: args[2]))
print("wrote \(args[2])")
