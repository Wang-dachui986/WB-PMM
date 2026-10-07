import Foundation

/// 应用的数据与日志目录布局。
///
/// 设计原则（开发计划 §6.1、§10.1）：安装包与数据分离。
/// 所有可变数据都在用户目录下，`.app` 本身只读；删除 `.app` 不影响账号与配置，
/// 因此升级只需替换应用包。
///
/// 布局：
/// ```
/// ~/Library/Application Support/WorkBuddy2APIDesktop/
///   auths/                  网关账号凭证（workbuddy-*.json）
///   gateway-config.json     网关配置（listen 默认回环，可经配置事务开启 LAN）
///   backups/                配置备份
///   run/                    运行期文件（锁、子进程日志）
/// ~/Library/Logs/WorkBuddy2APIDesktop/
///   panel.log / gateway.log / desktop.log
/// ```
enum AppPaths {

    /// 数据根目录。
    static let dataDirectory: URL = {
        #if DEBUG
        if let root = ProcessInfo.processInfo.environment["WB_TEST_ROOT"], root.hasPrefix("/") {
            return URL(fileURLWithPath: root).appendingPathComponent("data", isDirectory: true)
        }
        #endif
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return base.appendingPathComponent("WorkBuddy2APIDesktop", isDirectory: true)
    }()

    /// 日志根目录。
    static let logDirectory: URL = {
        #if DEBUG
        if let root = ProcessInfo.processInfo.environment["WB_TEST_ROOT"], root.hasPrefix("/") {
            return URL(fileURLWithPath: root).appendingPathComponent("logs", isDirectory: true)
        }
        #endif
        let base = FileManager.default.urls(for: .libraryDirectory, in: .userDomainMask)[0]
        return base.appendingPathComponent("Logs/WorkBuddy2APIDesktop", isDirectory: true)
    }()

    static var authDirectory: URL { dataDirectory.appendingPathComponent("auths", isDirectory: true) }
    static var gatewayConfigFile: URL { dataDirectory.appendingPathComponent("gateway-config.json") }
    static var panelConfigFile: URL { dataDirectory.appendingPathComponent("panel-config.json") }
    static var backupDirectory: URL { dataDirectory.appendingPathComponent("backups", isDirectory: true) }
    static var runDirectory: URL { dataDirectory.appendingPathComponent("run", isDirectory: true) }

    static var instanceLockFile: URL { runDirectory.appendingPathComponent("instance.lock") }
    static var panelLogFile: URL { logDirectory.appendingPathComponent("panel.log") }
    static var gatewayLogFile: URL { logDirectory.appendingPathComponent("gateway.log") }
    static var desktopLogFile: URL { logDirectory.appendingPathComponent("desktop.log") }

    /// 创建所有必需目录，权限 0700。
    ///
    /// 目录里会出现账号 Token 与本地 API 密钥，权限必须是 0700。
    /// 注意：在 exFAT 卷上 chmod 是空操作，因此数据目录必须落在内置 APFS 卷上。
    /// 这里在创建后回读权限并校验，不满足时明确报错而不是静默继续。
    static func prepare() throws {
        let fm = FileManager.default
        for dir in [dataDirectory, logDirectory, authDirectory, backupDirectory, runDirectory] {
            try fm.createDirectory(at: dir, withIntermediateDirectories: true,
                                   attributes: [.posixPermissions: 0o700])
        }
        try verifyPermissions(of: dataDirectory)
        try verifyPermissions(of: authDirectory)
    }

    /// 校验目录权限确实是 0700（而不是「设置过但被文件系统忽略」）。
    private static func verifyPermissions(of url: URL) throws {
        let attrs = try FileManager.default.attributesOfItem(atPath: url.path)
        guard let mode = attrs[.posixPermissions] as? NSNumber else {
            throw AppError.dataDirectoryNotSecurable(
                path: url.path, detail: "无法读取目录权限")
        }
        let bits = mode.intValue & 0o777
        guard bits == 0o700 else {
            throw AppError.dataDirectoryNotSecurable(
                path: url.path,
                detail: String(format: "实际权限为 %03o，期望 700", bits))
        }
    }

    /// 本应用在 Bundle 内的 helper 可执行文件路径（开发构建时回退到 OUT 目录）。
    ///
    /// 生产布局：`WorkBuddyDesktop.app/Contents/Helpers/wbgui`
    /// 开发布局：环境变量 `WB_HELPERS_DIR` 指向构建输出目录
    static func helperExecutable(named name: String) throws -> URL {
        if let override = ProcessInfo.processInfo.environment["WB_HELPERS_DIR"], !override.isEmpty {
            let url = URL(fileURLWithPath: override).appendingPathComponent(name)
            if FileManager.default.isExecutableFile(atPath: url.path) { return url }
            throw AppError.helperMissing(path: url.path)
        }
        guard let helpers = Bundle.main.builtInPlugInsURL?
            .deletingLastPathComponent()
            .appendingPathComponent("Helpers", isDirectory: true) else {
            throw AppError.helperMissing(path: "Contents/Helpers（Bundle 路径解析失败）")
        }
        let url = helpers.appendingPathComponent(name)
        guard FileManager.default.isExecutableFile(atPath: url.path) else {
            throw AppError.helperMissing(path: url.path)
        }
        return url
    }
}

/// 应用级错误。每条都带可操作的中文说明，供原生故障页直接展示。
enum AppError: LocalizedError {
    case dataDirectoryNotSecurable(path: String, detail: String)
    case helperMissing(path: String)
    case instanceLockFailed(detail: String)
    case panelHandshakeTimeout(seconds: Int)
    case panelHandshakeMalformed(detail: String)
    case panelExited(code: Int32, tail: String)
    case gatewayPortInUse(port: Int)
    case gatewayExited(code: Int32, tail: String)

    var errorDescription: String? {
        switch self {
        case let .dataDirectoryNotSecurable(path, detail):
            return """
            数据目录权限不安全，已停止启动。
            路径：\(path)
            原因：\(detail)
            账号凭证会保存在这里，权限必须是 700。若数据目录位于 exFAT 等不支持 Unix 权限的卷上，\
            请把数据目录移到内置磁盘后重试。
            """
        case let .helperMissing(path):
            return """
            应用内置组件缺失，无法启动服务。
            期望位置：\(path)
            这通常意味着应用包不完整。请重新安装完整版本。
            """
        case let .instanceLockFailed(detail):
            return "无法取得单实例锁：\(detail)"
        case let .panelHandshakeTimeout(seconds):
            return """
            内置管理面板在 \(seconds) 秒内没有就绪，已停止本次启动。
            请在「日志与诊断」查看面板日志，或退出后重试。
            """
        case let .panelHandshakeMalformed(detail):
            return "内置管理面板回报的启动信息无法解析：\(detail)"
        case let .panelExited(code, tail):
            return """
            内置管理面板意外退出（退出码 \(code)）。
            \(tail.isEmpty ? "" : "最后输出：\n\(tail)")
            """
        case let .gatewayPortInUse(port):
            return """
            API 端口 \(port) 已被其他程序占用。
            本应用不会结束占用该端口的程序，也不会自动改用其他端口。\
            请在上方改一个端口，或自行处理占用该端口的程序。
            """
        case let .gatewayExited(code, tail):
            return """
            API 服务意外退出（退出码 \(code)）。
            \(tail.isEmpty ? "" : "最后输出：\n\(tail)")
            """
        }
    }
}
