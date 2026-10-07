import AppKit
import Foundation

@main
enum VerifyInstallerHelper {
    static let fakeBundleID = "cn.workbuddy.installer.synthetic-quit-test"
    enum Failure: Error { case syntheticAppDidNotRegister, normalQuitFailed, syntheticAppStillRunning }

    static func main() throws {
        if Bundle.main.bundleIdentifier == fakeBundleID {
            let app = NSApplication.shared
            app.setActivationPolicy(.prohibited)
            app.finishLaunching()
            let readyPath = CommandLine.arguments[2]
            Timer.scheduledTimer(withTimeInterval: 0.1, repeats: false) { _ in
                _ = FileManager.default.createFile(atPath: readyPath, contents: Data())
            }
            app.run()
            return
        }
        let unrelated: [String: Any] = [
            "GUID": 10,
            "tile-data": ["bundle-identifier": "com.apple.Safari", "file-label": "Safari",
                          "file-data": ["_CFURLString": "file:///Applications/Safari.app/", "_CFURLStringType": 15]],
        ]
        let byID: [String: Any] = [
            "GUID": 20, "tile-type": "file-tile", "alias-data": Data([1, 2]),
            "tile-data": ["bundle-identifier": InstallerHelper.bundleID, "file-label": "Old label",
                          "book": Data([3, 4]), "keep": "metadata",
                          "file-data": ["_CFURLString": "file:///Volumes/Old/App.app/", "_CFURLStringType": 15,
                                        "bookmark": Data([5, 6])]],
        ]
        let byOldPath: [String: Any] = [
            "GUID": 30,
            "tile-data": ["file-data": ["_CFURLString": "file:///Users/example/Downloads/WorkBuddyDesktop.app/",
                                          "_CFURLStringType": 15]],
        ]
        let byOldName: [String: Any] = [
            "GUID": 40,
            "tile-data": ["file-data": ["_CFURLString": "/Volumes/Old/WB-PMM.app/", "_CFURLStringType": 15]],
        ]
        let unrelatedSameName: [String: Any] = [
            "GUID": 50,
            "tile-data": ["bundle-identifier": "example.other-app",
                          "file-data": ["_CFURLString": "file:///Applications/WorkBuddyDesktop.app/", "_CFURLStringType": 15]],
        ]
        let original: [Any] = [unrelated, byID, "unknown entry", byOldPath, byOldName, unrelatedSameName]
        let result = DockMigration.migrate(original)
        precondition(result.changed && result.entries.count == original.count)
        precondition(NSDictionary(dictionary: result.entries[0] as! [String: Any]).isEqual(to: unrelated))
        precondition(NSDictionary(dictionary: result.entries[5] as! [String: Any]).isEqual(to: unrelatedSameName))
        precondition(result.entries[2] as? String == "unknown entry")
        for (index, guid) in [(1, 20), (3, 30), (4, 40)] {
            let entry = result.entries[index] as! [String: Any]
            let tile = entry["tile-data"] as! [String: Any]
            let file = tile["file-data"] as! [String: Any]
            precondition(entry["GUID"] as? Int == guid)
            precondition(tile["bundle-identifier"] as? String == InstallerHelper.bundleID)
            precondition(tile["file-label"] as? String == "WB-PMM")
            precondition(file["_CFURLString"] as? String == DockMigration.targetURL)
            precondition(file["_CFURLStringType"] as? Int == 15)
            precondition(entry["alias-data"] == nil && tile["book"] == nil && file["bookmark"] == nil)
        }
        let preserved = (result.entries[1] as! [String: Any])["tile-data"] as! [String: Any]
        precondition(preserved["keep"] as? String == "metadata")
        precondition(!DockMigration.migrate(result.entries).changed) // Repeat installs are no-ops.
        precondition(!DockMigration.migrate([]).changed) // Never add an unpinned icon.
        precondition(!DockMigration.migrate([unrelated]).changed)
        precondition(DockMigration.migrate([byOldPath]).changed) // Same transform covers recent-apps.
        precondition(OtherUserAppScan.isOurApp(path: "/Applications/WB-PMM.app/Contents/MacOS/WorkBuddyDesktop"))
        precondition(OtherUserAppScan.isOurApp(path: "/Users/example/WorkBuddyDesktop.app/Contents/MacOS/WorkBuddyDesktop"))
        precondition(!OtherUserAppScan.isOurApp(path: "/Applications/Other.app/Contents/MacOS/Other"))
        try verifyNormalQuit()
        print("installer helper Dock transform and normal quit: PASS")
    }

    static func verifyNormalQuit() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("wb-pmm-helper-\(UUID().uuidString)")
        let app = root.appendingPathComponent("SyntheticQuit.app")
        let ready = root.appendingPathComponent("ready")
        let executable = app.appendingPathComponent("Contents/MacOS/SyntheticQuit")
        try FileManager.default.createDirectory(at: executable.deletingLastPathComponent(), withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let info: [String: Any] = ["CFBundleIdentifier": fakeBundleID, "CFBundleExecutable": "SyntheticQuit",
                                   "CFBundlePackageType": "APPL", "LSUIElement": true]
        let plist = try PropertyListSerialization.data(fromPropertyList: info, format: .xml, options: 0)
        try plist.write(to: app.appendingPathComponent("Contents/Info.plist"))
        try FileManager.default.copyItem(at: URL(fileURLWithPath: CommandLine.arguments[0]), to: executable)
        let child = Process()
        child.executableURL = executable
        child.arguments = ["--ready-file", ready.path]
        child.standardOutput = FileHandle.nullDevice
        child.standardError = FileHandle.nullDevice
        try child.run()
        defer { if child.isRunning { child.terminate(); child.waitUntilExit() } }
        let deadline = Date().addingTimeInterval(5)
        while Date() < deadline,
              (!FileManager.default.fileExists(atPath: ready.path) ||
               NSRunningApplication.runningApplications(withBundleIdentifier: fakeBundleID).isEmpty) {
            RunLoop.current.run(until: Date().addingTimeInterval(0.1))
        }
        guard FileManager.default.fileExists(atPath: ready.path),
              !NSRunningApplication.runningApplications(withBundleIdentifier: fakeBundleID).isEmpty else {
            throw Failure.syntheticAppDidNotRegister
        }
        guard InstallerHelper.quitRunningApplications(bundleID: fakeBundleID, timeout: 5) else {
            throw Failure.normalQuitFailed
        }
        while child.isRunning && Date() < deadline.addingTimeInterval(5) {
            RunLoop.current.run(until: Date().addingTimeInterval(0.1))
        }
        guard !child.isRunning else { throw Failure.syntheticAppStillRunning }
    }
}
