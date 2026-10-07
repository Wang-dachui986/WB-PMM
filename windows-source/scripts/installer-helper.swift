import AppKit
import CoreFoundation
import Foundation
import Darwin

// Run as the console user. Only these two commands may touch that user's session.
enum InstallerHelper {
    static let bundleID = "cn.workbuddy.desktop"
    static var dockDomain: CFString { "com.apple.dock" as CFString }
    static let quitTimeout: TimeInterval = 20
    static let dockTimeout: TimeInterval = 5

    static func prepare() -> Bool {
        guard OtherUserAppScan.clear() else { return false }
        return quitRunningApplications(bundleID: bundleID, timeout: quitTimeout) && OtherUserAppScan.clear()
    }

    static func quitRunningApplications(bundleID: String, timeout: TimeInterval) -> Bool {
        let running = NSRunningApplication.runningApplications(withBundleIdentifier: bundleID)
            .filter { !$0.isTerminated }
        guard running.allSatisfy({ OtherUserAppScan.owner(of: $0.processIdentifier) == geteuid() }) else { return false }
        for app in running { _ = app.terminate() } // Normal quit; never force terminate.
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            let remaining = NSRunningApplication.runningApplications(withBundleIdentifier: bundleID)
                .contains { !$0.isTerminated }
            if !remaining {
                return !NSRunningApplication.runningApplications(withBundleIdentifier: bundleID).contains { !$0.isTerminated }
            }
            RunLoop.current.run(until: min(Date().addingTimeInterval(0.1), deadline))
        }
        return false
    }

    static func dock() -> Bool {
        guard CFPreferencesAppSynchronize(dockDomain) else { return false }
        var changes: [(CFString, [Any])] = []
        for name in ["persistent-apps", "recent-apps"] {
            let key = name as CFString
            guard let original = CFPreferencesCopyAppValue(key, dockDomain) else { continue }
            guard let entries = original as? [Any] else { return false }
            let result = DockMigration.migrate(entries)
            if result.changed { changes.append((key, result.entries)) }
        }
        guard !changes.isEmpty else { return true }
        for (key, entries) in changes {
            CFPreferencesSetAppValue(key, entries as CFPropertyList, dockDomain)
        }
        guard CFPreferencesAppSynchronize(dockDomain) else { return false }

        // Dock reads its pins on launch. Ask only this user's Dock to quit normally;
        // launchd brings it back with the synchronized preference.
        let docks = NSRunningApplication.runningApplications(withBundleIdentifier: "com.apple.dock")
            .filter { !$0.isTerminated && OtherUserAppScan.owner(of: $0.processIdentifier) == geteuid() }
        let originalPIDs = Set(docks.map(\.processIdentifier))
        for app in docks { _ = app.terminate() }
        let deadline = Date().addingTimeInterval(dockTimeout)
        let signalAfter = Date().addingTimeInterval(1)
        var signaled = false
        while Date() < deadline {
            let remaining = NSRunningApplication.runningApplications(withBundleIdentifier: "com.apple.dock")
                .filter { originalPIDs.contains($0.processIdentifier) && !$0.isTerminated }
            if remaining.isEmpty { return true }
            if !signaled && Date() >= signalAfter {
                signaled = true
                for app in remaining {
                    let pid = app.processIdentifier
                    guard OtherUserAppScan.owner(of: pid) == geteuid(),
                          NSRunningApplication.runningApplications(withBundleIdentifier: "com.apple.dock")
                            .contains(where: { $0.processIdentifier == pid && !$0.isTerminated }) else { return false }
                    if kill(pid, SIGTERM) != 0 && errno != ESRCH { return false } // Graceful Dock refresh, never SIGKILL.
                }
            }
            RunLoop.current.run(until: min(Date().addingTimeInterval(0.1), deadline))
        }
        return !NSRunningApplication.runningApplications(withBundleIdentifier: "com.apple.dock")
            .contains { originalPIDs.contains($0.processIdentifier) && !$0.isTerminated }
    }
}

// libproc enumerates all users' PIDs. A matching executable owned by another
// user blocks replacement; only this user's AppKit instances receive quit.
enum OtherUserAppScan {
    static func owner(of pid: pid_t) -> uid_t? {
        var info = proc_bsdinfo()
        let size = Int32(MemoryLayout<proc_bsdinfo>.size)
        guard proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, size) == size else { return nil }
        return info.pbi_uid
    }

    static func clear() -> Bool {
        let estimate = proc_listallpids(nil, 0)
        guard estimate > 0 else { return false }
        var pids = [pid_t](repeating: 0, count: Int(estimate) + 128)
        let count = proc_listallpids(&pids, Int32(pids.count * MemoryLayout<pid_t>.stride))
        guard count >= 0, count < pids.count else { return false }
        for pid in pids.prefix(Int(count)) where pid > 0 {
            var name = [CChar](repeating: 0, count: 256)
            guard proc_name(pid, &name, UInt32(name.count)) > 0 else { continue }
            // Darwin may truncate process names, so accept both spellings.
            let processName = String(decoding: name.prefix { $0 != 0 }.map { UInt8(bitPattern: $0) }, as: UTF8.self)
            guard processName == "WorkBuddyDesktop" || processName == "WorkBuddyDeskto" else { continue }
            guard let uid = owner(of: pid) else {
                if errno == ESRCH { continue }
                return false
            }
            guard uid != geteuid() else { continue }
            var path = [CChar](repeating: 0, count: 4096)
            guard proc_pidpath(pid, &path, UInt32(path.count)) > 0 else {
                if errno == ESRCH { continue }
                return false
            }
            let executablePath = String(decoding: path.prefix { $0 != 0 }.map { UInt8(bitPattern: $0) }, as: UTF8.self)
            guard !isOurApp(path: executablePath) else { return false }
        }
        return true
    }

    static func isOurApp(path: String) -> Bool {
        let executable = URL(fileURLWithPath: path)
        guard executable.lastPathComponent == "WorkBuddyDesktop",
              executable.deletingLastPathComponent().lastPathComponent == "MacOS" else { return false }
        let contents = executable.deletingLastPathComponent().deletingLastPathComponent()
        guard contents.lastPathComponent == "Contents" else { return false }
        let app = contents.deletingLastPathComponent()
        guard app.pathExtension == "app" else { return false }
        if ["WB-PMM.app", "WorkBuddyDesktop.app"].contains(app.lastPathComponent) { return true }
        return Bundle(url: app)?.bundleIdentifier == InstallerHelper.bundleID
    }
}

// Pure transform so tests never read or change the real Dock preference.
enum DockMigration {
    static let targetPath = "/Applications/WB-PMM.app"
    static let targetURL = URL(fileURLWithPath: targetPath, isDirectory: true).absoluteString
    private static let oldNames: Set<String> = ["WB-PMM.app", "WorkBuddyDesktop.app"]
    private static let referenceKeys: Set<String> = ["book", "bookmark", "bookmark-data", "alias", "alias-data"]

    static func migrate(_ entries: [Any]) -> (entries: [Any], changed: Bool) {
        var changed = false
        let migrated: [Any] = entries.map { raw in
            guard let entry = raw as? [String: Any], let tile = entry["tile-data"] as? [String: Any],
                  matches(entry: entry, tile: tile) else { return raw }
            var nextTile = tile.filter { !referenceKeys.contains($0.key.lowercased()) }
            nextTile["file-data"] = ["_CFURLString": targetURL, "_CFURLStringType": 15]
            nextTile["bundle-identifier"] = InstallerHelper.bundleID
            nextTile["file-label"] = "WB-PMM"
            var nextEntry = entry.filter { !referenceKeys.contains($0.key.lowercased()) }
            nextEntry["tile-data"] = nextTile
            if !NSDictionary(dictionary: entry).isEqual(to: nextEntry) { changed = true }
            return nextEntry
        }
        return (migrated, changed)
    }

    private static func matches(entry: [String: Any], tile: [String: Any]) -> Bool {
        if let declaredID = tile["bundle-identifier"] ?? entry["bundle-identifier"] {
            return declaredID as? String == InstallerHelper.bundleID
        }
        guard let file = tile["file-data"] as? [String: Any],
              let location = file["_CFURLString"] as? String else { return false }
        let url: URL
        if location.hasPrefix("file://") {
            guard let parsed = URL(string: location), parsed.isFileURL else { return false }
            url = parsed
        } else if location.hasPrefix("/") {
            url = URL(fileURLWithPath: location)
        } else { return false }
        return oldNames.contains(url.standardizedFileURL.lastPathComponent)
    }
}

#if !INSTALLER_HELPER_TEST
@main
enum InstallerHelperMain {
    static func main() {
        guard geteuid() != 0 else {
            FileHandle.standardError.write(Data("installer helper must run as the console user\n".utf8))
            exit(77)
        }
        guard CommandLine.arguments.count == 2 else { exit(64) }
        switch CommandLine.arguments[1] {
        case "prepare": exit(InstallerHelper.prepare() ? 0 : 2)
        case "dock": exit(InstallerHelper.dock() ? 0 : 3)
        default: exit(64)
        }
    }
}
#endif
