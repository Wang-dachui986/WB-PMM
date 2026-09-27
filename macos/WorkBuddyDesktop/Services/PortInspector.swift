import Foundation

/// 端口占用探测（开发计划 §6.1、§8.2）。
///
/// 明确的行为边界：
///   - 只**检测**与**展示**占用者，绝不结束它。占用端口的可能是用户自己的其他服务，
///     擅自结束属于越权操作。
///   - 不静默改用其他端口。API 端口是用户客户端配置里写死的值，
///     悄悄换端口会让「服务明明在跑但客户端连不上」变成极难排查的问题。
///
/// 探测方式：真的去 bind 一次。比读 `lsof` 可靠 —— lsof 需要权限、有解析歧义，
/// 而 bind 的成败就是内核给出的权威答案。
enum PortInspector {

    enum ProbeResult: String { case free, occupied, unavailable }

    /// Fail closed: system errors are distinct from an available port.
    static func probe(port: Int) -> ProbeResult {
        guard (1024...65535).contains(port) else { return .unavailable }
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        guard fd >= 0 else { return .unavailable }
        defer { close(fd) }

        var reuse: Int32 = 1
        guard setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &reuse, socklen_t(MemoryLayout<Int32>.size)) == 0 else { return .unavailable }

        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = in_port_t(UInt16(port).bigEndian)
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")

        let result = withUnsafePointer(to: &addr) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockaddrPointer in
                bind(fd, sockaddrPointer, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }

        if result == 0 { return .free }
        return errno == EADDRINUSE ? .occupied : .unavailable
    }

    static func isInUse(port: Int) -> Bool { probe(port: port) != .free }

    /// 占用该端口的进程信息，**仅供展示**。
    ///
    /// 用 `lsof` 读取而不做任何终止动作。拿不到信息时返回 nil ——
    /// 展示不出占用者不影响正确性，仍会明确告知用户端口被占用。
    static func occupantDescription(port: Int) -> String? {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/sbin/lsof")
        process.arguments = ["-nP", "-iTCP:\(port)", "-sTCP:LISTEN", "-F", "cn"]
        process.environment = ["PATH": "/usr/bin:/bin:/usr/sbin:/sbin"]

        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = FileHandle.nullDevice

        do {
            try process.run()
        } catch {
            return nil
        }

        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        guard process.terminationStatus == 0 else { return nil }

        // -F cn 输出形如：
        //   p1234
        //   cnginx
        var pid: String?
        var command: String?
        for line in String(decoding: data, as: UTF8.self).split(separator: "\n") {
            if line.hasPrefix("p") { pid = String(line.dropFirst()) }
            if line.hasPrefix("c") { command = String(line.dropFirst()) }
        }

        guard let command else { return nil }
        if let pid { return "\(command)（PID \(pid)）" }
        return command
    }
}
