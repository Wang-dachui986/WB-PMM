import Foundation
import Darwin

/// 受管子进程（开发计划 §6.1、§7.1、§8.2）。
///
/// 三个关键机制：
///
/// **1. 只管理自己创建的子进程。**
/// 每条记录都保存可执行文件路径、PID 与启动时刻。终止前核对身份，
/// 不凭端口或过期 PID 文件去杀陌生进程（§8.2 明令）。
///
/// **2. 父管道 EOF 退出（T06）。**
/// 子进程的 stdin 由本进程持有的写端驱动。父进程一旦消失（正常退出或被强杀），
/// 内核会关闭写端，子进程读到 EOF 后自行优雅退出 —— 不依赖任何清理代码，
/// 因此强杀父进程也不会留下孤儿服务。
///
/// **3. 不拼接 shell。**
/// 一律 `Process.executableURL` + 参数数组。参数里不会出现用户输入，
/// 但这条纪律要一开始就立住，否则后续加入账号路径之类的输入就晚了。
/// 输出读取在 outputQueue 上同步完成（每块最多 4 KiB），没有异步数据积压。
/// 原始行只在私有有界握手解析器内存活；回调与 tail 均不暴露正文。
/// 进程控制与回调配置由 MainActor 隔离。
final class ManagedProcess: @unchecked Sendable {

    /// 子进程身份，用于安全终止。
    struct Identity: Equatable {
        let executablePath: String
        let pid: Int32
        let startedAt: Date
        /// 子进程回报的实例 ID（面板有；网关没有则为 nil）。
        let instanceID: String?
    }

    let name: String
    let executableURL: URL
    private let arguments: [String]
    private let environment: [String: String]
    private let log: AppLog
    private let source: AppLog.Source

    /// 输出解析相关状态的归属队列（串行）。
    private let outputQueue = DispatchQueue(label: "cn.workbuddy.desktop.managed-process.output")

    @MainActor private var process: Process?
    @MainActor private var stdinPipe: Pipe?
    @MainActor private var stdoutPipe: Pipe?
    @MainActor private var stderrPipe: Pipe?

    static let maxHandshakeBytes = 16 * 1024
    static let readChunkBytes = 4 * 1024
    private var stdoutBuffer = Data()
    private var droppingLine = false
    private var handshakeDelivered = false
    private var expectedInstanceID: String?
    private var stdoutDiscarded: UInt64 = 0
    private var stderrDiscarded: UInt64 = 0
    private var peakBufferedBytes = 0

    enum ReadyFailure: Error, Sendable { case invalidHandshake }
    /// Exactly one typed outcome; peer-supplied strings/errors never cross this callback.
    @MainActor var onReady: (@Sendable (Result<PanelReady, ReadyFailure>) -> Void)?
    /// 进程退出回调（在主队列上）。
    @MainActor var onExit: (@Sendable (Int32) -> Void)?

    @MainActor private(set) var identity: Identity?

    @MainActor init(name: String, executableURL: URL, arguments: [String],
         environment: [String: String], source: AppLog.Source, log: AppLog = .shared) {
        self.name = name
        self.executableURL = executableURL
        self.arguments = arguments
        self.environment = environment
        self.source = source
        self.log = log
    }

    @MainActor var isRunning: Bool {
        process?.isRunning ?? false
    }

    @MainActor var pid: Int32? {
        guard let process, process.isRunning else { return nil }
        return process.processIdentifier
    }

    /// Safe events plus current discard counts, never peer output.
    var outputTail: String {
        outputQueue.sync {
            log.recentText(source: source)
                + "outputDiscarded stdoutBytes=\(stdoutDiscarded) stderrBytes=\(stderrDiscarded)"
        }
    }

    /// Numeric resource diagnostics for isolated verification and future diagnostics.
    var outputMetrics: (bufferedBytes: Int, peakBufferedBytes: Int, discardedBytes: UInt64) {
        outputQueue.sync { (stdoutBuffer.count, peakBufferedBytes, Self.add(stdoutDiscarded, stderrDiscarded)) }
    }

    // MARK: - 启动

    @MainActor func start(instanceID: String? = nil) throws {
        guard process == nil else { return }

        let p = Process()
        p.executableURL = executableURL
        p.arguments = arguments
        p.environment = environment
        // 不继承父进程的工作目录语义：显式设成数据目录，避免相对路径落到随机位置。
        p.currentDirectoryURL = AppPaths.dataDirectory

        let inPipe = Pipe()
        let outPipe = Pipe()
        let errPipe = Pipe()
        p.standardInput = inPipe
        p.standardOutput = outPipe
        p.standardError = errPipe

        outputQueue.sync {
            expectedInstanceID = instanceID
            stdoutBuffer.removeAll(keepingCapacity: true)
            droppingLine = false
            handshakeDelivered = false
            stdoutDiscarded = 0
            stderrDiscarded = 0
            peakBufferedBytes = 0
        }
        attachReader(outPipe, stdout: true)
        attachReader(errPipe, stdout: false)

        // 把名字与退出回调快照成局部常量：terminationHandler 在后台线程回调，
        // 不再捕获 self，避免「读一个可能在别处被改写的属性」。
        let log = self.log
        let source = self.source
        let exitCallback = onExit
        p.terminationHandler = { proc in
            let code = proc.terminationStatus
            log.record(.processExited, source: source, exitCode: code)
            DispatchQueue.main.async { exitCallback?(code) }
        }

        try p.run()

        process = p
        stdinPipe = inPipe
        stdoutPipe = outPipe
        stderrPipe = errPipe
        identity = Identity(executablePath: executableURL.path,
                            pid: p.processIdentifier,
                            startedAt: Date(),
                            instanceID: instanceID)

        log.record(.processStarted, source: source)
    }

    /// 向子进程 stdin 写入一行（启动参数）。
    @MainActor func writeStdinLine(_ data: Data) throws {
        guard let stdinPipe else { return }
        var payload = data
        payload.append(0x0A)  // 换行结尾，便于子进程按行解析
        try stdinPipe.fileHandleForWriting.write(contentsOf: payload)
    }

    // MARK: - 输出读取

    /// Two pipe callbacks synchronously drain bounded chunks; the pipe supplies backpressure.
    @MainActor private func attachReader(_ pipe: Pipe, stdout: Bool) {
        let callback = onReady
        pipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            guard let self else { handle.readabilityHandler = nil; return }
            self.outputQueue.sync {
                var bytes = [UInt8](repeating: 0, count: Self.readChunkBytes)
                let count = Darwin.read(handle.fileDescriptor, &bytes, bytes.count)
                if count < 0 && errno == EINTR { return }
                guard count > 0 else {
                    handle.readabilityHandler = nil
                    if stdout {
                        self.stdoutDiscarded = Self.add(self.stdoutDiscarded, UInt64(self.stdoutBuffer.count))
                        self.stdoutBuffer.removeAll(keepingCapacity: true)
                    }
                    let discarded = stdout ? self.stdoutDiscarded : self.stderrDiscarded
                    if discarded > 0 {
                        self.log.record(.outputDiscarded, source: self.source, discardedBytes: discarded)
                    }
                    return
                }
                if stdout {
                    self.consumeStdout(bytes.prefix(count), callback: callback)
                } else {
                    self.stderrDiscarded = Self.add(self.stderrDiscarded, UInt64(count))
                }
            }
        }
    }

    private static func add(_ left: UInt64, _ right: UInt64) -> UInt64 {
        let sum = left.addingReportingOverflow(right)
        return sum.overflow ? UInt64.max : sum.partialValue
    }

    private func consumeStdout(_ bytes: ArraySlice<UInt8>,
                               callback: (@Sendable (Result<PanelReady, ReadyFailure>) -> Void)?) {
        // Gateway output has no handshake. After the panel's first outcome, all further
        // bytes are discarded directly, without JSON parsing or per-line disk writes.
        guard source == .panel, !handshakeDelivered else {
            stdoutDiscarded = Self.add(stdoutDiscarded, UInt64(bytes.count))
            return
        }
        for byte in bytes {
            if handshakeDelivered {
                stdoutDiscarded = Self.add(stdoutDiscarded, 1)
            } else if droppingLine {
                stdoutDiscarded = Self.add(stdoutDiscarded, 1)
                if byte == 0x0A { droppingLine = false }
            } else if byte == 0x0A {
                finishLine(callback: callback)
            } else if stdoutBuffer.count == Self.maxHandshakeBytes {
                stdoutDiscarded = Self.add(stdoutDiscarded, UInt64(stdoutBuffer.count + 1))
                stdoutBuffer.removeAll(keepingCapacity: true)
                droppingLine = true
            } else {
                stdoutBuffer.append(byte)
                peakBufferedBytes = max(peakBufferedBytes, stdoutBuffer.count)
            }
        }
    }

    private func finishLine(callback: (@Sendable (Result<PanelReady, ReadyFailure>) -> Void)?) {
        defer { stdoutBuffer.removeAll(keepingCapacity: true) }
        guard let line = String(data: stdoutBuffer, encoding: .utf8),
              line.hasPrefix(PanelHandshake.prefix) else {
            stdoutDiscarded = Self.add(stdoutDiscarded, UInt64(stdoutBuffer.count + 1))
            return
        }
        handshakeDelivered = true
        do {
            guard let ready = try PanelHandshake.parse(line: line),
                  let expectedInstanceID, ready.instanceID == expectedInstanceID, ready.pid > 0 else {
                throw ReadyFailure.invalidHandshake
            }
            // The raw version string is not a trusted diagnostic. Do not forward it.
            callback?(.success(PanelReady(service: "wbgui", protocolVersion: PanelHandshake.supportedProtocol,
                                         version: "undisclosed", port: ready.port,
                                         instanceID: expectedInstanceID, pid: ready.pid)))
        } catch {
            log.record(.handshakeRejected, source: source, level: .error)
            callback?(.failure(.invalidHandshake))
        }
    }

    // MARK: - 停止

    /// 优雅停止：SIGTERM → 等待 → SIGKILL。
    ///
    /// 只对**本实例创建的**进程生效。若 PID 与记录不符（理论上不会发生），
    /// 则拒绝发送信号，避免误杀。
    /// - Returns: 是否在超时内正常退出。
    @discardableResult
    @MainActor func stop(timeout: TimeInterval = 10) async -> Bool {
        guard let p = process else { return true }
        guard let expected = identity, expected.pid == p.processIdentifier,
              expected.executablePath == executableURL.path else {
            log.record(.processIdentityRejected, source: source, level: .warning)
            return false
        }
        if p.isRunning {
            try? stdinPipe?.fileHandleForWriting.close()
            p.terminate()
            await waitForExit(p, timeout: timeout)
            if p.isRunning {
                kill(p.processIdentifier, SIGKILL)
                await waitForExit(p, timeout: 2)
            }
        }
        // 未确认退出时保留句柄，不能把仍在运行的子进程遗失。
        guard !p.isRunning else { return false }
        detachHandlers()
        process = nil
        identity = nil
        return true
    }

    @discardableResult
    @MainActor func forceStop() async -> Bool {
        await stop(timeout: 0)
    }

    @MainActor private func waitForExit(_ process: Process, timeout: TimeInterval) async {
        let deadline = Date().addingTimeInterval(timeout)
        while process.isRunning && Date() < deadline {
            // 即使调用任务已取消，清理仍须等待退出；不阻塞主 actor。
            await withCheckedContinuation { continuation in
                DispatchQueue.global().asyncAfter(deadline: .now() + 0.05) {
                    continuation.resume()
                }
            }
        }
    }

    @MainActor private func detachHandlers() {
        stdoutPipe?.fileHandleForReading.readabilityHandler = nil
        stderrPipe?.fileHandleForReading.readabilityHandler = nil
        stdoutPipe = nil
        stderrPipe = nil
        stdinPipe = nil
    }
}
