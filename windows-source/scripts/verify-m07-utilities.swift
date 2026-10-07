import Foundation
import AppKit

private final class Store { var values: [String: Any] = [:]; var writes = 0 }
/// Injected storage only: no call delegates to persistent UserDefaults. This checks
/// preference logic across AppSettings objects, NOT macOS cross-launch persistence.
private final class MemoryDefaults: UserDefaults {
    let store: Store
    init(_ store: Store) { self.store = store; super.init(suiteName: "workbuddy-m07-injected-only")! }
    override func set(_ value: Any?, forKey key: String) { store.values[key] = value; store.writes += 1 }
    override func integer(forKey key: String) -> Int { store.values[key] as? Int ?? 0 }
    override func bool(forKey key: String) -> Bool { store.values[key] as? Bool ?? false }
    override func string(forKey key: String) -> String? { store.values[key] as? String }
    override func dictionary(forKey key: String) -> [String: Any]? { store.values[key] as? [String: Any] }
}
@MainActor private final class Picker {
    var calls = 0
    var suspended = false
    var target: URL?
    var continuation: CheckedContinuation<URL?, Never>?
    func choose(_ name: String) async -> URL? {
        precondition(name == "workbuddy-diagnostics.json")
        calls += 1
        if suspended { return await withCheckedContinuation { continuation = $0 } }
        return target
    }
    func finish(_ url: URL?) { continuation?.resume(returning: url); continuation = nil }
}
@MainActor private final class ClearConfirmation {
    var approved = false
    var suspended = false
    var continuation: CheckedContinuation<Bool, Never>?
    func confirm() async -> Bool {
        if suspended { return await withCheckedContinuation { continuation = $0 } }
        return approved
    }
    func finish() { continuation?.resume(returning: true); continuation = nil }
}

@main struct UtilityChecks {
    @MainActor static func delayedClearCheck() async throws {
        let root = URL(fileURLWithPath: ProcessInfo.processInfo.environment["WB_TEST_ROOT"]!)
        precondition(root.path.hasPrefix("/private/tmp/"))
        let log = AppLog(directory: root.appendingPathComponent("logs"), maxFileBytes: 512, emitSystemLog: false)
        for source in AppLog.Source.allCases { for _ in 0..<12 { log.record(.processStarted, source: source) } }
        let directory = root.appendingPathComponent("logs/safe-events-v1")
        func files() throws -> [String: Data] {
            try Dictionary(uniqueKeysWithValues: FileManager.default.contentsOfDirectory(atPath: directory.path)
                .map { ($0, try Data(contentsOf: directory.appendingPathComponent($0))) })
        }
        func check(_ value: Bool, _ name: String) { precondition(value, name); print("PASS \(name)") }
        var confirmations: [CheckedContinuation<Bool, Never>] = []
        let utilities = DesktopUtilities(settings: AppSettings(defaults: MemoryDefaults(Store())), log: log,
            snapshot: { .init(gateway: .stopped, panel: .notStarted, apiPort: nil) },
            confirmClear: { await withCheckedContinuation { confirmations.append($0) } })
        let owner = NativeBridge.DocumentOwner()
        let before = try files()
        check(before.count == 10, "delayed confirmation fixture contains six archives and three current files")
        let started = ContinuousClock.now
        let first = Task { () -> Bool in
            let result = try await utilities.dispatch(action: "logs.clearArchives", payload: [:], owner: owner)
            return result["cancelled"] as? Bool == true && result["cleared"] as? Int == 0
        }
        let cancelled = try await first.value
        let elapsed = started.duration(to: .now)
        check(cancelled && elapsed >= .seconds(24) && elapsed < .seconds(30), "production 25-second native expiry returns cancellation before JS 30 seconds")
        check(try confirmations.count == 1 && files() == before, "expired confirmation leaves all log bytes unchanged")
        var secondFinished = false
        let second = Task { () -> Bool in
            let result = try await utilities.dispatch(action: "logs.clearArchives", payload: [:], owner: owner)
            secondFinished = true
            return result["cancelled"] as? Bool == true
        }
        for _ in 0..<100 { if confirmations.count == 2 { break }; await Task.yield() }
        check(confirmations.count == 2, "expiry releases busy for a fresh confirmation")
        confirmations[0].resume(returning: true)
        try await Task.sleep(for: .milliseconds(50))
        check(try !secondFinished && files() == before, "old affirmative callback neither deletes archives nor resolves new confirmation")
        do { _ = try await utilities.dispatch(action: "logs.clearArchives", payload: [:], owner: owner); preconditionFailure("new confirmation lost busy ownership") }
        catch { check((error as? DesktopUtilities.Failure) == .busy, "old callback cannot release new confirmation busy ownership") }
        confirmations[1].resume(returning: false)
        check(try await second.value, "new confirmation can cancel independently after old callback")
        let third = Task { () -> Int in
            let result = try await utilities.dispatch(action: "logs.clearArchives", payload: [:], owner: owner)
            return result["cleared"] as? Int ?? -1
        }
        for _ in 0..<100 { if confirmations.count == 3 { break }; await Task.yield() }
        precondition(confirmations.count == 3)
        confirmations[2].resume(returning: true)
        check(try await third.value == 6, "fresh timely confirmation still clears exactly six archives")
        print("M07_CLEAR_TIMEOUT_SELF_CHECK_PASS elapsed=\(elapsed)")
    }

    @MainActor static func main() async throws {
        if let mode = CommandLine.arguments.dropFirst().first {
            if mode == "login-autostart" {
                let settings = AppSettings(defaults: MemoryDefaults(Store()))
                precondition(!settings.shouldStartGateway(atLogin: false) && !settings.shouldStartGateway(atLogin: true))
                settings.autoStartGateway = true
                precondition(settings.shouldStartGateway(atLogin: false) && !settings.shouldStartGateway(atLogin: true))
                settings.autoStartGatewayAtLogin = true
                settings.autoStartGateway = false
                precondition(!settings.shouldStartGateway(atLogin: false) && settings.shouldStartGateway(atLogin: true))
                print("PASS manual and macOS login startup preferences stay independent")
                return
            }
            if mode == "clear-timeout" { try await delayedClearCheck(); return }
            if mode == "archives-contender" {
                let root = URL(fileURLWithPath: ProcessInfo.processInfo.environment["WB_TEST_ROOT"]!)
                precondition(root.path.hasPrefix("/private/tmp/"))
                let other = AppLog(directory: root.appendingPathComponent("logs"), maxFileBytes: 512, emitSystemLog: false)
                do { _ = try other.clearArchives(); preconditionFailure("second process cleared writer archives") }
                catch AppLog.ArchiveError.notWriter { print("PASS second process cannot clear archives held by active writer") }
                return
            }
            if mode == "cleanup-location-probe" {
                let defaults = UserDefaults(suiteName: "workbuddy-m07-location-check")!
                defaults.removeObject(forKey: "probe")
                precondition(defaults.synchronize())
                print("PASS removed only synthetic location-check probe key")
                return
            }
            guard let domain = ProcessInfo.processInfo.environment["WB_TEST_PREFERENCES_DOMAIN"],
                  domain.hasPrefix("workbuddy-m07-synthetic-"),
                  UUID(uuidString: String(domain.dropFirst("workbuddy-m07-synthetic-".count))) != nil else { fatalError("synthetic UUID suite required") }
            let defaults = UserDefaults(suiteName: domain)!
            if mode == "preferences-cleanup" {
                defaults.removePersistentDomain(forName: domain)
                precondition(defaults.synchronize())
                precondition(defaults.persistentDomain(forName: domain)?.isEmpty != false)
                print("PASS exclusive synthetic UUID suite removed")
                return
            }
            let settings = AppSettings(defaults: defaults)
            if mode == "preferences-write" {
                settings.theme = "dark"; settings.autoStartGateway = true; settings.hasCompletedFirstRun = true
                precondition(settings.setAccountNote(uid: "fake001", value: "synthetic note"))
                precondition(defaults.synchronize())
                print("PASS production AppSettings saved synthetic preferences")
            } else {
                precondition(mode == "preferences-read")
                precondition(settings.theme == "dark" && settings.autoStartGateway && settings.hasCompletedFirstRun
                             && settings.accountNotes["fake001"] == "synthetic note")
                print("PASS fresh process AppSettings recovered synthetic preferences")
            }
            return
        }
        let root = URL(fileURLWithPath: ProcessInfo.processInfo.environment["WB_TEST_ROOT"]!)
        precondition(root.path.hasPrefix("/private/tmp/"))
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let secret = "SYNTHETIC_SECRET_7af6 /Users/fictional/private?token=chat-body"
        let store = Store(), picker = Picker(), confirmation = ClearConfirmation()
        let settings = AppSettings(defaults: MemoryDefaults(store))
        let log = AppLog(directory: root.appendingPathComponent("logs"), maxFileBytes: 512, emitSystemLog: false)
        var time = Date()
        var opened: [URL] = []
        let utilities = DesktopUtilities(settings: settings, log: log,
            snapshot: { .init(gateway: .failed(reason: secret), panel: .failed(reason: secret), apiPort: 14567,
                              healthyAccounts: 0, totalAccounts: 0, proxyConfigured: true, apiKeyConfigured: true) },
            now: { time }, selectSaveURL: { await picker.choose($0) },
            openDirectory: { opened.append($0); return true }, confirmClear: { await confirmation.confirm() })
        let owner = NativeBridge.DocumentOwner()
        func check(_ value: Bool, _ name: String) { precondition(value, name); print("PASS \(name)") }
        func reject(_ action: String, _ payload: [String: Any], owner candidate: NativeBridge.DocumentOwner? = nil,
                    code: DesktopUtilities.Failure = .invalid_payload) async {
            do { _ = try await utilities.dispatch(action: action, payload: payload, owner: candidate ?? owner); preconditionFailure(action) }
            catch { check((error as? DesktopUtilities.Failure) == code, "reject \(action) \(code)") }
        }
        func call(_ action: String, _ payload: [String: Any] = [:], owner candidate: NativeBridge.DocumentOwner? = nil) async throws -> [String: Any] {
            try await utilities.dispatch(action: action, payload: payload, owner: candidate ?? owner)
        }
        func preview(owner candidate: NativeBridge.DocumentOwner? = nil) async throws -> String {
            let result = try await call("diagnostics.export", ["operation": "preview"], owner: candidate)
            check(result["files"] as? [String] == ["workbuddy-diagnostics.json"], "preview lists exact single file")
            return result["preview_id"] as! String
        }
        func awaitPicker() async {
            for _ in 0..<100 { if picker.continuation != nil { return }; await Task.yield() }
            preconditionFailure("picker not reached")
        }
        check(store.writes == 0, "preference load does not write storage")
        for theme in ["system", "light", "dark"] {
            _ = try await call("preferences.update", ["theme": theme])
            check(settings.theme == theme, "valid \(theme) theme")
        }
        _ = try await call("preferences.update", ["autoStartGatewayOnLaunch": true, "hasCompletedFirstRun": true,
                                                    "note": ["uid": "fake001", "value": secret]])
        let reloaded = AppSettings(defaults: MemoryDefaults(store))
        check(reloaded.theme == "dark" && reloaded.autoStartGateway && reloaded.hasCompletedFirstRun
              && reloaded.accountNotes["fake001"] == secret, "preferences persist across AppSettings objects with injected defaults")
        let writes = store.writes
        for payload: [String: Any] in [["apiPort": 7863], ["theme": "bad"], ["theme": NSNull()],
            ["autoStartGatewayOnLaunch": 1], ["autoStartGatewayAtLogin": 1], ["openLoginItems": false],
            ["openLoginItems": true, "theme": "light"], ["hasCompletedFirstRun": "true"], ["note": ["uid": "../escape", "value": "x"]],
            ["note": ["uid": "fake001", "value": String(repeating: "x", count: 257)]],
            ["theme": "light", "note": ["uid": "fake001", "value": 12]], ["unknown": true], [:]] {
            await reject("preferences.update", payload)
        }
        check(store.writes == writes && settings.theme == "dark", "invalid patch cannot partially persist preferences")
        await reject("preferences.get", ["path": "/tmp/secrets"])
        let revoked = NativeBridge.DocumentOwner(); revoked.invalidate()
        await reject("preferences.update", ["theme": "light"], owner: revoked, code: .stale_document)
        check(store.writes == writes, "revoked owner cannot change preferences")
        for index in 1..<200 { precondition(settings.setAccountNote(uid: String(format: "fake%03d", index + 1), value: "note")) }
        await reject("preferences.update", ["note": ["uid": "new999", "value": "over-cap"]])
        settings.removeAccountNote(uid: "fake001")
        check(settings.accountNotes["fake001"] == nil, "account delete hook removes only matching local note")
        _ = try await call("preferences.update", ["note": ["uid": "fake001", "value": secret]])
        _ = try await call("preferences.update", ["note": ["uid": "fake200", "value": ""]])
        check(settings.accountNotes["fake200"] == nil, "empty note deletes local label")

        log.info(secret)
        for _ in 0..<230 { log.record(.processStarted, source: .panel) }
        let tail = try await call("logs.tail", ["source": "panel", "limit": 2])
        check((tail["entries"] as! [[String: Any]]).count == 2, "logs tail enforces requested limit")
        _ = try await call("logs.tail", ["source": "panel", "run_id": tail["run_id"]!, "cursor": tail["cursor"]!, "limit": 200])
        for payload: [String: Any] in [["source": "../file"], ["source": "app", "path": "/tmp/a"],
            ["source": "app", "limit": true], ["source": "app", "limit": 0], ["source": "app", "limit": 201],
            ["source": "app", "limit": 1.5], ["source": "app", "cursor": -1], ["source": "app", "cursor": 0],
            ["source": "app", "run_id": "bad", "cursor": 0], ["source": "app", "cursor": "0"],
            ["source": "app", "run_id": tail["run_id"]!, "cursor": 9_007_199_254_740_992]] {
            await reject("logs.tail", payload)
        }
        await reject("logs.tail", ["source": "app"], owner: revoked, code: .stale_document)
        await reject("preferences.update", ["note": ["uid": "fake001", "value": String(repeating: "x", count: 5000)]])
        await reject("diagnostics.export", ["operation": "preview", "path": "/tmp/arbitrary"])
        let id = try await preview()
        let unrelated = NativeBridge.DocumentOwner()
        await reject("diagnostics.export", ["operation": "cancel", "preview_id": id], owner: unrelated, code: .stale_preview)
        _ = try await call("diagnostics.export", ["operation": "cancel", "preview_id": id])
        await reject("diagnostics.export", ["operation": "save", "preview_id": id], code: .stale_preview)
        check(picker.calls == 0, "cancel before save never presents dialog")
        let cancelID = try await preview()
        let cancelled = try await call("diagnostics.export", ["operation": "save", "preview_id": cancelID])
        check(cancelled["cancelled"] as? Bool == true, "save-panel cancellation returns cancelled")
        let target = root.appendingPathComponent("diagnostic.json")
        check(!FileManager.default.fileExists(atPath: target.path), "cancelled dialog writes no file")
        let expired = try await preview()
        time = time.addingTimeInterval(121)
        await reject("diagnostics.export", ["operation": "save", "preview_id": expired], code: .stale_preview)

        picker.suspended = true
        let changedOwner = NativeBridge.DocumentOwner()
        let lateID = try await preview(owner: changedOwner)
        let late = Task { _ = try await call("diagnostics.export", ["operation": "save", "preview_id": lateID], owner: changedOwner) }
        await awaitPicker()
        changedOwner.invalidate(); utilities.invalidate(owner: changedOwner)
        picker.finish(target)
        do { _ = try await late.value; preconditionFailure("old owner saved") }
        catch { check((error as? DesktopUtilities.Failure) == .stale_document, "old owner cannot save late picker selection") }
        check(!FileManager.default.fileExists(atPath: target.path), "revoked dialog leaves no output")
        let spaID = try await preview()
        let spa = Task { _ = try await call("diagnostics.export", ["operation": "save", "preview_id": spaID]) }
        await awaitPicker()
        _ = try await call("diagnostics.export", ["operation": "cancel", "preview_id": spaID])
        picker.finish(target)
        do { _ = try await spa.value; preconditionFailure("cancelled SPA saved") }
        catch { check((error as? DesktopUtilities.Failure) == .cancelled, "SPA cancel defeats late picker selection") }
        check(!FileManager.default.fileExists(atPath: target.path), "SPA cancellation writes no file")
        let waitingID = try await preview()
        let waiting = Task { _ = try await call("diagnostics.export", ["operation": "save", "preview_id": waitingID]) }
        await awaitPicker(); time = time.addingTimeInterval(121); picker.finish(target)
        do { _ = try await waiting.value; preconditionFailure("expired dialog saved") }
        catch { check((error as? DesktopUtilities.Failure) == .cancelled, "expiry during dialog defeats late selection") }
        picker.suspended = false; picker.target = target
        let saveID = try await preview()
        let saved = try await call("diagnostics.export", ["operation": "save", "preview_id": saveID])
        check(saved["saved"] as? Bool == true && saved["path"] == nil, "successful export returns confirmation without path")
        let bytes = try Data(contentsOf: target)
        let text = String(decoding: bytes, as: UTF8.self)
        check(!text.contains(secret) && !text.contains("/Users/") && !text.contains("fake001") && !text.contains("token="),
              "diagnostic excludes error/body/path/notes/UID content")
        let json = try JSONSerialization.jsonObject(with: bytes) as! [String: Any]
        check(Set(json.keys) == Set(["schema", "about", "status", "safe_configuration", "logs"]), "diagnostic top-level whitelist")
        let logs = json["logs"] as! [String: [[String: Any]]]
        check(logs["panel"]!.count == 200, "diagnostic tail stays at 200 per source")
        check((json["status"] as! [String: Any])["gateway"] as? String == "failed", "diagnostic keeps fixed error state without reason")
        check((try FileManager.default.contentsOfDirectory(atPath: root.path)).filter { $0.hasSuffix(".json") } == ["diagnostic.json"],
              "export creates exactly one selected JSON with no side copy")
        for kind in ["data", "logs"] {
            let result = try await call("desktop.openDirectory", ["kind": kind])
            check(result["opened"] as? Bool == true && result["path"] == nil, "fixed \(kind) directory opens without returning path")
        }
        check(opened == [AppPaths.dataDirectory, AppPaths.logDirectory], "directory opener receives only production fixed locations")
        await reject("desktop.openDirectory", ["kind": "../secrets"])
        await reject("desktop.openDirectory", ["kind": "logs", "path": "/tmp/other"])
        await reject("desktop.openDirectory", ["kind": "data"], owner: revoked, code: .stale_document)
        check(opened.count == 2, "invalid directory requests never invoke opener")

        for source in AppLog.Source.allCases { for _ in 0..<12 { log.record(.processStarted, source: source) } }
        let directory = root.appendingPathComponent("logs/safe-events-v1")
        let archiveNames = AppLog.Source.allCases.flatMap { source in ["\(source.rawValue).log.1", "\(source.rawValue).log.2"] }
        func files() throws -> [String: Data] {
            try Dictionary(uniqueKeysWithValues: FileManager.default.contentsOfDirectory(atPath: directory.path)
                .map { ($0, try Data(contentsOf: directory.appendingPathComponent($0))) })
        }
        let before = try files()
        check(archiveNames.allSatisfy { before[$0] != nil }, "small bounded fixture creates exactly six archives")
        await reject("logs.clearArchives", ["path": "/tmp/a"])
        let cancelledClear = try await call("logs.clearArchives")
        check(try cancelledClear["cancelled"] as? Bool == true && files() == before, "native confirmation cancel leaves every log byte unchanged")
        confirmation.suspended = true
        let clearOwner = NativeBridge.DocumentOwner()
        let pendingClear = Task { _ = try await call("logs.clearArchives", owner: clearOwner) }
        for _ in 0..<100 { if confirmation.continuation != nil { break }; await Task.yield() }
        precondition(confirmation.continuation != nil)
        await reject("logs.clearArchives", [:], code: .busy)
        clearOwner.invalidate(); utilities.invalidate(owner: clearOwner); confirmation.finish()
        do { try await pendingClear.value; preconditionFailure("revoked owner cleared archives") }
        catch { check((error as? DesktopUtilities.Failure) == .stale_document, "late clear confirmation cannot authorize revoked owner") }
        check(try files() == before, "revoked confirmation leaves every log byte unchanged")
        confirmation.suspended = false; confirmation.approved = true
        let contender = Process()
        contender.executableURL = URL(fileURLWithPath: CommandLine.arguments[0])
        contender.arguments = ["archives-contender"]
        try contender.run(); contender.waitUntilExit()
        check(try contender.terminationStatus == 0 && files() == before, "cross-process lock refusal leaves archives intact")
        let lastArchive = directory.appendingPathComponent("gateway.log.2")
        let external = root.appendingPathComponent("synthetic-untouched")
        try Data("untouched".utf8).write(to: external)
        try FileManager.default.removeItem(at: lastArchive)
        try FileManager.default.createSymbolicLink(at: lastArchive, withDestinationURL: external)
        await reject("logs.clearArchives", [:], code: .cleanup_failed)
        check(try archiveNames.dropLast().allSatisfy { FileManager.default.fileExists(atPath: directory.appendingPathComponent($0).path) }
              && Data(contentsOf: external) == Data("untouched".utf8), "symlink rejection preflights all archives before any deletion")
        try FileManager.default.removeItem(at: lastArchive)
        try before["gateway.log.2"]!.write(to: lastArchive)
        try FileManager.default.setAttributes([.posixPermissions: 0o644], ofItemAtPath: lastArchive.path)
        await reject("logs.clearArchives", [:], code: .cleanup_failed)
        check(archiveNames.allSatisfy { FileManager.default.fileExists(atPath: directory.appendingPathComponent($0).path) }, "abnormal archive permissions reject without partial cleanup")
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: lastArchive.path)
        let oldRaw = root.appendingPathComponent("logs/desktop.log")
        try Data(secret.utf8).write(to: oldRaw)
        let tailBefore = try log.tail(source: .app)
        let cleared = try await call("logs.clearArchives")
        check(cleared["cleared"] as? Int == 6, "clear removes only six fixed archives")
        let after = try files()
        check(after.count == 4 && AppLog.Source.allCases.allSatisfy { after["\($0.rawValue).log"] == before["\($0.rawValue).log"] },
              "clear preserves current file bytes and writer lock")
        check(try log.tail(source: .app).entries.count == tailBefore.entries.count && Data(contentsOf: oldRaw) == Data(secret.utf8),
              "clear preserves memory tail and untouched synthetic legacy raw log")
        for source in AppLog.Source.allCases { for _ in 0..<12 { log.record(.processStarted, source: source) } }
        let rotated = try files()
        check(rotated.count == 10 && rotated.values.allSatisfy { $0.count <= 512 }, "all sources keep rotating within capacity after archive clear")
        check(try AppLog.Source.allCases.allSatisfy { !(try log.tail(source: $0)).entries.contains { $0.event == .diskLoggingDisabled } },
              "archive operations never disable active writer")
        print("M07_UTILITIES_SELF_CHECK_PASS (injected defaults scope)")
    }
}
