import Foundation
import CoreFoundation

/// 所有命令先校验 frame / 实例，再校验固定 envelope 与命令专属载荷。
enum NativeBridge {
    static let messageName = "workbuddyNative"
    static let protocolVersion = 1
    static let implementedCommands = [
        "desktop.getStatus", "desktop.getBridgeInfo", "desktop.checkReadiness", "desktop.checkProxy", "gateway.start",
        "gateway.stop", "gateway.restart", "desktop.reconnect", "stats.reset",
        "account.maintenance", "auth.openBrowser", "files.importAuth", "account.setEnabled", "account.delete",
        "gateway.applyConfig", "api.rotateKey", "api.copyKey",
        "logs.tail", "preferences.get", "preferences.update", "diagnostics.export", "desktop.openDirectory", "logs.clearArchives",
    ]

    struct Request {
        let id: String
        let action: String
        let payload: [String: Any]
    }

    /// Synchronously revoked on navigation, including while a native alert runs a nested event loop.
    @MainActor final class DocumentOwner {
        let id = UUID()
        private(set) var isActive = true
        func invalidate() { isActive = false }
    }

    enum MaintenanceRequest {
        case begin(action: String, uids: [String], requestID: String? = nil)
        case cancelPending(requestID: String, importing: Bool)
        case openBrowser(sessionID: String)
        case importPreview(requestID: String)
        case importConfirm(previewID: String, overwrite: Bool, requestID: String)
        case importCancel(previewID: String)
        case setEnabled(uid: String, enabled: Bool)
        case delete(uid: String)
        case end(instanceID: String, generation: UInt64)
    }

    enum MaintenanceError: String, Error {
        case busy, cancelled, expired, shutting_down, operation_failed, stale_maintenance, not_supported
        case invalid_file, region_conflict, overwrite_required, invalid_authorization_url
    }

    struct OperationFailure: Error { let code: String }
    enum ConfigurationRequest {
        case apply(revision: String, patch: [String: Any], requestID: String)
        case rotate(revision: String, requestID: String)
        case copyKey
        case cancelPending(action: String, requestID: String)
    }
    static func validRevision(_ value: String) -> Bool {
        value.utf8.count == 64 && value.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
    }
    static func configurationRequest(_ request: Request) -> ConfigurationRequest? {
        let p = request.payload
        if request.action == "api.copyKey" { return p.isEmpty ? .copyKey : nil }
        guard ["gateway.applyConfig", "api.rotateKey"].contains(request.action) else { return nil }
        if p["operation"] as? String == "cancel_pending" {
            guard Set(p.keys) == ["operation", "request_id"], let id = p["request_id"] as? String,
                  !id.isEmpty, id.utf8.count <= 128 else { return nil }
            return .cancelPending(action: request.action, requestID: id)
        }
        guard let revision = p["revision"] as? String, validRevision(revision) else { return nil }
        if request.action == "api.rotateKey" {
            return Set(p.keys) == ["revision"] ? .rotate(revision: revision, requestID: request.id) : nil
        }
        guard Set(p.keys) == ["revision", "patch"], let patch = p["patch"] as? [String: Any],
              !patch.isEmpty, JSONSerialization.isValidJSONObject(patch),
              let raw = try? JSONSerialization.data(withJSONObject: patch), raw.count <= 16384,
              Set(patch.keys).isSubset(of: ["port", "allow_lan", "upstream", "pool", "session_sticky", "desktop", "schedule"]) else { return nil }
        func integer(_ value: Any, _ range: ClosedRange<Int>) -> Bool {
            guard let number = value as? NSNumber, CFGetTypeID(number) != CFBooleanGetTypeID(),
                  let value = Int(exactly: number.doubleValue) else { return false }
            return range.contains(value)
        }
        if let value = patch["port"], !integer(value, 1024...65535) { return nil }
        if let value = patch["allow_lan"] {
            guard let number = value as? NSNumber, CFGetTypeID(number) == CFBooleanGetTypeID() else { return nil }
        }
        if let value = patch["upstream"] {
            guard let dict = value as? [String: Any], !dict.isEmpty,
                  Set(dict.keys).isSubset(of: ["timeout_seconds", "header_timeout_seconds", "idle_timeout_seconds"]),
                  dict.values.allSatisfy({ integer($0, 1...86400) }) else { return nil }
        }
        if let value = patch["pool"] {
            guard let dict = value as? [String: Any], Set(dict.keys) == ["max_in_flight"],
                  integer(dict["max_in_flight"]!, 0...9_007_199_254_740_991) else { return nil }
        }
        if let value = patch["session_sticky"] {
            guard let dict = value as? [String: Any], !dict.isEmpty, Set(dict.keys).isSubset(of: ["enabled", "ttl"]) else { return nil }
            if let value = dict["enabled"] {
                guard let number = value as? NSNumber, CFGetTypeID(number) == CFBooleanGetTypeID() else { return nil }
            }
            if let value = dict["ttl"] { guard let text = value as? String, !text.isEmpty, text.utf8.count <= 128 else { return nil } }
        }
        if let value = patch["desktop"] {
            guard let dict = value as? [String: Any], Set(dict.keys) == ["proxy_url"],
                  let text = dict["proxy_url"] as? String, text.utf8.count <= 2048 else { return nil }
        }
        if let value = patch["schedule"] {
            guard let dict = value as? [String: Any], !dict.isEmpty,
                  Set(dict.keys).isSubset(of: ["checkin_enabled", "checkin_hours"]) else { return nil }
            if let value = dict["checkin_enabled"] {
                guard let number = value as? NSNumber, CFGetTypeID(number) == CFBooleanGetTypeID() else { return nil }
            }
            if let value = dict["checkin_hours"] {
                guard let hours = value as? [Any], (1...24).contains(hours.count) else { return nil }
                var seen = Set<Int>()
                for value in hours {
                    guard integer(value, 0...23), let number = value as? NSNumber,
                          seen.insert(Int(number.doubleValue)).inserted else { return nil }
                }
            }
        }
        return .apply(revision: revision, patch: patch, requestID: request.id)
    }

    static func validUID(_ value: String) -> Bool {
        (6...128).contains(value.utf8.count) && !value.contains("..") &&
            value.utf8.allSatisfy { (65...90).contains($0) || (97...122).contains($0) ||
                (48...57).contains($0) || $0 == 46 || $0 == 95 || $0 == 45 }
    }

    static func maintenanceRequest(_ payload: [String: Any], requestID: String? = nil) -> MaintenanceRequest? {
        switch payload["operation"] as? String {
        case "begin":
            guard Set(payload.keys) == Set(["operation", "action", "uids"]),
                  let action = payload["action"] as? String, let uids = payload["uids"] as? [String],
                  uids.allSatisfy(validUID), Set(uids).count == uids.count else { return nil }
            switch action {
            case "login": guard uids.isEmpty else { return nil }
            case "reauthorize": guard uids.count == 1 else { return nil }
            case "delete": guard (1...100).contains(uids.count) else { return nil }
            case "refresh", "credits": guard (1...20).contains(uids.count) else { return nil }
            default: return nil // import 只由后续原生文件预览确认流程进入。
            }
            return .begin(action: action == "reauthorize" ? "login" : action, uids: uids, requestID: requestID)
        case "cancel_pending":
            guard Set(payload.keys) == Set(["operation", "request_id"]),
                  let id = payload["request_id"] as? String, !id.isEmpty, id.utf8.count <= 128 else { return nil }
            return .cancelPending(requestID: id, importing: false)
        case "end", "cancel":
            guard Set(payload.keys) == Set(["operation", "instance_id", "generation"]),
                  let instance = payload["instance_id"] as? String, !instance.isEmpty,
                  instance.utf8.count <= 128,
                  let value = payload["generation"] as? String, let generation = UInt64(value),
                  generation > 0, String(generation) == value else { return nil }
            return .end(instanceID: instance, generation: generation)
        default: return nil
        }
    }

    static func accountRequest(_ request: Request) -> MaintenanceRequest? {
        let p = request.payload
        switch request.action {
        case "account.maintenance": return maintenanceRequest(p, requestID: request.id)
        case "auth.openBrowser":
            guard Set(p.keys) == ["session_id"], let id = p["session_id"] as? String,
                  id.utf8.count == 32, id.utf8.allSatisfy({ (48...57).contains($0) || (97...102).contains($0) }) else { return nil }
            return .openBrowser(sessionID: id)
        case "account.delete":
            guard Set(p.keys) == ["uid"], let uid = p["uid"] as? String, validUID(uid) else { return nil }
            return .delete(uid: uid)
        case "account.setEnabled":
            guard Set(p.keys) == ["uid", "enabled"], let uid = p["uid"] as? String, validUID(uid),
                  let number = p["enabled"] as? NSNumber, CFGetTypeID(number) == CFBooleanGetTypeID() else { return nil }
            return .setEnabled(uid: uid, enabled: number.boolValue)
        case "files.importAuth":
            switch p["operation"] as? String {
            case "preview":
                guard Set(p.keys) == ["operation"] else { return nil }
                return .importPreview(requestID: request.id)
            case "confirm":
                guard Set(p.keys) == ["operation", "preview_id", "overwrite"],
                      let id = p["preview_id"] as? String, UUID(uuidString: id) != nil,
                      let number = p["overwrite"] as? NSNumber, CFGetTypeID(number) == CFBooleanGetTypeID() else { return nil }
                return .importConfirm(previewID: id, overwrite: number.boolValue, requestID: request.id)
            case "cancel":
                guard Set(p.keys) == ["operation", "preview_id"], let id = p["preview_id"] as? String,
                      UUID(uuidString: id) != nil else { return nil }
                return .importCancel(previewID: id)
            case "cancel_pending":
                guard Set(p.keys) == ["operation", "request_id"], let id = p["request_id"] as? String,
                      !id.isEmpty, id.utf8.count <= 128 else { return nil }
                return .cancelPending(requestID: id, importing: true)
            default: return nil
            }
        default: return nil
        }
    }

    static func parse(_ body: Any) -> Request? {
        guard let dict = body as? [String: Any],
              Set(dict.keys) == Set(["id", "action", "payload"]),
              let id = dict["id"] as? String, !id.isEmpty, id.utf8.count <= 128,
              let action = dict["action"] as? String, !action.isEmpty, action.utf8.count <= 64,
              let payload = dict["payload"] as? [String: Any] else { return nil }
        return Request(id: id, action: action, payload: payload)
    }

    static func isTrustedOrigin(isMainFrame: Bool, scheme: String,
                                host: String, port: Int, panelPort: Int) -> Bool {
        isMainFrame && scheme == "http" && host == "127.0.0.1"
            && (1024...65535).contains(panelPort) && port == panelPort
    }

    @MainActor
    static func dispatch(_ request: Request,
                         statusProvider: @MainActor () -> [String: Any],
                         performAction: @MainActor (String) async -> String?,
                         performMaintenance: (@MainActor (MaintenanceRequest) async throws -> [String: Any])? = nil,
                         performConfiguration: (@MainActor (ConfigurationRequest) async throws -> [String: Any])? = nil,
                         performUtility: (@MainActor (String, [String: Any]) async throws -> [String: Any])? = nil,
                         performGateway: (@MainActor (Request) async throws -> [String: Any])? = nil,
                         performCheck: (@MainActor (Request) async throws -> [String: Any])? = nil) async -> [String: Any] {
        guard implementedCommands.contains(request.action) else {
            return failure(request.id, code: "unsupported_action", message: "命令尚未开放")
        }
        if ["desktop.checkReadiness", "desktop.checkProxy"].contains(request.action) {
            do {
                guard let performCheck else { throw OperationFailure(code: "not_supported") }
                return success(request.id, try await performCheck(request))
            } catch {
                return failure(request.id, code: (error as? OperationFailure)?.code ?? "check_failed", message: "本机检查未完成，请重试")
            }
        }
        let utilities = ["logs.tail", "preferences.get", "preferences.update", "diagnostics.export", "desktop.openDirectory", "logs.clearArchives"]
        if ["gateway.applyConfig", "api.rotateKey", "api.copyKey"].contains(request.action) || utilities.contains(request.action) ||
            (["gateway.stop", "gateway.restart"].contains(request.action) && performGateway != nil) {
            do {
                let result: [String: Any]
                if utilities.contains(request.action) {
                    guard let performUtility else { throw OperationFailure(code: "not_supported") }
                    result = try await performUtility(request.action, request.payload)
                } else if ["gateway.stop", "gateway.restart"].contains(request.action) {
                    guard let performGateway else { throw OperationFailure(code: "not_supported") }
                    result = try await performGateway(request)
                } else {
                    guard let operation = configurationRequest(request) else { throw OperationFailure(code: "invalid_payload") }
                    guard let performConfiguration else { throw OperationFailure(code: "not_supported") }
                    result = try await performConfiguration(operation)
                }
                return success(request.id, result)
            } catch {
                let code = (error as? OperationFailure)?.code ?? (error as? MaintenanceError)?.rawValue ?? "operation_failed"
                return failure(request.id, code: code, message: "操作未完成，请查看原生状态；配置草稿会保留")
            }
        }
        if ["account.maintenance", "auth.openBrowser", "files.importAuth", "account.setEnabled", "account.delete"].contains(request.action) {
            guard let operation = accountRequest(request) else {
                return failure(request.id, code: "invalid_payload", message: "账号维护参数不合法")
            }
            guard let performMaintenance else {
                return failure(request.id, code: "not_supported", message: "账号维护尚未就绪")
            }
            do { return success(request.id, try await performMaintenance(operation)) }
            catch {
                let code = (error as? MaintenanceError)?.rawValue ?? "operation_failed"
                return failure(request.id, code: code, message: "账号维护未完成，请查看原生状态或重试")
            }
        }
        guard request.payload.isEmpty else {
            return failure(request.id, code: "invalid_payload", message: "此命令不接受参数")
        }
        switch request.action {
        case "desktop.getStatus":
            // 状态响应不携带进程原始输出、路径、Cookie 或内部口令。
            let keys: Set<String> = ["gateway_state", "panel_state", "api_port", "api_base_url", "lan_enabled", "lan_api_base_urls",
                                     "instance_id", "updated_at", "busy", "panel_port", "uptime_seconds", "lifecycle_epoch",
                                     "healthy_accounts", "total_accounts", "maintenance_generation", "maintenance_action", "maintenance_phase"]
            return success(request.id, ["protocol": protocolVersion,
                                        "status": statusProvider().filter { keys.contains($0.key) }])
        case "desktop.getBridgeInfo":
            return success(request.id, ["protocol": protocolVersion, "commands": implementedCommands])
        default:
            if let code = await performAction(request.action) {
                let messages = ["busy": "服务操作正在进行，请稍后重试",
                                "shutting_down": "应用正在退出",
                                "operation_failed": "操作未完成，请在原生状态中查看原因"]
                return failure(request.id, code: code, message: messages[code] ?? "命令尚未开放")
            }
            return success(request.id, [:])
        }
    }

    static func success(_ id: String, _ data: [String: Any]) -> [String: Any] {
        ["id": id, "ok": true, "data": data]
    }

    static func failure(_ id: String, code: String, message: String) -> [String: Any] {
        ["id": id, "ok": false, "error": ["code": code, "message": message]]
    }

    static func encodeForJavaScript(_ object: [String: Any]) -> String {
        guard let data = try? JSONSerialization.data(withJSONObject: object),
              let json = String(data: data, encoding: .utf8) else {
            return #"{"ok":false,"error":{"code":"encode_failed","message":"响应序列化失败"}}"#
        }
        return json
    }
}
