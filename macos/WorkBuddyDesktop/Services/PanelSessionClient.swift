import Foundation
import AppKit
import UniformTypeIdentifiers
import CoreFoundation
import Darwin

/// 面板会话客户端（开发计划 §7.1 第 6 步）。
///
/// 复用面板既有的 `/api/login` 会话系统，不新写一套账号体系。
/// 内部口令由原生层随机生成并经 stdin 管道下发，因此这里登录用的就是当次口令。
///
/// 标 `@MainActor` 是为了让 `Session` 能带着 `HTTPCookie`（非 Sendable）返回给
/// 主 actor 上的调用方，而不必把它拆成字段再重建 —— 重建正是上面注释里那个坑。
/// 网络请求本身仍是 `async`，不会阻塞主线程。
@MainActor
struct PanelSessionClient {

    struct Session {
        let token: String
        let username: String
        /// 服务端下发的**原始** Cookie 对象。
        ///
        /// 直接复用它，而不是在原生层自行重建一个：
        /// 重建时任何一处属性（Secure / Path / Domain）写错，都会让 Cookie 在
        /// `http://` 请求里**不被发送** —— 症状是面板永远停在登录页，
        /// 而「Cookie 已写入存储」的日志看起来一切正常，极难定位。
        ///
        /// 实测踩过：自行构造时显式传了 `.secure: "FALSE"`，但经 `WKHTTPCookieStore`
        /// 回读仍是 `secure=true`，于是明文 HTTP 下 Cookie 被静默丢弃。
        let cookie: HTTPCookie
    }

    enum LoginError: LocalizedError {
        case badResponse
        case rejected(status: Int, message: String)
        case noCookie

        var errorDescription: String? {
            switch self {
            case .badResponse:
                return "面板登录响应格式异常"
            case let .rejected(status, message):
                return "面板拒绝了登录（HTTP \(status)）：\(message)"
            case .noCookie:
                return "面板登录成功但未返回会话 Cookie"
            }
        }
    }

    let port: Int

    /// 会话 Cookie 名，与 `panel/internal/api/session.go` 的 `sessionCookieName` 一致。
    ///
    /// `nonisolated`：`WKHTTPCookieStore` 的回调在任意队列上，那里也要用这个名字做校验。
    nonisolated static let cookieName = "wbgui_session"

    private static let session: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        // 本地回环调用必须直连，不经过任何代理（开发计划 §16 T22）
        config.connectionProxyDictionary = [:]
        config.timeoutIntervalForRequest = 8
        config.timeoutIntervalForResource = 10
        config.httpCookieStorage = nil
        config.httpShouldSetCookies = false   // 自己解析 Set-Cookie，避免共享 Cookie 存储
        return URLSession(configuration: config, delegate: NoRedirect(), delegateQueue: nil)
    }()

    func login(username: String, password: String) async throws -> Session {
        guard let url = URL(string: "http://127.0.0.1:\(port)/api/login") else {
            throw LoginError.badResponse
        }

        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        // 面板的写请求同源校验只在 Origin 存在时生效；这里显式带上本机 Origin，
        // 使请求走的是「带 Origin 且同源」的严格路径，而不是依赖缺省放行。
        request.setValue("http://127.0.0.1:\(port)", forHTTPHeaderField: "Origin")
        request.httpBody = try JSONSerialization.data(withJSONObject: [
            "username": username,
            "password": password,
        ])

        let (data, response) = try await Self.session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw LoginError.badResponse }

        guard (200..<300).contains(http.statusCode) else {
            let message = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["error"] as? String
                ?? "未知原因"
            throw LoginError.rejected(status: http.statusCode, message: message)
        }

        // 从 Set-Cookie 解析会话令牌。用 HTTPCookie 解析而不是手工切字符串，
        // 以正确处理带属性（Path/HttpOnly/SameSite）与多 Cookie 的情况。
        let headerFields = http.allHeaderFields.reduce(into: [String: String]()) { acc, pair in
            if let key = pair.key as? String, let value = pair.value as? String {
                acc[key] = value
            }
        }
        let cookies = HTTPCookie.cookies(withResponseHeaderFields: headerFields, for: url)
        guard let cookie = cookies.first(where: { $0.name == Self.cookieName }) else {
            throw LoginError.noCookie
        }

        let username = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["username"] as? String
            ?? username

        return Session(token: cookie.value, username: username, cookie: cookie)
    }

    /// 固定原生控制端点；Cookie、网页参数和登录口令均不能替代 controlKey。
    func maintenance(operation: String, instanceID: String, generation: UInt64,
                     controlKey: String, action: String? = nil, uids: [String] = [],
                     expiresAt: Date? = nil) async throws -> Bool {
        guard let url = URL(string: "http://127.0.0.1:\(port)/api/desktop/maintenance"),
              !controlKey.isEmpty else { throw LoginError.badResponse }
        var body: [String: Any] = ["operation": operation, "instance_id": instanceID,
                                   "generation": generation]
        if let action, let expiresAt {
            let formatter = ISO8601DateFormatter()
            formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            body["action"] = action
            body["uids"] = uids
            body["expires_at"] = formatter.string(from: expiresAt)
        }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("http://127.0.0.1:\(port)", forHTTPHeaderField: "Origin")
        request.setValue(controlKey, forHTTPHeaderField: "X-WorkBuddy-Control-Key")
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        struct Reply: Decodable {
            let ok: Bool
            let drained: Bool?
            let instance_id: String
            let generation: UInt64
        }
        let (data, response) = try await Self.session.data(for: request)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200,
              let reply = try? JSONDecoder().decode(Reply.self, from: data), reply.ok,
              reply.instance_id == instanceID, reply.generation == generation else {
            throw LoginError.badResponse
        }
        return operation == "begin" || reply.drained == true
    }
    private func accountRequest(path: String, method: String = "GET", cookie: HTTPCookie? = nil,
                                instance: String? = nil, generation: UInt64? = nil,
                                controlKey: String? = nil, body: [String: Any]? = nil) async throws -> [String: Any] {
        guard let url = URL(string: "http://127.0.0.1:\(port)" + path) else { throw NativeBridge.MaintenanceError.operation_failed }
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.setValue("http://127.0.0.1:\(port)", forHTTPHeaderField: "Origin")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if let cookie { request.setValue("\(Self.cookieName)=\(cookie.value)", forHTTPHeaderField: "Cookie") }
        if let instance { request.setValue(instance, forHTTPHeaderField: "X-WorkBuddy-Panel-Instance") }
        if let generation { request.setValue(String(generation), forHTTPHeaderField: "X-WorkBuddy-Maintenance-Generation") }
        if let controlKey { request.setValue(controlKey, forHTTPHeaderField: "X-WorkBuddy-Control-Key") }
        if let body { request.httpBody = try JSONSerialization.data(withJSONObject: body) }
        let (stream, response) = try await Self.session.bytes(for: request)
        guard let http = response as? HTTPURLResponse else { throw NativeBridge.MaintenanceError.operation_failed }
        var data = Data()
        for try await byte in stream {
            guard data.count < 2 << 20 else { throw NativeBridge.MaintenanceError.operation_failed }
            data.append(byte)
        }
        guard let value = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { throw NativeBridge.MaintenanceError.operation_failed }
        guard http.statusCode == 200 else {
            switch value["code"] as? String {
            case "CREDENTIAL_REGION_CONFLICT": throw NativeBridge.MaintenanceError.region_conflict
            case "OVERWRITE_CONFIRMATION_REQUIRED": throw NativeBridge.MaintenanceError.overwrite_required
            case "MAINTENANCE_REQUIRED": throw NativeBridge.MaintenanceError.stale_maintenance
            default: throw NativeBridge.MaintenanceError.operation_failed
            }
        }
        return value
    }

    func authorizationURL(sessionID: String, instance: String, generation: UInt64, controlKey: String) async throws -> URL {
        let value = try await accountRequest(path: "/api/desktop/login/" + sessionID,
                                            instance: instance, generation: generation, controlKey: controlKey)
        let dateParser = ISO8601DateFormatter()
        dateParser.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        guard value["session_id"] as? String == sessionID, let text = value["expires_at"] as? String,
              let expires = dateParser.date(from: text) ?? ISO8601DateFormatter().date(from: text), expires > Date(),
              let raw = value["auth_url"] as? String, let url = URL(string: raw),
              url.scheme == "https", let host = url.host,
              ["copilot.tencent.com", "www.workbuddy.ai"].contains(host),
              url.user == nil, url.password == nil, url.port == nil || url.port == 443 else {
            throw NativeBridge.MaintenanceError.invalid_authorization_url
        }
        return url
    }

    func existingRegion(uid: String, cookie: HTTPCookie) async throws -> String? {
        let value = try await accountRequest(path: "/api/accounts", cookie: cookie)
        guard let accounts = value["accounts"] as? [[String: Any]] else { throw NativeBridge.MaintenanceError.operation_failed }
        guard let account = accounts.first(where: { $0["uid"] as? String == uid && $0["has_file"] as? Bool == true }) else { return nil }
        return try NativeAuthImport.region(account["domain"] as? String ?? "")
    }

    func importCredential(_ raw: Data, overwrite: Bool, cookie: HTTPCookie, instance: String, generation: UInt64) async throws {
        guard let text = String(data: raw, encoding: .utf8) else { throw NativeBridge.MaintenanceError.invalid_file }
        let value = try await accountRequest(path: "/api/accounts/import", method: "POST", cookie: cookie,
                                            instance: instance, generation: generation, body: ["raw_json": text, "overwrite": overwrite])
        guard value["ok"] as? Bool == true else { throw NativeBridge.MaintenanceError.operation_failed }
    }

    func deleteAccount(uid: String, cookie: HTTPCookie, instance: String, generation: UInt64) async throws {
        let value = try await accountRequest(path: "/api/accounts/\(uid)?confirm=\(uid)", method: "DELETE", cookie: cookie,
                                            instance: instance, generation: generation)
        guard value["ok"] as? Bool == true else { throw NativeBridge.MaintenanceError.operation_failed }
    }

    struct ConfigFailure: Error {
        let code: String
        // A transport failure is not proof that the server did not commit.
        let uncertain: Bool
    }
    struct PreparedConfig {
        let backupID: String
        let revision: String
        let candidateRevision: String
    }
    struct ConfigView {
        let revision: String
        let port: Int
        let keyConfigured: Bool
        let proxyConfigured: Bool
        let json: [String: Any]
        private static func checkinHours(_ value: Any) -> [Int]? {
            guard let values = value as? [Any], (1...24).contains(values.count) else { return nil }
            var hours: [Int] = [], seen = Set<Int>()
            for value in values {
                guard let number = value as? NSNumber, CFGetTypeID(number) != CFBooleanGetTypeID(),
                      !["f", "d"].contains(String(cString: number.objCType)),
                      let hour = Int(number.stringValue), (0...23).contains(hour), seen.insert(hour).inserted else { return nil }
                hours.append(hour)
            }
            return hours
        }
        init(_ value: [String: Any]) throws {
            guard let revision = value["revision"] as? String, NativeBridge.validRevision(revision),
                  let editable = value["editable"] as? [String: Any],
                  let upstream = editable["upstream"] as? [String: Any],
                  let pool = editable["pool"] as? [String: Any],
                  let sticky = editable["session_sticky"] as? [String: Any],
                  let desktop = editable["desktop"] as? [String: Any],
                  let summary = value["safe_summary"] as? [String: Any],
                  let port = editable["port"] as? Int, (1024...65535).contains(port),
                  let timeout = upstream["timeout_seconds"] as? Int,
                  let header = upstream["header_timeout_seconds"] as? Int,
                  let idle = upstream["idle_timeout_seconds"] as? Int,
                  let max = pool["max_in_flight"] as? Int,
                  let enabled = sticky["enabled"] as? Bool, let ttl = sticky["ttl"] as? String,
                  let proxy = desktop["proxy_url"] as? String,
                  summary["listen_host"] as? String == "127.0.0.1",
                  let schedulesDisabledNumber = summary["schedules_disabled"] as? NSNumber,
                  CFGetTypeID(schedulesDisabledNumber) == CFBooleanGetTypeID(),
                  let configured = value["key_configured"] as? Bool else {
                throw ConfigFailure(code: "CONFIG_RESPONSE", uncertain: true)
            }
            let schedulesDisabled = schedulesDisabledNumber.boolValue
            var checkinEnabled = false, hours = [9, 21]
            if let rawSchedule = editable["schedule"] {
                guard let schedule = rawSchedule as? [String: Any],
                      Set(schedule.keys) == ["checkin_enabled", "checkin_hours"],
                      let enabledNumber = schedule["checkin_enabled"] as? NSNumber,
                      CFGetTypeID(enabledNumber) == CFBooleanGetTypeID(),
                      let rawHours = schedule["checkin_hours"],
                      let checkinHours = Self.checkinHours(rawHours) else {
                    throw ConfigFailure(code: "CONFIG_RESPONSE", uncertain: true)
                }
                checkinEnabled = enabledNumber.boolValue; hours = checkinHours
            }
            guard schedulesDisabled == !checkinEnabled else { throw ConfigFailure(code: "CONFIG_RESPONSE", uncertain: true) }
            self.revision = revision; self.port = port; keyConfigured = configured; proxyConfigured = !proxy.isEmpty
            // Rebuild the public projection; never forward arbitrary server fields to JS.
            json = ["revision": revision, "key_configured": configured,
                    "safe_summary": ["listen_host": "127.0.0.1", "schedules_disabled": schedulesDisabled],
                    "editable": ["port": port, "upstream": ["timeout_seconds": timeout, "header_timeout_seconds": header, "idle_timeout_seconds": idle],
                                 "pool": ["max_in_flight": max], "session_sticky": ["enabled": enabled, "ttl": ttl],
                                 "schedule": ["checkin_enabled": checkinEnabled, "checkin_hours": hours], "desktop": ["proxy_url": proxy]]]
        }
    }

    func configView(cookie: HTTPCookie) async throws -> ConfigView {
        try ConfigView(await configRequest(body: nil, controlKey: nil, cookie: cookie))
    }
    func prepareConfig(instance: String, controlKey: String, revision: String, patch: [String: Any]?) async throws -> PreparedConfig {
        var body: [String: Any] = ["operation": "prepare", "instance_id": instance, "revision": revision]
        if let patch { body["patch"] = patch } else { body["rotate_key"] = true }
        let result = try await configRequest(body: body, controlKey: controlKey)
        guard let backup = result["backup_id"] as? String, !backup.isEmpty, backup.utf8.count <= 128,
              result["revision"] as? String == revision,
              let candidate = result["candidate_revision"] as? String, NativeBridge.validRevision(candidate) else {
            throw ConfigFailure(code: "CONFIG_RESPONSE", uncertain: true)
        }
        return PreparedConfig(backupID: backup, revision: revision, candidateRevision: candidate)
    }
    func commitConfig(instance: String, controlKey: String, backupID: String) async throws -> ConfigView {
        let result = try await configRequest(body: ["operation": "commit", "instance_id": instance, "backup_id": backupID], controlKey: controlKey)
        guard let view = result["config"] as? [String: Any] else { throw ConfigFailure(code: "CONFIG_RESPONSE", uncertain: true) }
        return try ConfigView(view)
    }
    func restoreConfig(instance: String, controlKey: String, backupID: String, currentRevision: String) async throws -> ConfigView {
        let result = try await configRequest(body: ["operation": "restore", "instance_id": instance, "backup_id": backupID, "current_revision": currentRevision], controlKey: controlKey)
        guard let view = result["config"] as? [String: Any] else { throw ConfigFailure(code: "CONFIG_RESPONSE", uncertain: true) }
        return try ConfigView(view)
    }
    func finishConfig(instance: String, controlKey: String, backupID: String) async throws {
        _ = try await configRequest(body: ["operation": "finish", "instance_id": instance, "backup_id": backupID], controlKey: controlKey)
    }
    private func configRequest(body: [String: Any]?, controlKey: String?, cookie: HTTPCookie? = nil) async throws -> [String: Any] {
        let path = body == nil ? "/api/config" : "/api/desktop/config"
        var request = URLRequest(url: URL(string: "http://127.0.0.1:\(port)" + path)!)
        request.httpMethod = body == nil ? "GET" : "POST"
        request.setValue("http://127.0.0.1:\(port)", forHTTPHeaderField: "Origin")
        if let cookie { request.setValue("\(Self.cookieName)=\(cookie.value)", forHTTPHeaderField: "Cookie") }
        if let controlKey { request.setValue(controlKey, forHTTPHeaderField: "X-WorkBuddy-Control-Key") }
        if let body {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try JSONSerialization.data(withJSONObject: body)
        }
        do {
            let (stream, response) = try await Self.session.bytes(for: request)
            var data = Data()
            for try await byte in stream {
                guard data.count < 65536 else { throw ConfigFailure(code: "CONFIG_RESPONSE", uncertain: true) }
                data.append(byte)
            }
            guard let http = response as? HTTPURLResponse,
                  let result = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
                throw ConfigFailure(code: "CONFIG_RESPONSE", uncertain: true)
            }
            guard http.statusCode == 200 else {
                let allowed: Set<String> = ["CONFIG_INVALID", "CONFIG_CONFLICT", "CONFIG_BUSY", "CONFIG_TRANSACTION", "CONFIG_BACKUP_BUDGET", "CONFIG_CANCELLED", "CONFIG_STORAGE"]
                let code = result["code"] as? String ?? ""
                throw ConfigFailure(code: allowed.contains(code) ? code : "CONFIG_RESPONSE", uncertain: !allowed.contains(code))
            }
            if body != nil, result["ok"] as? Bool != true { throw ConfigFailure(code: "CONFIG_RESPONSE", uncertain: true) }
            return result
        } catch let failure as ConfigFailure { throw failure }
        catch { throw ConfigFailure(code: "CONFIG_TRANSPORT", uncertain: true) }
    }

}

/// 即使回环端口被替换，也不把内部登录口令随 307/308 转发到另一端点。
final class NoRedirect: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

/// Only user-selected bounded credential bytes are retained in native memory.
/// Kept with the fixed client so existing Xcode/source lists need no new module.
@MainActor final class NativeAuthImport {
    struct Credential { let raw: Data; let uid: String; let region: String; let expiresAt: Int64 }
    var picker: NSOpenPanel?
    var chooseFile: (() async -> URL?)? // Isolated test injection; never supplied by the bridge.

    func choose() async -> URL? {
        if let chooseFile { return await chooseFile() }
        let panel = NSOpenPanel()
        panel.allowedContentTypes = [.json]
        panel.allowsMultipleSelection = false
        panel.canChooseDirectories = false
        panel.canChooseFiles = true
        picker = panel
        defer { if picker === panel { picker = nil } }
        return await withCheckedContinuation { continuation in
            panel.begin { result in continuation.resume(returning: result == .OK ? panel.url : nil) }
        }
    }
    func cancel() { picker?.cancel(nil) }

    static func region(_ domain: String) throws -> String {
        switch domain {
        case "", "copilot.tencent.com": return "cn"
        case "workbuddy.ai", "www.workbuddy.ai": return "global"
        default: throw NativeBridge.MaintenanceError.region_conflict
        }
    }
    static func read(_ url: URL) throws -> Credential {
        guard url.isFileURL else { throw NativeBridge.MaintenanceError.invalid_file }
        let fd = Darwin.open(url.path, O_RDONLY | O_NOFOLLOW | O_NONBLOCK)
        guard fd >= 0 else { throw NativeBridge.MaintenanceError.invalid_file }
        let handle = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
        defer { try? handle.close() }
        var st = stat()
        guard fstat(fd, &st) == 0, st.st_mode & S_IFMT == S_IFREG, st.st_size > 0, st.st_size <= 1 << 20,
              let data = try handle.read(upToCount: (1 << 20) + 1), data.count <= 1 << 20,
              String(data: data, encoding: .utf8) != nil,
              let root = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw NativeBridge.MaintenanceError.invalid_file
        }
        let auth: [String: Any], account: [String: Any]
        if root.keys.contains("auth") {
            guard let a = root["auth"] as? [String: Any], let c = root["account"] as? [String: Any] else { throw NativeBridge.MaintenanceError.invalid_file }
            auth = a; account = c
        } else { auth = root; account = root }
        guard let uid = account["uid"] as? String, NativeBridge.validUID(uid),
              let token = auth["accessToken"] as? String, !token.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            throw NativeBridge.MaintenanceError.invalid_file
        }
        if let refresh = auth["refreshToken"], !(refresh is String) { throw NativeBridge.MaintenanceError.invalid_file }
        let domain = auth["domain"] as? String ?? ""
        if let value = auth["domain"], !(value is String) { throw NativeBridge.MaintenanceError.invalid_file }
        var expiry: Int64 = 0
        if let value = auth["expiresAt"] {
            guard let number = value as? NSNumber, CFGetTypeID(number) != CFBooleanGetTypeID(), let parsed = Int64(number.stringValue) else { throw NativeBridge.MaintenanceError.invalid_file }
            expiry = parsed
        }
        return Credential(raw: data, uid: uid, region: try region(domain), expiresAt: expiry)
    }
}
