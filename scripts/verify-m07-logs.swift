import Foundation
import Darwin

private final class Outcomes: @unchecked Sendable {
    private let lock = NSLock()
    private var results: [Result<PanelReady, ManagedProcess.ReadyFailure>] = []
    private var exits: [Int32] = []
    func ready(_ result: Result<PanelReady, ManagedProcess.ReadyFailure>) {
        lock.lock(); defer { lock.unlock() }; results.append(result)
    }
    func exit(_ code: Int32) { lock.lock(); defer { lock.unlock() }; exits.append(code) }
    var snapshot: ([Result<PanelReady, ManagedProcess.ReadyFailure>], [Int32]) {
        lock.lock(); defer { lock.unlock() }; return (results, exits)
    }
}

@main struct LogChecks {
    static let secret = "FAKE_UNLABELLED_SECRET_73ee"
    static let body = "FAKE_COMPLETE_CHAT_BODY_42"
    static let instance = "0123456789abcdef"
    static var fm: FileManager { FileManager.default }
    static func check(_ value: Bool, _ name: String) {
        precondition(value, name)
        print("PASS \(name)")
    }
    static func folder(_ root: URL, _ name: String) throws -> URL {
        let url = root.appendingPathComponent(name)
        try fm.createDirectory(at: url, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        return url
    }
    static func bytes(_ fd: Int32, _ data: Data) {
        data.withUnsafeBytes { buffer in
            var offset = 0
            while offset < buffer.count {
                let written = Darwin.write(fd, buffer.baseAddress!.advanced(by: offset), buffer.count - offset)
                if written < 0 && errno == EINTR { continue }
                precondition(written > 0)
                offset += written
            }
        }
    }
    static func child(_ mode: String) throws {
        signal(SIGTERM, SIG_IGN)
        _ = readLine() // Private init pipe still works.
        let prefix = Data("\(secret)\nAuthorization: Bearer\n\(secret)\n{\"messages\":[\n\"\(body)\"\n]}\n".utf8)
        // Split UTF-8/body/credential boundaries across writes; neither stream is trusted.
        for chunk in [prefix.prefix(9), prefix.dropFirst(9).prefix(19), prefix.dropFirst(28)] {
            bytes(STDOUT_FILENO, Data(chunk)); bytes(STDERR_FILENO, Data(chunk))
        }
        // A single bounded flood (512 KiB), no newline. A valid ready follows it.
        let block = Data(repeating: 0x58, count: 4096)
        for _ in 0..<128 { bytes(STDOUT_FILENO, block); bytes(STDERR_FILENO, block) }
        bytes(STDOUT_FILENO, Data("\n".utf8))
        let fields: [String: Any] = ["service": mode == "bad" ? secret : "wbgui", "protocol": 1,
                                    "version": secret + body, "instance_id": instance,
                                    "port": 54321, "pid": getpid()]
        var ready = Data(PanelHandshake.prefix.utf8)
        ready.append(try JSONSerialization.data(withJSONObject: fields))
        ready.append(0x0A)
        bytes(STDOUT_FILENO, ready.prefix(11)); bytes(STDOUT_FILENO, ready.dropFirst(11))
        bytes(STDOUT_FILENO, ready) // Repeated ready must not replay the callback.
        bytes(STDERR_FILENO, Data((secret + body + "\n").utf8))
        if mode == "crash" { Darwin.exit(17) }
        while readLine() != nil {} // EOF remains a graceful shutdown signal.
    }
    static func contender() throws {
        let root = URL(fileURLWithPath: ProcessInfo.processInfo.environment["WB_TEST_ROOT"]!)
        let log = AppLog(directory: root.appendingPathComponent("dual-writer"), maxFileBytes: 256, emitSystemLog: false)
        _ = readLine()
        for _ in 0..<40 {
            for source in AppLog.Source.allCases { log.record(.existingInstance, source: source) }
        }
        for source in AppLog.Source.allCases {
            let tail = try log.tail(source: source)
            precondition(tail.entries.count == 40 && !tail.entries.contains { $0.event == .diskLoggingDisabled })
        }
        bytes(STDOUT_FILENO, Data([1]))
        _ = readLine() // Parent releases its writer lock before permitting this phase.
        for source in AppLog.Source.allCases { log.record(.shutdownCompleted, source: source) }
        bytes(STDOUT_FILENO, Data([2]))
    }
    @MainActor static func dualWriter(_ root: URL) async throws {
        let directory = try folder(root, "dual-writer")
        var owner: AppLog? = AppLog(directory: directory, maxFileBytes: 256, emitSystemLog: false)
        for source in AppLog.Source.allCases { owner!.record(.instanceStarted, source: source) }
        let child = Process()
        child.executableURL = URL(fileURLWithPath: CommandLine.arguments[0])
        child.arguments = ["writer-contender"]
        child.environment = ["WB_TEST_ROOT": root.path]
        let input = Pipe(), output = Pipe()
        child.standardInput = input; child.standardOutput = output
        try child.run()
        defer {
            try? input.fileHandleForWriting.close()
            if child.isRunning { child.terminate() }
        }
        try input.fileHandleForWriting.write(contentsOf: Data("start\n".utf8))
        let safe = directory.appendingPathComponent("safe-events-v1")
        for _ in 0..<40 {
            for source in AppLog.Source.allCases {
                owner!.record(.configurationReady, source: source)
                for name in ["\(source.rawValue).log", "\(source.rawValue).log.1", "\(source.rawValue).log.2"] {
                    let file = safe.appendingPathComponent(name)
                    if fm.fileExists(atPath: file.path) {
                        let attrs = try fm.attributesOfItem(atPath: file.path)
                        precondition((attrs[.size] as! NSNumber).intValue <= 256)
                    }
                }
            }
        }
        check(try output.fileHandleForReading.read(upToCount: 1) == Data([1]), "real contender finishes memory-only logging")
        for source in AppLog.Source.allCases {
            owner!.record(.configurationReady, source: source)
            check(try !owner!.tail(source: source).entries.contains { $0.event == .diskLoggingDisabled },
                  "\(source) competing process cannot disable disk owner")
            let logs = try files(safe).filter { $0.lastPathComponent.hasPrefix(source.rawValue + ".log") }
            check(logs.count == 3, "\(source) competing writers keep three-file limit")
            for file in logs {
                let data = try Data(contentsOf: file)
                precondition(data.count <= 256 && !String(decoding: data, as: UTF8.self).contains("existingInstance"))
            }
        }
        check(true, "competing process cannot append or rotate owner files; all checked writes <=256 bytes")
        owner = nil
        try input.fileHandleForWriting.write(contentsOf: Data("released\n".utf8))
        check(try output.fileHandleForReading.read(upToCount: 1) == Data([2]), "contender retries after owner releases lock")
        check(await eventually { !child.isRunning }, "contender exits without leftover process")
        check(child.terminationStatus == 0, "contender preserves bounded memory for all sources")
        for source in AppLog.Source.allCases {
            let data = try Data(contentsOf: safe.appendingPathComponent("\(source.rawValue).log"))
            check(data.count <= 256 && String(decoding: data, as: UTF8.self).contains("shutdownCompleted"),
                  "\(source) next writer resumes disk after ownership release")
        }
        let attrs = try fm.attributesOfItem(atPath: safe.appendingPathComponent(".writer.lock").path)
        check((attrs[.posixPermissions] as! NSNumber).intValue == 0o600 && (attrs[.size] as! NSNumber).intValue == 0,
              "stable writer lock is private and stores no content")
        print("M07_DUAL_WRITER_PASS")
    }
    static func assertSafe(_ data: Data, _ name: String) {
        let text = String(decoding: data, as: UTF8.self)
        check(!text.contains(secret) && !text.contains(body) && !text.contains("Authorization"), name)
    }
    static func files(_ root: URL) throws -> [URL] {
        guard let enumerator = fm.enumerator(at: root, includingPropertiesForKeys: [.isRegularFileKey]) else { return [] }
        return enumerator.compactMap { $0 as? URL }.filter {
            (try? $0.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true
        }
    }
    static func projection(_ log: AppLog, _ source: AppLog.Source) throws -> Data {
        var response = try log.tail(source: source)
        var entries = response.entries
        while !response.entries.isEmpty {
            response = try log.tail(source: source, runID: response.runID, cursor: response.cursor)
            entries.append(contentsOf: response.entries)
        }
        return try JSONEncoder().encode(entries)
    }
    static func rejects(_ name: String, _ action: () throws -> Void) {
        do { try action(); preconditionFailure(name) } catch { print("PASS \(name)") }
    }
    @MainActor static func eventually(_ predicate: () -> Bool) async -> Bool {
        for _ in 0..<200 {
            if predicate() { return true }
            try? await Task.sleep(for: .milliseconds(20))
        }
        return predicate()
    }
    @MainActor static func main() async throws {
        if CommandLine.arguments.dropFirst().first == "writer-contender" { try contender(); return }
        if let mode = CommandLine.arguments.dropFirst().first, mode != "writer-only" { try child(mode); return }
        guard let location = ProcessInfo.processInfo.environment["WB_TEST_ROOT"] else { fatalError("isolated root required") }
        let root = URL(fileURLWithPath: location)
        precondition(root.path.hasPrefix("/private/tmp/"))
        if CommandLine.arguments.dropFirst().first == "writer-only" { try await dualWriter(root); return }
        try AppPaths.prepare()
        try await dualWriter(root)
        let legacy = try folder(root, "legacy")
        let raw = legacy.appendingPathComponent("desktop.log")
        try Data(secret.utf8).write(to: raw)
        let log = AppLog(directory: legacy, maxFileBytes: 512, memoryLimit: 7, emitSystemLog: false)
        var evaluated = false
        func untrusted() -> String { evaluated = true; return secret + body }
        log.info(untrusted()); log.warn(untrusted()); log.error(untrusted())
        check(!evaluated, "legacy String logging never evaluates or retains untrusted expression")
        for source in AppLog.Source.allCases {
            for _ in 0..<28 { log.record(.processExited, source: source, exitCode: -17, discardedBytes: UInt64.max) }
            let safe = legacy.appendingPathComponent("safe-events-v1")
            let names = try fm.contentsOfDirectory(atPath: safe.path).filter { $0.hasPrefix(source.rawValue + ".log") }
            check(Set(names) == Set(["\(source.rawValue).log", "\(source.rawValue).log.1", "\(source.rawValue).log.2"]), "\(source) retains exactly current plus two backups")
            for name in names {
                let url = safe.appendingPathComponent(name)
                let attrs = try fm.attributesOfItem(atPath: url.path)
                check((attrs[.size] as! NSNumber).intValue <= 512 && (attrs[.posixPermissions] as! NSNumber).intValue == 0o600,
                      "\(name) write-before-rotate size and 0600")
                assertSafe(try Data(contentsOf: url), "\(name) contains fixed events only")
            }
            assertSafe(try projection(log, source), "\(source) export projection excludes raw body")
        }
        check(try Data(contentsOf: raw) == Data(secret.utf8), "legacy raw file unchanged and excluded")
        let initial = try log.tail(source: .app)
        check(initial.entries.count == 7, "memory entry limit")
        let expired = try log.tail(source: .app, runID: initial.runID, cursor: 0, limit: 2)
        check(expired.reset && expired.entries.count == 2, "expired cursor resets within limit")
        let stale = try log.tail(source: .app, runID: UUID().uuidString, cursor: Int64.max, limit: 1)
        check(stale.reset && stale.entries.count == 1, "previous run cursor resets")
        check(try log.tail(source: .app, runID: initial.runID, cursor: initial.cursor).entries.isEmpty, "current cursor produces no duplicate entries")
        for limit in [-1, 0, 201] { rejects("reject limit \(limit)") { _ = try log.tail(source: .app, limit: limit) } }
        rejects("reject negative cursor") { _ = try log.tail(source: .app, runID: initial.runID, cursor: -1) }
        rejects("reject cursor without run") { _ = try log.tail(source: .app, cursor: 0) }
        rejects("reject malformed run") { _ = try log.tail(source: .app, runID: secret, cursor: 0) }
        rejects("reject future cursor") { _ = try log.tail(source: .app, runID: initial.runID, cursor: Int64.max) }
        check(AppLog.Source(rawValue: "../desktop.log") == nil, "no arbitrary path source")

        let links = try folder(root, "symlink")
        let safeLinks = try folder(links, "safe-events-v1")
        let sentinel = links.appendingPathComponent("sentinel")
        try Data(secret.utf8).write(to: sentinel)
        try fm.createSymbolicLink(at: safeLinks.appendingPathComponent("app.log"), withDestinationURL: sentinel)
        let linkLog = AppLog(directory: links, emitSystemLog: false)
        linkLog.record(.instanceStarted); linkLog.record(.processStarted, source: .panel)
        check(try Data(contentsOf: sentinel) == Data(secret.utf8), "file symlink never followed")
        check(try linkLog.tail(source: .app).entries.contains { $0.event == .diskLoggingDisabled }, "symlink disables only failed source")
        check(fm.fileExists(atPath: safeLinks.appendingPathComponent("panel.log").path), "other source keeps writing")
        let ancestor = root.appendingPathComponent("ancestor-link")
        try fm.createSymbolicLink(at: ancestor, withDestinationURL: links)
        let ancestorLog = AppLog(directory: ancestor, emitSystemLog: false)
        ancestorLog.record(.instanceStarted)
        check(try ancestorLog.tail(source: .app).entries.contains { $0.event == .diskLoggingDisabled }, "ancestor symlink rejected")

        let permissions = try folder(root, "permissions")
        try fm.setAttributes([.posixPermissions: 0o500], ofItemAtPath: permissions.path)
        let denied = AppLog(directory: permissions, emitSystemLog: false)
        for _ in 0..<2010 { denied.record(.instanceStarted) }
        let bounded = try projection(denied, .app)
        let boundedEntries = try JSONDecoder().decode([AppLog.Entry].self, from: bounded)
        check(boundedEntries.count <= 2000 && bounded.count <= AppLog.maxMemoryBytes + 1, "production memory bounded when disk disabled")
        check(!fm.fileExists(atPath: permissions.appendingPathComponent("safe-events-v1").path), "unwritable root never written")
        try fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: permissions.path)

        let rotation = try folder(root, "rotation-failure")
        let rotating = AppLog(directory: rotation, maxFileBytes: 256, emitSystemLog: false)
        rotating.record(.instanceStarted)
        let rotationSafe = rotation.appendingPathComponent("safe-events-v1")
        _ = try folder(rotationSafe, "app.log.2")
        for _ in 0..<5 { rotating.record(.instanceStarted) }
        let before = try Data(contentsOf: rotationSafe.appendingPathComponent("app.log"))
        rotating.record(.shutdownCompleted)
        check(try before.count <= 256 && before == Data(contentsOf: rotationSafe.appendingPathComponent("app.log")), "rotation failure stops append without exceeding size")
        check(try rotating.tail(source: .app).entries.last?.event == .shutdownCompleted, "rotation failure retains memory events")

        // Exercise write(2)'s error return without filling the disk. This test ignores
        // SIGXFSZ; it does NOT prove production survives an external RLIMIT_FSIZE signal.
        let failure = try folder(root, "write-failure")
        let failing = AppLog(directory: failure, maxFileBytes: 2048, emitSystemLog: false)
        var oldLimit = rlimit()
        precondition(getrlimit(RLIMIT_FSIZE, &oldLimit) == 0)
        var smallLimit = rlimit(rlim_cur: 256, rlim_max: oldLimit.rlim_max)
        let oldSignal = signal(SIGXFSZ, SIG_IGN)
        precondition(setrlimit(RLIMIT_FSIZE, &smallLimit) == 0)
        for _ in 0..<8 { failing.record(.instanceStarted) }
        precondition(setrlimit(RLIMIT_FSIZE, &oldLimit) == 0)
        signal(SIGXFSZ, oldSignal)
        let failedFile = failure.appendingPathComponent("safe-events-v1/app.log")
        let failedBytes = try Data(contentsOf: failedFile)
        failing.record(.shutdownCompleted)
        check(try failing.tail(source: .app).entries.contains { $0.event == .diskLoggingDisabled }, "write error return disables source (test ignores SIGXFSZ)")
        check(try Data(contentsOf: failedFile) == failedBytes, "disk failure does not retry writes")
        check(try failing.tail(source: .app).entries.last?.event == .shutdownCompleted, "disk failure preserves memory tail")

        for mode in ["valid", "bad", "crash"] {
            let processRoot = try folder(root, "process-" + mode)
            let processLog = AppLog(directory: processRoot, emitSystemLog: false)
            let outcomes = Outcomes()
            let proc = ManagedProcess(name: "wbgui", executableURL: URL(fileURLWithPath: CommandLine.arguments[0]),
                                      arguments: [mode], environment: [:], source: .panel, log: processLog)
            proc.onReady = { outcomes.ready($0) }
            proc.onExit = { outcomes.exit($0) }
            try proc.start(instanceID: instance)
            let ownPID = proc.pid!
            try proc.writeStdinLine(Data("{}".utf8))
            check(await eventually { !outcomes.snapshot.0.isEmpty }, "\(mode) bounded flood still drains to handshake")
            let result = outcomes.snapshot.0.first!
            if mode == "bad" {
                if case .failure(.invalidHandshake) = result { print("PASS malformed peer error becomes fixed failure") }
                else { preconditionFailure("malformed ready accepted") }
            } else {
                let ready = try result.get()
                check(ready.pid == ownPID && ready.port == 54321 && ready.instanceID == instance && ready.protocolVersion == 1,
                      "\(mode) valid ready retains identity/port/protocol")
                check(ready.version == "undisclosed", "\(mode) untrusted version never propagated")
            }
            if mode == "crash" {
                check(await eventually { outcomes.snapshot.1 == [17] }, "unexpected exit callback retains numeric code")
            } else {
                check(proc.isRunning, "\(mode) log flood leaves service running")
            }
            check(await proc.stop(timeout: 1), "\(mode) own child terminates with private stdin EOF")
            check(await eventually { outcomes.snapshot.1.count == 1 }, "\(mode) exit callback delivered once")
            check(!proc.isRunning && proc.pid == nil && proc.identity == nil, "\(mode) stopped identity cleared")
            let metrics = proc.outputMetrics
            check(metrics.peakBufferedBytes <= ManagedProcess.maxHandshakeBytes && metrics.bufferedBytes <= ManagedProcess.maxHandshakeBytes
                  && metrics.discardedBytes >= 1024 * 1024, "\(mode) output memory bounded across flood")
            check(outcomes.snapshot.0.count == 1, "\(mode) repeated ready cannot replay callback")
            assertSafe(Data(proc.outputTail.utf8), "\(mode) outputTail excludes unlabelled/multiline secrets")
            assertSafe(try projection(processLog, .panel), "\(mode) export projection excludes chat body")
            for file in try files(processRoot) { assertSafe(try Data(contentsOf: file), "\(mode) disk excludes raw stdout/stderr") }
            check(try processLog.tail(source: .panel).entries.count < 10, "\(mode) flood does not create per-line disk events")
        }
        print("M07_LOGS_STAGE1_SELF_CHECK_PASS")
    }
}
