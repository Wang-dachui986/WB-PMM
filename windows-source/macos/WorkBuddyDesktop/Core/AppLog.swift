import Foundation
import Darwin
import os

/// Only fixed events and numbers cross this boundary. Persisted logs (including old raw
/// logs) are never input to tail/diagnostics. All mutation is synchronous under one lock:
/// no unbounded pending write queue, even when a child floods its pipes.
final class AppLog: @unchecked Sendable {
    enum Source: String, CaseIterable, Codable, Sendable { case app, panel, gateway }
    enum Level: String, Codable, Sendable { case info, warning, error }
    enum Event: String, Codable, Sendable {
        case appInfo, appWarning, appError
        case directoryPreparationFailed, instanceLockFailed, existingInstance, instanceStarted
        case configurationReady, configurationFailed, panelReady, panelSessionReady
        case panelStartupFailed, panelSessionFailed, panelExited, gatewayExited, portOccupied
        case shutdownCompleted, operationRejected
        case processStarted, processExited, processIdentityRejected, handshakeRejected
        case outputDiscarded, diskLoggingDisabled
    }
    struct Entry: Codable, Sendable {
        let sequence: Int64
        let timestamp: Double
        let source: Source
        let level: Level
        let event: Event
        let exitCode: Int32?
        let discardedBytes: UInt64?
    }
    struct Tail: Codable, Sendable {
        let runID: String
        let cursor: Int64
        let reset: Bool
        let entries: [Entry]
    }
    enum TailError: Error { case invalidLimit, invalidCursor }
    enum ArchiveError: Error { case notWriter, unavailable }

    static let shared = AppLog(directory: AppPaths.logDirectory)
    static let maxEventBytes = 4 * 1024
    static let maxMemoryBytes = 256 * 1024
    private let lock = NSLock()
    private let runID = UUID().uuidString
    private let directory: URL
    private let maxFileBytes: Int
    private let memoryLimit: Int
    private let emitSystemLog: Bool
    private let logger = Logger(subsystem: "cn.workbuddy.desktop", category: "safe-events")
    private var states: [Source: State] = [:]
    // Shared by all three sources; kept until deinit, never inherited by helpers.
    private var writerLockFD: Int32 = -1
    private struct State {
        var entries: [(Entry, Int)] = []
        var memoryBytes = 0
        var sequence: Int64 = 0
        var directoryFD: Int32 = -1
        var fileFD: Int32 = -1
        var disabled = false
    }

    // Smaller limits support bounded isolated checks; production defaults are fixed.
    init(directory: URL, maxFileBytes: Int = 10 * 1024 * 1024,
         memoryLimit: Int = 2000, emitSystemLog: Bool = true) {
        self.directory = directory
        self.maxFileBytes = max(256, min(maxFileBytes, 10 * 1024 * 1024))
        self.memoryLimit = max(1, min(memoryLimit, 2000))
        self.emitSystemLog = emitSystemLog
    }

    deinit {
        for state in states.values {
            if state.fileFD >= 0 { close(state.fileFD) }
            if state.directoryFD >= 0 { close(state.directoryFD) }
        }
        if writerLockFD >= 0 { close(writerLockFD) }
    }

    // Compatibility for callers outside this stage: the arbitrary expression is never
    // evaluated or retained. Lifecycle callers use explicit event cases below.
    func info(_ message: @autoclosure () -> String) { record(.appInfo) }
    func warn(_ message: @autoclosure () -> String) { record(.appWarning, level: .warning) }
    func error(_ message: @autoclosure () -> String) { record(.appError, level: .error) }

    func record(_ event: Event, source: Source = .app, level: Level = .info,
                exitCode: Int32? = nil, discardedBytes: UInt64? = nil) {
        lock.lock()
        defer { lock.unlock() }
        var state = states[source] ?? State()
        let data = append(event, source: source, level: level, exitCode: exitCode,
                          discardedBytes: discardedBytes, state: &state)
        if !state.disabled {
            do {
                if state.directoryFD < 0 { state.directoryFD = try openDirectory() }
                if try claimDiskWriter(directoryFD: state.directoryFD) {
                    try persist(data, source: source, state: &state)
                }
            } catch {
                state.disabled = true
                if state.fileFD >= 0 { close(state.fileFD); state.fileFD = -1 }
                if state.directoryFD >= 0 { close(state.directoryFD); state.directoryFD = -1 }
                // Memory only; never recursively attempt to log a disk failure.
                _ = append(.diskLoggingDisabled, source: source, level: .warning,
                           exitCode: nil, discardedBytes: nil, state: &state)
            }
        }
        states[source] = state
    }

    func tail(source: Source, runID requestedRun: String? = nil, cursor: Int64? = nil,
              limit: Int = 200) throws -> Tail {
        guard (1...200).contains(limit) else { throw TailError.invalidLimit }
        guard cursor == nil || (cursor! >= 0 && requestedRun != nil),
              requestedRun == nil || UUID(uuidString: requestedRun!) != nil else {
            throw TailError.invalidCursor
        }
        lock.lock()
        defer { lock.unlock() }
        let state = states[source] ?? State()
        let first = state.entries.first?.0.sequence ?? (state.sequence + 1)
        let staleRun = requestedRun != nil && requestedRun != runID
        if !staleRun, let cursor, cursor > state.sequence { throw TailError.invalidCursor }
        let reset = staleRun || (cursor != nil && cursor! < first - 1)
        let after = reset ? first - 1 : (cursor ?? first - 1)
        let entries = state.entries.lazy.map(\.0).filter { $0.sequence > after }.prefix(limit)
        let result = Array(entries)
        return Tail(runID: runID, cursor: result.last?.sequence ?? state.sequence,
                    reset: reset, entries: result)
    }

    func recentText(source: Source, limit: Int = 8) -> String {
        lock.lock()
        defer { lock.unlock() }
        return (states[source]?.entries.suffix(max(0, min(limit, 200))) ?? [])
            .map { String(decoding: Self.encode($0.0), as: UTF8.self) }.joined()
    }

    /// Removes only the six fixed safe-event archives, after native confirmation.
    /// Shares the writer/rotation lock; current descriptors and memory stay intact.
    func clearArchives() throws -> Int {
        lock.lock()
        defer { lock.unlock() }
        do {
            let dir = try openDirectory()
            defer { close(dir) }
            guard try claimDiskWriter(directoryFD: dir) else { throw ArchiveError.notWriter }
            var held = stat()
            guard fstat(writerLockFD, &held) == 0,
                  let named = try checkedFile(".writer.lock", directoryFD: dir),
                  held.st_dev == named.st_dev, held.st_ino == named.st_ino else { throw ArchiveError.unavailable }
            // Reject every abnormal entry before deleting any archive. No globs or
            // caller-supplied filenames, and no reads of historical log contents.
            var archives: [String] = []
            for source in Source.allCases {
                for suffix in [".1", ".2"] {
                    let name = "\(source.rawValue).log" + suffix
                    if try checkedFile(name, directoryFD: dir) != nil { archives.append(name) }
                }
            }
            for name in archives {
                guard unlinkat(dir, name, 0) == 0 else { throw ArchiveError.unavailable }
            }
            return archives.count
        } catch let error as ArchiveError { throw error }
        catch { throw ArchiveError.unavailable }
    }

    private func append(_ event: Event, source: Source, level: Level, exitCode: Int32?,
                        discardedBytes: UInt64?, state: inout State) -> Data {
        state.sequence += 1
        let entry = Entry(sequence: state.sequence, timestamp: Date().timeIntervalSince1970,
                          source: source, level: level, event: event,
                          exitCode: exitCode, discardedBytes: discardedBytes)
        let data = Self.encode(entry)
        precondition(data.count <= Self.maxEventBytes)
        state.entries.append((entry, data.count))
        state.memoryBytes += data.count
        while state.entries.count > memoryLimit || state.memoryBytes > Self.maxMemoryBytes {
            state.memoryBytes -= state.entries.removeFirst().1
        }
        if emitSystemLog {
            let fixedLine = String(decoding: data, as: UTF8.self)
            switch level {
            case .error: logger.error("\(fixedLine, privacy: .public)")
            case .warning: logger.warning("\(fixedLine, privacy: .public)")
            case .info: logger.info("\(fixedLine, privacy: .public)")
            }
        }
        return data
    }

    private static func encode(_ entry: Entry) -> Data {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        var data = try! encoder.encode(entry) // Fixed enums and finite numeric fields only.
        data.append(0x0A)
        return data
    }

    private enum DiskError: Error { case unsafeOrUnavailable }

    /// Walk with directory descriptors: never follow a symlink, including ancestors.
    /// The private versioned directory isolates pre-M07 untrusted *.log files.
    private func openDirectory() throws -> Int32 {
        guard directory.isFileURL, directory.path.hasPrefix("/") else { throw DiskError.unsafeOrUnavailable }
        var fd = open("/", O_RDONLY | O_DIRECTORY | O_CLOEXEC)
        guard fd >= 0 else { throw DiskError.unsafeOrUnavailable }
        do {
            let components = directory.pathComponents.dropFirst() + ["safe-events-v1"]
            for (index, component) in components.enumerated() {
                guard component != ".", component != "..", !component.contains("/") else {
                    throw DiskError.unsafeOrUnavailable
                }
                if mkdirat(fd, component, 0o700) != 0 && errno != EEXIST {
                    throw DiskError.unsafeOrUnavailable
                }
                let next = openat(fd, component, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
                guard next >= 0 else { throw DiskError.unsafeOrUnavailable }
                close(fd)
                fd = next
                // The log root and safe child both must be private and owned by us.
                if index >= components.count - 2 {
                    var status = stat()
                    guard fstat(fd, &status) == 0, status.st_uid == getuid(),
                          status.st_mode & 0o777 == 0o700 else { throw DiskError.unsafeOrUnavailable }
                }
            }
            return fd
        } catch { close(fd); throw error }
    }

    private func checkedFile(_ name: String, directoryFD: Int32) throws -> stat? {
        var status = stat()
        if fstatat(directoryFD, name, &status, AT_SYMLINK_NOFOLLOW) != 0 {
            if errno == ENOENT { return nil }
            throw DiskError.unsafeOrUnavailable
        }
        guard status.st_mode & S_IFMT == S_IFREG, status.st_uid == getuid(),
              status.st_mode & 0o777 == 0o600, status.st_nlink == 1,
              status.st_size >= 0, status.st_size <= maxFileBytes else {
            throw DiskError.unsafeOrUnavailable
        }
        return status
    }

    /// Multiple app instances may log before/after the application single-instance check.
    /// The loser keeps memory events and retries on its next event; it never opens or
    /// rotates source files. The lock file is stable and must never be unlinked on exit.
    private func claimDiskWriter(directoryFD: Int32) throws -> Bool {
        if writerLockFD >= 0 { return true }
        let name = ".writer.lock"
        let fd = openat(directoryFD, name, O_RDWR | O_CREAT | O_NOFOLLOW | O_CLOEXEC, 0o600)
        guard fd >= 0 else { throw DiskError.unsafeOrUnavailable }
        var status = stat()
        do {
            guard fstat(fd, &status) == 0,
                  let named = try checkedFile(name, directoryFD: directoryFD),
                  named.st_size == 0, status.st_dev == named.st_dev, status.st_ino == named.st_ino else {
                throw DiskError.unsafeOrUnavailable
            }
            if flock(fd, LOCK_EX | LOCK_NB) != 0 {
                if errno == EWOULDBLOCK { close(fd); return false }
                throw DiskError.unsafeOrUnavailable
            }
            writerLockFD = fd
            return true
        } catch { close(fd); throw error }
    }

    private func persist(_ data: Data, source: Source, state: inout State) throws {
        guard data.count <= maxFileBytes else { throw DiskError.unsafeOrUnavailable }
        let name = "\(source.rawValue).log"
        let dir = state.directoryFD
        if state.fileFD < 0 {
            // Validate every retained file before the first write. Never read old contents.
            for suffix in ["", ".1", ".2"] { _ = try checkedFile(name + suffix, directoryFD: dir) }
            state.fileFD = openat(dir, name, O_WRONLY | O_APPEND | O_CREAT | O_NOFOLLOW | O_CLOEXEC, 0o600)
        }
        guard state.fileFD >= 0 else { throw DiskError.unsafeOrUnavailable }
        var status = stat()
        guard fstat(state.fileFD, &status) == 0,
              let named = try checkedFile(name, directoryFD: dir),
              status.st_dev == named.st_dev, status.st_ino == named.st_ino else {
            throw DiskError.unsafeOrUnavailable
        }
        if status.st_size + Int64(data.count) > Int64(maxFileBytes) {
            // Any failed step disables this source; never append to the full file.
            for suffix in [".1", ".2"] { _ = try checkedFile(name + suffix, directoryFD: dir) }
            close(state.fileFD)
            state.fileFD = -1
            if try checkedFile(name + ".2", directoryFD: dir) != nil,
               unlinkat(dir, name + ".2", 0) != 0 { throw DiskError.unsafeOrUnavailable }
            if try checkedFile(name + ".1", directoryFD: dir) != nil,
               renameat(dir, name + ".1", dir, name + ".2") != 0 { throw DiskError.unsafeOrUnavailable }
            guard renameat(dir, name, dir, name + ".1") == 0 else { throw DiskError.unsafeOrUnavailable }
            state.fileFD = openat(dir, name, O_WRONLY | O_APPEND | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o600)
            guard state.fileFD >= 0, let fresh = try checkedFile(name, directoryFD: dir),
                  fresh.st_size == 0 else { throw DiskError.unsafeOrUnavailable }
        }
        try data.withUnsafeBytes { bytes in
            var written = 0
            while written < bytes.count {
                let count = Darwin.write(state.fileFD, bytes.baseAddress!.advanced(by: written), bytes.count - written)
                if count < 0 && errno == EINTR { continue }
                guard count > 0 else { throw DiskError.unsafeOrUnavailable }
                written += count
            }
        }
    }
}
