import Foundation
import AppKit
import CoreFoundation
import UniformTypeIdentifiers
import ServiceManagement

/// Fixed desktop commands. The bridge supplies its already-validated document owner.
@MainActor final class DesktopUtilities {
    static let commands = ["logs.tail", "preferences.get", "preferences.update", "diagnostics.export", "desktop.openDirectory", "logs.clearArchives"]
    enum Failure: String, Error { case invalid_payload, stale_document, stale_preview, busy, cancelled, save_failed, cleanup_failed, unsupported_action }
    struct Snapshot {
        let gateway: GatewayState
        let panel: PanelState
        let apiPort: Int?
        var healthyAccounts: Int? = nil
        var totalAccounts: Int? = nil
        var listenHost: String? = nil
        var proxyConfigured: Bool? = nil
        var apiKeyConfigured: Bool? = nil
    }
    private struct Preview {
        let id: String
        let owner: NativeBridge.DocumentOwner
        let expires: Date
        let data: Data
    }
    private struct ArchiveConfirmation {
        let id: UUID
        let owner: NativeBridge.DocumentOwner
        let expires: ContinuousClock.Instant
        let continuation: CheckedContinuation<Bool, Never>
        let alert: NSAlert?
    }
    private let settings: AppSettings
    private let log: AppLog
    private let snapshot: () -> Snapshot
    private let now: () -> Date
    private let selectSaveURL: ((String) async -> URL?)?
    private let openDirectory: (URL) -> Bool
    private let confirmClear: (() async -> Bool)?
    private var preview: Preview?
    private var previewExpiry: Task<Void, Never>?
    private var saving = false
    private var panel: NSSavePanel?
    private var archiveConfirmation: ArchiveConfirmation?
    private var archiveExpiry: Task<Void, Never>?
    private let fileName = "workbuddy-diagnostics.json"
    private let sections = ["应用与上游版本", "系统与架构", "服务状态与计数", "安全配置摘要", "最近安全事件（每源最多200条）"]

    init(settings: AppSettings, log: AppLog = .shared, snapshot: @escaping () -> Snapshot,
         now: @escaping () -> Date = Date.init, selectSaveURL: ((String) async -> URL?)? = nil,
         openDirectory: @escaping (URL) -> Bool = { NSWorkspace.shared.open($0) },
         confirmClear: (() async -> Bool)? = nil) {
        self.settings = settings; self.log = log; self.snapshot = snapshot
        self.now = now; self.selectSaveURL = selectSaveURL
        self.openDirectory = openDirectory; self.confirmClear = confirmClear
        applyTheme()
    }

    deinit { previewExpiry?.cancel(); archiveExpiry?.cancel() }

    func invalidate(owner: NativeBridge.DocumentOwner) {
        if let confirmation = archiveConfirmation, confirmation.owner === owner {
            finishArchiveConfirmation(id: confirmation.id, accepted: false)
        }
        clearPreview(owner: owner)
    }

    private func clearPreview(owner: NativeBridge.DocumentOwner) {
        guard preview?.owner === owner else { return }
        previewExpiry?.cancel(); previewExpiry = nil
        preview = nil
        panel?.cancel(nil)
    }

    func dispatch(action: String, payload: [String: Any], owner: NativeBridge.DocumentOwner) async throws -> [String: Any] {
        guard owner.isActive else { invalidate(owner: owner); throw Failure.stale_document }
        guard payload.count <= 5, JSONSerialization.isValidJSONObject(payload),
              let bytes = try? JSONSerialization.data(withJSONObject: payload), bytes.count <= 4096 else {
            throw Failure.invalid_payload
        }
        if let old = preview, old.expires <= now() || !old.owner.isActive { clearPreview(owner: old.owner) }
        switch action {
        case "logs.tail":
            guard Set(payload.keys).isSubset(of: ["source", "run_id", "cursor", "limit"]),
                  let raw = payload["source"] as? String, let source = AppLog.Source(rawValue: raw) else { throw Failure.invalid_payload }
            var run: String?
            if let value = payload["run_id"] {
                guard let string = value as? String, string.utf8.count == 36, UUID(uuidString: string) != nil else { throw Failure.invalid_payload }
                run = string
            }
            let cursor = try payload["cursor"].map { try Self.integer($0, range: 0...9_007_199_254_740_991) }
            let limit = try payload["limit"].map { try Self.integer($0, range: 1...200) } ?? 200
            do {
                let tail = try log.tail(source: source, runID: run, cursor: cursor, limit: Int(limit))
                return ["run_id": tail.runID, "cursor": tail.cursor, "reset": tail.reset,
                        "entries": tail.entries.map(Self.entry)]
            } catch { throw Failure.invalid_payload }
        case "preferences.get":
            guard payload.isEmpty else { throw Failure.invalid_payload }
            return preferences()
        case "preferences.update":
            try updatePreferences(payload)
            return preferences()
        case "diagnostics.export":
            return try await export(payload, owner: owner)
        case "desktop.openDirectory":
            guard Set(payload.keys) == Set(["kind"]), let kind = payload["kind"] as? String,
                  ["data", "logs"].contains(kind) else { throw Failure.invalid_payload }
            return ["opened": openDirectory(kind == "data" ? AppPaths.dataDirectory : AppPaths.logDirectory)]
        case "logs.clearArchives":
            guard payload.isEmpty else { throw Failure.invalid_payload }
            let decision = try await requestArchiveConfirmation(owner: owner)
            guard owner.isActive else { throw Failure.stale_document }
            guard decision.accepted, ContinuousClock.now < decision.expires else { return ["cleared": 0, "cancelled": true] }
            do { return ["cleared": try log.clearArchives()] }
            catch AppLog.ArchiveError.notWriter { throw Failure.busy }
            catch { throw Failure.cleanup_failed }
        default: throw Failure.unsupported_action
        }
    }

    private func requestArchiveConfirmation(owner: NativeBridge.DocumentOwner) async throws -> (accepted: Bool, expires: ContinuousClock.Instant) {
        guard archiveConfirmation == nil else { throw Failure.busy }
        let window = NSApp?.keyWindow ?? NSApp?.mainWindow
        guard confirmClear != nil || window != nil else { throw Failure.busy }
        let alert: NSAlert?
        if confirmClear == nil {
            let value = NSAlert()
            value.messageText = "清理安全日志归档？"
            value.informativeText = "最多删除 6 个安全日志归档文件。当前日志、内存中的事件和业务数据会保留。确认将在 25 秒后自动取消。"
            value.alertStyle = .warning
            value.addButton(withTitle: "取消")
            value.addButton(withTitle: "清理归档")
            alert = value
        } else { alert = nil }
        let id = UUID(), expires = ContinuousClock.now.advanced(by: .seconds(25))
        let accepted = await withCheckedContinuation { (continuation: CheckedContinuation<Bool, Never>) in
            archiveConfirmation = ArchiveConfirmation(id: id, owner: owner, expires: expires, continuation: continuation, alert: alert)
            archiveExpiry = Task { [weak self] in
                do { try await ContinuousClock().sleep(until: expires) } catch { return }
                self?.finishArchiveConfirmation(id: id, accepted: false)
            }
            if let confirmClear {
                Task { [weak self] in
                    let answer = await confirmClear()
                    self?.finishArchiveConfirmation(id: id, accepted: answer)
                }
            } else if let alert, let window {
                alert.beginSheetModal(for: window) { [weak self] response in
                    self?.finishArchiveConfirmation(id: id, accepted: response == .alertSecondButtonReturn)
                }
            }
        }
        return (accepted, expires)
    }

    private func finishArchiveConfirmation(id: UUID, accepted: Bool) {
        guard let value = archiveConfirmation, value.id == id else { return }
        // Revoke before closing the sheet: its callback may run later, after a new
        // confirmation starts. Only this ID can resume this continuation.
        archiveConfirmation = nil
        archiveExpiry?.cancel(); archiveExpiry = nil
        if let alert = value.alert { alert.window.sheetParent?.endSheet(alert.window, returnCode: .cancel) }
        value.continuation.resume(returning: accepted && ContinuousClock.now < value.expires)
    }

    private static func integer(_ value: Any, range: ClosedRange<Int64>) throws -> Int64 {
        guard let number = value as? NSNumber, CFGetTypeID(number) != CFBooleanGetTypeID(),
              let integer = Int64(exactly: number.doubleValue), range.contains(integer) else { throw Failure.invalid_payload }
        return integer
    }
    private static func boolean(_ value: Any) throws -> Bool {
        guard let number = value as? NSNumber, CFGetTypeID(number) == CFBooleanGetTypeID() else { throw Failure.invalid_payload }
        return number.boolValue
    }
    private func updatePreferences(_ payload: [String: Any]) throws {
        guard !payload.isEmpty, Set(payload.keys).isSubset(of: ["theme", "autoStartGatewayOnLaunch", "autoStartGatewayAtLogin", "hasCompletedFirstRun", "note", "openLoginItems"]) else { throw Failure.invalid_payload }
        if let value = payload["openLoginItems"] {
            guard payload.count == 1, try Self.boolean(value) else { throw Failure.invalid_payload }
            SMAppService.openSystemSettingsLoginItems()
            return
        }
        var theme: String?, start: Bool?, loginStart: Bool?, firstRun: Bool?, note: (String, String)?
        if let value = payload["theme"] {
            guard let value = value as? String, ["system", "light", "dark"].contains(value) else { throw Failure.invalid_payload }
            theme = value
        }
        if let value = payload["autoStartGatewayOnLaunch"] { start = try Self.boolean(value) }
        if let value = payload["autoStartGatewayAtLogin"] { loginStart = try Self.boolean(value) }
        if let value = payload["hasCompletedFirstRun"] { firstRun = try Self.boolean(value) }
        if let value = payload["note"] {
            guard let noteValue = value as? [String: Any], Set(noteValue.keys) == Set(["uid", "value"]),
                  let uid = noteValue["uid"] as? String, let text = noteValue["value"] as? String,
                  settings.canSetAccountNote(uid: uid, value: text) else { throw Failure.invalid_payload }
            note = (uid, text)
        }
        // Validate the whole patch before changing macOS registration or persisted preferences.
        if let loginStart {
            let service = SMAppService.mainApp
            do {
                if loginStart && service.status != .enabled && service.status != .requiresApproval { try service.register() }
                if !loginStart && (service.status == .enabled || service.status == .requiresApproval) { try service.unregister() }
            } catch {
                // A denied launch may still leave a registered item awaiting approval.
                guard loginStart && service.status == .requiresApproval else { throw Failure.save_failed }
            }
            let status = service.status
            guard loginStart ? (status == .enabled || status == .requiresApproval)
                             : (status == .notRegistered || status == .notFound) else { throw Failure.save_failed }
        }
        if let note { _ = settings.setAccountNote(uid: note.0, value: note.1) }
        if let theme { settings.theme = theme; applyTheme() }
        if let start { settings.autoStartGateway = start }
        if let loginStart { settings.autoStartGatewayAtLogin = loginStart }
        if let firstRun { settings.hasCompletedFirstRun = firstRun }
    }
    private func applyTheme() {
        switch settings.theme {
        case "light": NSApp?.appearance = NSAppearance(named: .aqua)
        case "dark": NSApp?.appearance = NSAppearance(named: .darkAqua)
        default: NSApp?.appearance = nil
        }
    }
    private func preferences() -> [String: Any] {
        ["theme": settings.theme, "autoStartGatewayOnLaunch": settings.autoStartGateway,
         "autoStartGatewayAtLogin": settings.autoStartGatewayAtLogin, "loginItemStatus": Self.loginItemStatus(),
         "hasCompletedFirstRun": settings.hasCompletedFirstRun, "notes": settings.accountNotes,
         "about": Self.about()]
    }
    private static func loginItemStatus() -> String {
        switch SMAppService.mainApp.status {
        case .enabled: return "enabled"
        case .requiresApproval: return "requiresApproval"
        case .notRegistered: return "notRegistered"
        case .notFound: return "failed"
        @unknown default: return "failed"
        }
    }
    private static func about() -> [String: Any] {
        func version(_ key: String) -> String {
            guard let value = Bundle.main.object(forInfoDictionaryKey: key) as? String, !value.isEmpty,
                  value.utf8.count <= 64, value.utf8.allSatisfy({
                    (48...57).contains($0) || (65...90).contains($0) || (97...122).contains($0) || [43,45,46,95].contains($0)
                  }) else { return "unversioned" }
            return value
        }
        #if arch(arm64)
        let architecture = "arm64"
        #elseif arch(x86_64)
        let architecture = "x86_64"
        #else
        let architecture = "other"
        #endif
        let os = ProcessInfo.processInfo.operatingSystemVersion
        let rawTime = Bundle.main.object(forInfoDictionaryKey: "WorkBuddyBuildTime") as? String ?? ""
        let buildTime = rawTime.utf8.count <= 32 && ISO8601DateFormatter().date(from: rawTime) != nil ? rawTime : "unrecorded"
        return ["app_version": version("CFBundleShortVersionString"), "app_build": version("CFBundleVersion"),
                "gateway_commit": "d1023f373065468d67791faf7e2235768af1e056",
                "panel_commit": "9413e70bc694a1e76c416d00cf70f5c9869bc3ca",
                "license": "MIT", "architecture": architecture, "build_time": buildTime,
                "os_version": "\(os.majorVersion).\(os.minorVersion).\(os.patchVersion)"]
    }
    private static func entry(_ item: AppLog.Entry) -> [String: Any] {
        var result: [String: Any] = ["sequence": item.sequence, "timestamp": item.timestamp,
            "source": item.source.rawValue, "level": item.level.rawValue, "event": item.event.rawValue]
        if let code = item.exitCode { result["exit_code"] = code }
        if let count = item.discardedBytes { result["discarded_bytes"] = String(count) }
        return result
    }
    private func diagnosticData() throws -> Data {
        let state = snapshot()
        let gateway: String
        switch state.gateway {
        case .stopped: gateway = "stopped"
        case .starting: gateway = "starting"
        case .runningReady: gateway = "running_ready"
        case .runningUnavailable: gateway = "running_unavailable"
        case .maintenance: gateway = "maintenance"
        case .stopping: gateway = "stopping"
        case .failed: gateway = "failed"
        }
        let panelState: String
        switch state.panel {
        case .notStarted: panelState = "not_started"
        case .starting: panelState = "starting"
        case .ready: panelState = "ready"
        case .sessionExpired: panelState = "session_expired"
        case .failed: panelState = "failed"
        }
        var status: [String: Any] = ["gateway": gateway, "panel": panelState]
        if let port = state.apiPort, (1024...65535).contains(port) { status["api_port"] = port }
        if let port = state.panel.port, (1024...65535).contains(port) { status["panel_port"] = port }
        if let count = state.totalAccounts, (0...1_000_000).contains(count) { status["total_accounts"] = count }
        if let count = state.healthyAccounts, (0...1_000_000).contains(count) { status["healthy_accounts"] = count }
        var config: [String: Any] = ["theme": settings.theme, "auto_start_gateway": settings.autoStartGateway]
        if let host = state.listenHost { config["listen_host"] = host }
        if let value = state.proxyConfigured { config["proxy_configured"] = value }
        if let value = state.apiKeyConfigured { config["api_key_configured"] = value }
        var logs: [String: Any] = [:]
        for source in AppLog.Source.allCases {
            // At most 2,000 retained entries, with a fixed ten-page work bound even
            // if another thread keeps logging. Persisted files are never read.
            let first = try log.tail(source: source)
            var page = first
            var recent = page.entries
            for _ in 0..<9 {
                if page.entries.isEmpty { break }
                page = try log.tail(source: source, runID: first.runID, cursor: page.cursor)
                recent = Array((recent + page.entries).suffix(200))
            }
            logs[source.rawValue] = recent.map(Self.entry)
        }
        return try JSONSerialization.data(withJSONObject: ["schema": 1, "about": Self.about(),
            "status": status, "safe_configuration": config, "logs": logs], options: [.prettyPrinted, .sortedKeys])
    }
    private func export(_ payload: [String: Any], owner: NativeBridge.DocumentOwner) async throws -> [String: Any] {
        switch payload["operation"] as? String {
        case "preview":
            guard Set(payload.keys) == Set(["operation"]), !saving else { throw Failure.invalid_payload }
            let data = try diagnosticData()
            guard data.count <= 1024 * 1024 else { throw Failure.save_failed }
            let value = Preview(id: UUID().uuidString, owner: owner, expires: now().addingTimeInterval(120), data: data)
            previewExpiry?.cancel()
            preview = value
            previewExpiry = Task { [weak self, weak owner] in
                do { try await Task.sleep(for: .seconds(120)) } catch { return }
                guard let self, let owner, self.preview?.id == value.id else { return }
                self.clearPreview(owner: owner)
            }
            return ["preview_id": value.id, "files": [fileName], "sections": sections, "bytes": data.count]
        case "cancel", "save":
            guard Set(payload.keys) == Set(["operation", "preview_id"]), let id = payload["preview_id"] as? String,
                  id.utf8.count == 36, let value = preview, value.id == id, value.owner === owner else { throw Failure.stale_preview }
            if payload["operation"] as? String == "cancel" { clearPreview(owner: owner); return ["cancelled": true] }
            guard !saving else { throw Failure.busy }
            saving = true
            defer { saving = false; if preview?.id == id { clearPreview(owner: owner) }; panel = nil }
            let url: URL?
            if let selectSaveURL { url = await selectSaveURL(fileName) }
            else {
                let save = NSSavePanel()
                save.nameFieldStringValue = fileName
                save.allowedContentTypes = [.json]
                save.canCreateDirectories = false
                save.message = "将保存 1 个 JSON 文件，包含：" + sections.joined(separator: "、")
                panel = save
                url = await withCheckedContinuation { continuation in
                    save.begin { response in continuation.resume(returning: response == .OK ? save.url : nil) }
                }
            }
            guard owner.isActive else { throw Failure.stale_document }
            guard let current = preview, current.id == id, current.owner === owner,
                  current.expires > now() else { throw Failure.cancelled }
            guard let url else { return ["cancelled": true] }
            guard url.isFileURL else { throw Failure.save_failed }
            do { try value.data.write(to: url, options: .atomic) }
            catch { throw Failure.save_failed }
            return ["saved": true]
        default: throw Failure.invalid_payload
        }
    }
}
