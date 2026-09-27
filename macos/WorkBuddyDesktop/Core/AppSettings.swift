import Foundation
import Combine
import Darwin

/// 应用自身偏好（开发计划 §7.2 `preferences.*` 只允许非敏感字段）。
///
/// 存放位置：`UserDefaults`。账号 Token、API 密钥、内部口令**一律不在这里** ——
/// UserDefaults 是明文 plist，且会被备份工具读取。
final class AppSettings: ObservableObject {

    private enum Key {
        static let apiPort = "apiPort"
        static let autoStartGateway = "autoStartGatewayOnLaunch"
        static let autoStartGatewayAtLogin = "autoStartGatewayAtLogin"
        static let theme = "theme"
        static let hasCompletedFirstRun = "hasCompletedFirstRun"
        static let closeWindowNoticeShown = "closeWindowNoticeShown"
        static let accountNotes = "accountNotes"
    }

    static let defaultAPIPort = 7863

    /// DEBUG app runs must opt into a private root and an explicit non-default port.
    /// Construct this before touching UserDefaults.standard; the test store never persists.
    static func forApplication() -> AppSettings {
        #if DEBUG
        if let root = ProcessInfo.processInfo.environment["WB_TEST_ROOT"] {
            guard validTestRoot(root),
                  let text = ProcessInfo.processInfo.environment["WB_TEST_PORT"], let port = Int(text),
                  (1024...65535).contains(port), port != defaultAPIPort else {
                fatalError("DEBUG isolation requires a private temporary root and explicit test port")
            }
            let memory = MemoryPreferences()
            memory.set(port, forKey: Key.apiPort)
            memory.set(false, forKey: Key.autoStartGateway)
            return AppSettings(defaults: memory)
        }
        #endif
        return AppSettings()
    }
    #if DEBUG
    /// Inspect every component before AppPaths or ServiceManager can create anything.
    static func validTestRoot(_ root: String) -> Bool {
        guard root.hasPrefix("/private/tmp/"),
              root.split(separator: "/", omittingEmptySubsequences: false).dropFirst().allSatisfy({ !$0.isEmpty && $0 != "." && $0 != ".." }) else { return false }
        var fd = open("/", O_RDONLY | O_DIRECTORY | O_CLOEXEC)
        guard fd >= 0 else { return false }
        defer { close(fd) }
        for component in URL(fileURLWithPath: root).pathComponents.dropFirst() {
            let next = openat(fd, component, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
            guard next >= 0 else { return false }; close(fd); fd = next
        }
        var status = stat()
        return fstat(fd, &status) == 0 && status.st_uid == getuid() && status.st_mode & 0o7777 == 0o700
    }
    private final class MemoryPreferences: UserDefaults, @unchecked Sendable {
        private var values: [String: Any] = [:]
        init() { super.init(suiteName: "WorkBuddy-Isolated-" + UUID().uuidString)! }
        override func set(_ value: Any?, forKey key: String) { values[key] = value }
        override func integer(forKey key: String) -> Int { values[key] as? Int ?? 0 }
        override func bool(forKey key: String) -> Bool { values[key] as? Bool ?? false }
        override func string(forKey key: String) -> String? { values[key] as? String }
        override func dictionary(forKey key: String) -> [String: Any]? { values[key] as? [String: Any] }
    }
    #endif

    private let defaults: UserDefaults

    /// API 端口。改动后需要重启网关才生效。
    @Published var apiPort: Int {
        didSet { defaults.set(apiPort, forKey: Key.apiPort) }
    }

    /// 打开应用时是否自动启动 API 服务。
    /// 初始关闭：这只是应用启动行为，不等于开机自启动（设计规范 §7.1）。
    @Published var autoStartGateway: Bool {
        didSet { defaults.set(autoStartGateway, forKey: Key.autoStartGateway) }
    }

    /// macOS 登录项启动本应用时是否启动 API；初始关闭，独立于手动打开应用。
    @Published var autoStartGatewayAtLogin: Bool {
        didSet { defaults.set(autoStartGatewayAtLogin, forKey: Key.autoStartGatewayAtLogin) }
    }

    func shouldStartGateway(atLogin: Bool) -> Bool {
        atLogin ? autoStartGatewayAtLogin : autoStartGateway
    }

    /// 主题：system / light / dark。
    @Published var theme: String {
        didSet { defaults.set(theme, forKey: Key.theme) }
    }

    /// 是否已完成首次向导。
    @Published var hasCompletedFirstRun: Bool {
        didSet { defaults.set(hasCompletedFirstRun, forKey: Key.hasCompletedFirstRun) }
    }

    /// 是否已展示过「关闭窗口后仍在菜单栏运行」的说明（只说明一次）。
    @Published var closeWindowNoticeShown: Bool {
        didSet { defaults.set(closeWindowNoticeShown, forKey: Key.closeWindowNoticeShown) }
    }

    /// Local labels only. They never enter logs or diagnostics.
    @Published private(set) var accountNotes: [String: String] {
        didSet { defaults.set(accountNotes, forKey: Key.accountNotes) }
    }
    static let maxAccountNotes = 200
    static let maxNoteBytes = 256

    func canSetAccountNote(uid: String, value: String) -> Bool {
        NativeBridge.validUID(uid) && value.utf8.count <= Self.maxNoteBytes && !value.contains("\0")
            && (value.isEmpty || accountNotes[uid] != nil || accountNotes.count < Self.maxAccountNotes)
    }

    @discardableResult
    func setAccountNote(uid: String, value: String) -> Bool {
        guard canSetAccountNote(uid: uid, value: value) else { return false }
        if value.isEmpty { accountNotes.removeValue(forKey: uid) }
        else { accountNotes[uid] = value }
        return true
    }

    /// Call only after the account's actual delete succeeded.
    func removeAccountNote(uid: String) {
        guard accountNotes[uid] != nil else { return }
        accountNotes.removeValue(forKey: uid)
    }

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults

        let storedPort = defaults.integer(forKey: Key.apiPort)
        self.apiPort = (1024...65535).contains(storedPort) ? storedPort : Self.defaultAPIPort
        self.autoStartGateway = defaults.bool(forKey: Key.autoStartGateway)
        self.autoStartGatewayAtLogin = defaults.bool(forKey: Key.autoStartGatewayAtLogin)
        let savedTheme = defaults.string(forKey: Key.theme) ?? "system"
        self.theme = ["system", "light", "dark"].contains(savedTheme) ? savedTheme : "system"
        self.hasCompletedFirstRun = defaults.bool(forKey: Key.hasCompletedFirstRun)
        self.closeWindowNoticeShown = defaults.bool(forKey: Key.closeWindowNoticeShown)
        var notes: [String: String] = [:]
        if let saved = defaults.dictionary(forKey: Key.accountNotes) {
            for (uid, raw) in saved where notes.count < Self.maxAccountNotes {
                if NativeBridge.validUID(uid), let value = raw as? String,
                   !value.isEmpty, value.utf8.count <= Self.maxNoteBytes, !value.contains("\0") {
                    notes[uid] = value
                }
            }
        }
        self.accountNotes = notes
    }

    /// API Base URL（端口变化时统一由这里生成，避免各处拼字符串）。
    var apiBaseURL: String { "http://127.0.0.1:\(apiPort)/v1" }
}
