import Foundation
import Darwin
import SystemConfiguration

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
    static func probe(port: Int, host: String = "127.0.0.1") -> ProbeResult {
        guard (1024...65535).contains(port), ["127.0.0.1", "0.0.0.0"].contains(host) else { return .unavailable }
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        guard fd >= 0 else { return .unavailable }
        defer { close(fd) }

        var reuse: Int32 = 1
        guard setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &reuse, socklen_t(MemoryLayout<Int32>.size)) == 0 else { return .unavailable }

        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = in_port_t(UInt16(port).bigEndian)
        addr.sin_addr.s_addr = inet_addr(host)

        let result = withUnsafePointer(to: &addr) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockaddrPointer in
                bind(fd, sockaddrPointer, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }

        if result == 0 { return .free }
        return errno == EADDRINUSE ? .occupied : .unavailable
    }

    static func isInUse(port: Int) -> Bool { probe(port: port) != .free }

    static func isPrivateIPv4(_ value: UInt32) -> Bool {
        let first = value >> 24, second = (value >> 16) & 0xff
        return first == 10 || (first == 172 && (16...31).contains(second)) || (first == 192 && second == 168)
    }

    /// Only active private IPv4 interfaces may be offered as LAN API addresses.
    static func lanIPv4Addresses() -> [String] {
        var head: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&head) == 0, let head else { return [] }
        defer { freeifaddrs(head) }
        var result = Set<String>()
        var next: UnsafeMutablePointer<ifaddrs>? = head
        while let entry = next {
            let item = entry.pointee
            next = item.ifa_next
            let flags = item.ifa_flags
            guard flags & UInt32(IFF_UP) != 0, flags & UInt32(IFF_LOOPBACK | IFF_POINTOPOINT) == 0,
                  let namePointer = item.ifa_name, let address = item.ifa_addr,
                  address.pointee.sa_family == sa_family_t(AF_INET) else { continue }
            let name = String(cString: namePointer)
            guard !name.hasPrefix("lo"), !name.hasPrefix("utun") else { continue }
            var ipv4 = address.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { $0.pointee.sin_addr }
            guard isPrivateIPv4(UInt32(bigEndian: ipv4.s_addr)) else { continue }
            var buffer = [CChar](repeating: 0, count: Int(INET_ADDRSTRLEN))
            guard inet_ntop(AF_INET, &ipv4, &buffer, socklen_t(buffer.count)) != nil else { continue }
            result.insert(String(decoding: buffer.prefix { $0 != 0 }.map { UInt8(bitPattern: $0) }, as: UTF8.self))
        }
        return result.sorted()
    }

    /// Bonjour 电脑名（系统设置 → 通用 → 共享 → 本地主机名），用于 `<name>.local` 地址。
    ///
    /// 路由器重新分配 IP（例如私有 Wi-Fi 地址轮换后）时，数字地址会失效，电脑名地址仍然有效；
    /// Windows 10/11、macOS、iOS 与多数 Linux 都能直接解析 `.local`。只接受合法 DNS 标签。
    static func bonjourHostName() -> String? {
        guard let name = SCDynamicStoreCopyLocalHostName(nil) as String?,
              (1...63).contains(name.utf8.count), !name.hasPrefix("-"), !name.hasSuffix("-"),
              name.unicodeScalars.allSatisfy({ ("a"..."z").contains($0) || ("A"..."Z").contains($0) || ("0"..."9").contains($0) || $0 == "-" })
        else { return nil }
        return name
    }

    /// 局域网 API 地址：先电脑名 `.local`，再各私有 IPv4 地址。没有可用网卡时为空。
    static func lanBaseURLs(port: Int) -> [String] {
        let addresses = lanIPv4Addresses()
        guard !addresses.isEmpty else { return [] }
        var urls = [String]()
        if let host = bonjourHostName() { urls.append("http://\(host).local:\(port)/v1") }
        urls += addresses.map { "http://\($0):\(port)/v1" }
        return urls
    }

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
