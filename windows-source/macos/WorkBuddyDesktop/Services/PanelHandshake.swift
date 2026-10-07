import Foundation

/// 面板启动握手消息（开发计划 §7.1 第 5 步）。
///
/// 面板在监听成功后向 **stdout** 输出一行：
/// ```
/// @@WBGUI-READY@@ {"service":"wbgui","protocol":1,"version":"...","port":54321,"instance_id":"...","pid":1234}
/// ```
/// 该消息**不含任何密钥**；普通日志一律走 stderr。
struct PanelReady: Decodable, Equatable {
    let service: String
    let protocolVersion: Int
    let version: String
    let port: Int
    let instanceID: String
    let pid: Int32

    enum CodingKeys: String, CodingKey {
        case service
        case protocolVersion = "protocol"
        case version
        case port
        case instanceID = "instance_id"
        case pid
    }
}

/// 握手行的解析器。
///
/// 单独成类型而不是塞进进程封装里，是因为它是**契约**：协议版本不匹配时必须明确失败，
/// 而不是「碰巧还能用就继续」。面板与应用是分别构建的，版本漂移迟早会发生。
enum PanelHandshake {

    /// 握手前缀，必须与 `panel/cmd/server/desktop.go` 的 `readyPrefix` 完全一致。
    static let prefix = "@@WBGUI-READY@@ "

    /// 本应用支持的协议版本。
    static let supportedProtocol = 1

    /// 从一行文本解析就绪消息。
    /// - Returns: 解析成功返回 `PanelReady`；不是握手行返回 nil。
    /// - Throws: 是握手行但内容不合法/版本不匹配时抛错。
    static func parse(line: String) throws -> PanelReady? {
        guard line.hasPrefix(prefix) else { return nil }
        let payload = String(line.dropFirst(prefix.count))

        guard let data = payload.data(using: .utf8) else {
            throw AppError.panelHandshakeMalformed(detail: "握手行不是合法 UTF-8")
        }

        let ready: PanelReady
        do {
            ready = try JSONDecoder().decode(PanelReady.self, from: data)
        } catch {
            throw AppError.panelHandshakeMalformed(detail: "JSON 解析失败：\(error.localizedDescription)")
        }

        guard ready.service == "wbgui" else {
            throw AppError.panelHandshakeMalformed(detail: "服务名不是 wbgui，而是 \(ready.service)")
        }
        guard ready.protocolVersion == supportedProtocol else {
            throw AppError.panelHandshakeMalformed(
                detail: "协议版本不匹配：面板 \(ready.protocolVersion)，应用 \(supportedProtocol)。"
                    + "应用包与内置组件版本不一致，请重新安装完整版本。")
        }
        guard (1024...65535).contains(ready.port) else {
            throw AppError.panelHandshakeMalformed(detail: "端口 \(ready.port) 不在合法范围内")
        }
        guard !ready.instanceID.isEmpty else {
            throw AppError.panelHandshakeMalformed(detail: "缺少 instance_id")
        }

        return ready
    }

    /// 生成下发给面板的启动参数 JSON（写入其 stdin）。
    ///
    /// 安全要求（开发计划 §7.1 第 4 步）：内部口令只走这条管道，
    /// **不进命令行、不进 URL、不进日志** —— 命令行参数在 `ps` 里对同机所有用户可见。
    static func makeInitPayload(
        instanceID: String,
        username: String,
        password: String,
        controlKey: String,
        gatewayURL: String,
        authDir: String,
        configFile: String,
        backupDir: String,
        gatewayAPIKey: String,
        dangerousOps: Bool
    ) -> Data {
        var dict: [String: Any] = [
            "protocol": supportedProtocol,
            "instance_id": instanceID,
            "username": username,
            "password": password,
            "panel_control_key": controlKey,
            "gateway_url": gatewayURL,
            "auth_dir": authDir,
            "config_file": configFile,
            "backup_dir": backupDir,
            // 高危操作默认关闭，由原生层显式下发；面板侧未收到时按 false 处理
            "dangerous_ops": dangerousOps,
        ]
        if !gatewayAPIKey.isEmpty {
            dict["gateway_api_key"] = gatewayAPIKey
        }
        // 字段全部是字符串/数字/布尔，序列化不会失败；失败时给空对象让面板报错退出。
        return (try? JSONSerialization.data(withJSONObject: dict, options: [])) ?? Data("{}".utf8)
    }

    /// 生成内部凭据：每次启动重新随机，只存在于原生层与面板内存。
    ///
    /// 32 字节 hex（64 字符）—— 远高于面板侧 16 位下限，且不可猜测。
    static func randomSecret() -> String {
        var bytes = [UInt8](repeating: 0, count: 32)
        let status = SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes)
        guard status == errSecSuccess else {
            // 取不到密码学随机数时不能退化成弱随机 —— 宁可让上层失败。
            return ""
        }
        return bytes.map { String(format: "%02x", $0) }.joined()
    }

    /// 内部管理用户名：桌面模式下不再使用 admin，避免与默认口令路径混淆。
    static let desktopUsername = "desktop"

    /// 本次启动的实例 ID。
    static func makeInstanceID() -> String {
        var bytes = [UInt8](repeating: 0, count: 8)
        _ = SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes)
        return bytes.map { String(format: "%02x", $0) }.joined()
    }
}
