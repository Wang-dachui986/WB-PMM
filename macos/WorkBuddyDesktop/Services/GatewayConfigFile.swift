import Foundation
import Security
import CryptoKit
import Darwin

/// Existing configuration is immutable here. Changes and key rotation belong to
/// the panel's revision checked transaction; startup only creates a missing file.
enum GatewayConfigFile {
    struct Snapshot {
        let listen: String
        let apiKey: String
        let authDir: String
        let stateFile: String
        let revision: String
        var proxyURL: String = ""
        var port: Int { GatewayConfigFile.parsePort(listen)! }
    }

    enum Failure: LocalizedError {
        case unsafeFile, invalidConfiguration, randomUnavailable, creationFailed
        var errorDescription: String? {
            switch self {
            case .unsafeFile: return "配置或数据目录不安全：请检查文件类型、权限和符号链接。"
            case .invalidConfiguration: return "已有网关配置无效，已保留原文件，请修正配置后重试。"
            case .randomUnavailable: return "安全随机数不可用，未创建 API 密钥。"
            case .creationFailed: return "无法安全创建初始配置，原有文件未被覆盖。"
            }
        }
    }
    private static let byteLimit = 10 * 1024 * 1024 // Matches the panel desktop config store.
    private static let schedules = ["checkin_enabled", "travel_enabled", "activity_enabled", "keepalive_enabled", "school_enabled", "cat_enabled"]

    static func currentPort(default fallback: Int) -> Int { (try? readSnapshot().port) ?? fallback }
    static func currentAPIKey() -> String? { try? readSnapshot().apiKey }

    static func readSnapshot(at url: URL = AppPaths.gatewayConfigFile) throws -> Snapshot {
        let directory = try openDirectory(url.deletingLastPathComponent())
        defer { close(directory) }
        return try readSnapshot(at: url, directory: directory)
    }

    /// Compatibility for old startup callers: NEVER rewrite a present file,
    /// including when their UserDefaults port is stale.
    @discardableResult
    static func write(port: Int, apiKey _: String?) throws -> Snapshot {
        try ensureInitial(port: port)
    }

    @discardableResult
    static func ensureInitial(port: Int, at url: URL = AppPaths.gatewayConfigFile) throws -> Snapshot {
        let root = url.deletingLastPathComponent()
        let directory = try openDirectory(root)
        defer { close(directory) }
        var status = stat()
        if fstatat(directory, url.lastPathComponent, &status, AT_SYMLINK_NOFOLLOW) == 0 {
            return try readSnapshot(at: url, directory: directory)
        }
        guard errno == ENOENT else { throw Failure.unsafeFile }
        guard (1024...65535).contains(port) else { throw Failure.invalidConfiguration }
        let key = try randomAPIKey()
        try checkPrivateTarget("auths", directory: directory, isDirectory: true)
        try checkPrivateTarget("state.json", directory: directory, isDirectory: false)
        var schedule: [String: Any] = Dictionary(uniqueKeysWithValues: schedules.map { ($0, false) })
        schedule["checkin_enabled"] = true
        schedule["checkin_hours"] = [9, 21]
        let document: [String: Any] = [
            "listen": "127.0.0.1:\(port)", "api_key": key,
            "auth_dir": root.appendingPathComponent("auths").path,
            "state_file": root.appendingPathComponent("state.json").path,
            "schedule": schedule,
            "admin": ["enabled": false],
        ]
        let data = try JSONSerialization.data(withJSONObject: document, options: [.prettyPrinted, .sortedKeys])
        let temporary = ".gateway-config-\(UUID().uuidString).tmp"
        let fd = openat(directory, temporary, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o600)
        guard fd >= 0 else { throw Failure.creationFailed }
        defer { close(fd); unlinkat(directory, temporary, 0) }
        guard fchmod(fd, 0o600) == 0 else { throw Failure.creationFailed }
        try data.withUnsafeBytes { bytes in
            var offset = 0
            while offset < bytes.count {
                let count = Darwin.write(fd, bytes.baseAddress!.advanced(by: offset), bytes.count - offset)
                if count < 0 && errno == EINTR { continue }
                guard count > 0 else { throw Failure.creationFailed }
                offset += count
            }
        }
        guard fsync(fd) == 0 else { throw Failure.creationFailed }
        // Exclusive rename prevents even a racing first launch from replacing a file.
        guard renameatx_np(directory, temporary, directory, url.lastPathComponent, UInt32(RENAME_EXCL)) == 0 else {
            if errno == EEXIST { return try readSnapshot(at: url, directory: directory) }
            throw Failure.creationFailed
        }
        guard fsync(directory) == 0 else { throw Failure.creationFailed }
        return try readSnapshot(at: url, directory: directory)
    }

    static func randomAPIKey(fill: (UnsafeMutableRawBufferPointer) -> OSStatus = {
        SecRandomCopyBytes(kSecRandomDefault, $0.count, $0.baseAddress!)
    }) throws -> String {
        var bytes = [UInt8](repeating: 0, count: 32)
        guard bytes.withUnsafeMutableBytes(fill) == errSecSuccess else { throw Failure.randomUnavailable }
        return "wbd_sk_" + bytes.map { String(format: "%02x", $0) }.joined()
    }

    static func parsePort(_ listen: String) -> Int? {
        let prefix = "127.0.0.1:"
        guard listen.hasPrefix(prefix), let port = Int(listen.dropFirst(prefix.count)),
              (1024...65535).contains(port), listen == "\(prefix)\(port)" else { return nil }
        return port
    }

    /// Open every ancestor without following links. Only the application root
    /// (not /Users or /private) must be private and owned by the current user.
    private static func openDirectory(_ url: URL) throws -> Int32 {
        guard url.isFileURL, url.path.hasPrefix("/") else { throw Failure.unsafeFile }
        var fd = open("/", O_RDONLY | O_DIRECTORY | O_CLOEXEC)
        guard fd >= 0 else { throw Failure.unsafeFile }
        do {
            for component in url.pathComponents.dropFirst() {
                guard component != ".", component != ".." else { throw Failure.unsafeFile }
                let next = openat(fd, component, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
                guard next >= 0 else { throw Failure.unsafeFile }
                close(fd); fd = next
            }
            var status = stat()
            guard fstat(fd, &status) == 0, status.st_uid == getuid(), status.st_mode & 0o7777 == 0o700 else { throw Failure.unsafeFile }
            return fd
        } catch { close(fd); throw error }
    }

    private static func checkPrivateTarget(_ name: String, directory: Int32, isDirectory: Bool) throws {
        var status = stat()
        if fstatat(directory, name, &status, AT_SYMLINK_NOFOLLOW) != 0 {
            if errno == ENOENT { return } // First run need not already have a state file.
            throw Failure.unsafeFile
        }
        guard status.st_uid == getuid(), status.st_mode & S_IFMT == (isDirectory ? S_IFDIR : S_IFREG),
              status.st_mode & 0o7777 == (isDirectory ? 0o700 : 0o600),
              isDirectory || status.st_nlink == 1 else { throw Failure.unsafeFile }
    }

    private static func readSnapshot(at url: URL, directory: Int32) throws -> Snapshot {
        let fd = openat(directory, url.lastPathComponent, O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC)
        guard fd >= 0 else { throw Failure.unsafeFile }
        defer { close(fd) }
        var status = stat()
        guard fstat(fd, &status) == 0, status.st_uid == getuid(), status.st_mode & S_IFMT == S_IFREG,
              status.st_mode & 0o7777 == 0o600, status.st_nlink == 1,
              status.st_size > 0, status.st_size <= byteLimit else { throw Failure.unsafeFile }
        let data = try FileHandle(fileDescriptor: fd, closeOnDealloc: false).read(upToCount: byteLimit + 1) ?? Data()
        guard data.count == status.st_size else { throw Failure.unsafeFile }
        guard let doc = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { throw Failure.invalidConfiguration }
        try rejectDuplicateKeys(data)
        try canonicalKeys(doc, ["listen", "api_key", "auth_dir", "state_file", "admin", "schedule", "desktop"])
        guard let listen = doc["listen"] as? String, parsePort(listen) != nil,
              let key = doc["api_key"] as? String,
              let auth = doc["auth_dir"] as? String, let state = doc["state_file"] as? String,
              auth == url.deletingLastPathComponent().appendingPathComponent("auths").path,
              state == url.deletingLastPathComponent().appendingPathComponent("state.json").path,
              let schedule = doc["schedule"] as? [String: Any] else { throw Failure.invalidConfiguration }
        try canonicalKeys(schedule, schedules + ["checkin_hours"])
        guard boolean(schedule["checkin_enabled"]) != nil else { throw Failure.invalidConfiguration }
        for name in schedules.dropFirst() { guard isFalse(schedule[name]) else { throw Failure.invalidConfiguration } }
        if let hours = schedule["checkin_hours"], !(hours is NSNull) {
            guard let hours = hours as? [Any], hours.isEmpty || validCheckinHours(hours) else { throw Failure.invalidConfiguration }
        }
        if let admin = doc["admin"] {
            guard let admin = admin as? [String: Any] else { throw Failure.invalidConfiguration }
            try canonicalKeys(admin, ["enabled"])
            if let enabled = admin["enabled"], !isFalse(enabled) { throw Failure.invalidConfiguration }
        }
        try checkPrivateTarget("auths", directory: directory, isDirectory: true)
        try checkPrivateTarget("state.json", directory: directory, isDirectory: false)
        var proxy = ""
        if let value = doc["desktop"] {
            guard let desktop = value as? [String: Any] else { throw Failure.invalidConfiguration }
            try canonicalKeys(desktop, ["proxy_url"])
            if let value = desktop["proxy_url"] {
                guard let text = value as? String, text.isEmpty || proxyEndpoint(text) != nil else { throw Failure.invalidConfiguration }
                proxy = text
            }
        }
        return Snapshot(listen: listen, apiKey: key, authDir: auth, stateFile: state,
                        revision: SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined(), proxyURL: proxy)
    }

    /// Only the committed HTTP proxy endpoint; no credentials or request target.
    static func proxyEndpoint(_ text: String) -> (host: String, port: UInt16)? {
        guard text.utf8.count <= 2048, let url = URLComponents(string: text), url.scheme == "http",
              let host = url.host, !host.isEmpty, url.user == nil, url.password == nil,
              url.path.isEmpty, url.query == nil, url.fragment == nil,
              let rawPort = url.port, let port = UInt16(exactly: rawPort), port > 0 else { return nil }
        return (host, port)
    }

    private static func isFalse(_ value: Any?) -> Bool {
        boolean(value) == false
    }
    private static func boolean(_ value: Any?) -> Bool? {
        guard let number = value as? NSNumber, CFGetTypeID(number) == CFBooleanGetTypeID() else { return nil }
        return number.boolValue
    }
    private static func validCheckinHours(_ values: [Any]) -> Bool {
        guard (1...24).contains(values.count) else { return false }
        var seen = Set<Int>()
        for value in values {
            guard let number = value as? NSNumber, CFGetTypeID(number) != CFBooleanGetTypeID(),
                  !["f", "d"].contains(String(cString: number.objCType)),
                  let hour = Int(number.stringValue), (0...23).contains(hour), seen.insert(hour).inserted else { return false }
        }
        return true
    }
    private static func canonicalKeys(_ doc: [String: Any], _ protected: [String]) throws {
        for key in doc.keys where protected.contains(key.lowercased()) && !protected.contains(key) { throw Failure.invalidConfiguration }
    }

    /// JSONSerialization validates syntax but accepts duplicate object keys. This
    /// bounded walk rejects them without decoding/re-encoding unknown large numbers.
    private static func rejectDuplicateKeys(_ data: Data) throws {
        let bytes = Array(data)
        guard String(data: data, encoding: .utf8) != nil, !bytes.contains(0) else { throw Failure.invalidConfiguration }
        var index = 0
        func whitespace() { while index < bytes.count && [9, 10, 13, 32].contains(bytes[index]) { index += 1 } }
        func string() throws -> String {
            let start = index; index += 1
            while index < bytes.count {
                let byte = bytes[index]; index += 1
                if byte == 92 { index += 1 }
                else if byte == 34 {
                    return try JSONSerialization.jsonObject(with: Data(bytes[start..<index]), options: .fragmentsAllowed) as! String
                }
            }
            throw Failure.invalidConfiguration
        }
        func value(_ depth: Int) throws {
            guard depth < 128 else { throw Failure.invalidConfiguration }
            whitespace()
            guard index < bytes.count else { throw Failure.invalidConfiguration }
            if bytes[index] == 123 {
                index += 1; whitespace(); var keys = Set<String>()
                if bytes[index] == 125 { index += 1; return }
                while true {
                    whitespace(); let key = try string()
                    guard keys.insert(key).inserted else { throw Failure.invalidConfiguration }
                    whitespace(); index += 1; try value(depth + 1); whitespace()
                    if bytes[index] == 125 { index += 1; return }; index += 1
                }
            } else if bytes[index] == 91 {
                index += 1; whitespace()
                if bytes[index] == 93 { index += 1; return }
                while true {
                    try value(depth + 1); whitespace()
                    if bytes[index] == 93 { index += 1; return }; index += 1
                }
            } else if bytes[index] == 34 { _ = try string() }
            else { while index < bytes.count && ![9, 10, 13, 32, 44, 93, 125].contains(bytes[index]) { index += 1 } }
        }
        whitespace()
        guard index < bytes.count, bytes[index] == 123 else { throw Failure.invalidConfiguration }
        try value(0)
        whitespace()
        guard index == bytes.count else { throw Failure.invalidConfiguration }
    }
}
