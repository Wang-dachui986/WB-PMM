import Foundation

/// 子进程环境变量的**显式允许列表**。
///
/// 为什么不能直接继承父进程环境：上游两个 Go 服务都提供了环境变量覆盖通道，
/// 且覆盖优先级高于配置文件。实测确认：
///   - 网关 `cmd/server/config.go:233,236` —— `WB2A_LISTEN` 可改写监听地址，
///     `WB2A_API_KEY` 可改写鉴权密钥；另有 `WB2A_AUTH_DIR`、`WB2A_STATE_FILE` 等十余项。
///   - 面板 `internal/config/config.go:194-237` —— `WBGUI_LISTEN`、`WBGUI_PASSWORD`、
///     `WBGUI_AUTH_DIR` 等。
///
/// 父进程环境里任何残留（用户 shell 里 export 过、从别的部署方式继承、CI 变量）
/// 都会静默改写监听地址与密钥。最坏情况是网关绑到全网卡且不鉴权 ——
/// 等于把账号 Token 开放到局域网。
///
/// 因此桌面模式**只传递下面列出的变量**，其余一律不传。
enum ChildEnvironment {

    /// 允许传递给子进程的环境变量白名单。
    ///
    /// 每一项都有明确用途，新增项必须说明理由：
    ///   - `PATH`      子进程可能调用 python3（上游签到/任务脚本）；给最小安全值
    ///   - `HOME`      Go 的 os.UserHomeDir()，以及任何 ~ 展开
    ///   - `TMPDIR`    临时文件；不传会让子进程回落到 /tmp
    ///   - `LANG`/`LC_ALL`  UTF-8 编码，避免中文日志乱码
    ///   - `TZ`        日志时间戳时区
    ///   - `USER`      少数库用它做展示
    ///
    /// **刻意不传递**：
    ///   - 任何 `WB2A_*` / `WBGUI_*` —— 见上，会改写监听地址与密钥
    ///   - `HTTP_PROXY` / `HTTPS_PROXY` / `ALL_PROXY` / `NO_PROXY` ——
    ///     本地服务之间的调用必须直连回环，不能绕代理（开发计划 §16 T22）
    ///   - `GODEBUG` / `GOGC` 等 Go 运行时开关
    private static let allowedKeys = [
        "PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TZ", "USER",
    ]

    /// 传给子进程的最小 PATH。
    ///
    /// 不含用户自定义目录：子进程需要的是系统基础命令，不是用户的 shell 环境。
    private static let safePATH = "/usr/bin:/bin:/usr/sbin:/sbin"

    /// 构建子进程环境。
    static func build() -> [String: String] {
        var env: [String: String] = [:]
        let parent = ProcessInfo.processInfo.environment

        for key in allowedKeys {
            guard let value = parent[key], !value.isEmpty else { continue }
            // 显式剔除：即使调用方误把 WB2A_* 加进白名单也不会漏出去
            if key.hasPrefix("WB2A_") || key.hasPrefix("WBGUI_") { continue }
            env[key] = value
        }

        env["PATH"] = safePATH
        env["LANG"] = "zh_CN.UTF-8"
        env["LC_ALL"] = "zh_CN.UTF-8"
        if env["TZ"] == nil { env["TZ"] = TimeZone.current.identifier }

        return env
    }

    /// 供自检使用：报告白名单里是否混入了危险键。
    /// 单元自检会断言这里返回空数组。
    static func auditLeaks(_ env: [String: String]) -> [String] {
        env.keys.filter { $0.hasPrefix("WB2A_") || $0.hasPrefix("WBGUI_") }
            + env.keys.filter { $0.hasSuffix("_PROXY") || $0 == "ALL_PROXY" }
    }
}
