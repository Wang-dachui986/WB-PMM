import Foundation

/// 单实例锁（开发计划 §7.1 第 2 步、§6.1「桌面应用是唯一进程管理者」）。
///
/// 用 `flock(2)` 而不是「检查 PID 文件是否存在」：
///   - flock 由内核在进程退出时自动释放，**崩溃/被强杀也不会留下死锁**。
///   - PID 文件在崩溃后会残留，需要额外的存活性判断，且 PID 会被系统复用，
///     「凭过期 PID 杀进程」正是开发计划 §8.2 明令禁止的做法。
///
/// 锁文件本身只用于互斥，其内容不参与任何判断。
final class InstanceLock {

    private var fileDescriptor: Int32 = -1
    private let path: String

    init(path: String = AppPaths.instanceLockFile.path) {
        self.path = path
    }

    deinit {
        release()
    }

    /// 尝试取得锁。返回 false 表示已有实例持有。
    func acquire() throws -> Bool {
        let fd = open(path, O_CREAT | O_RDWR, 0o600)
        guard fd >= 0 else {
            throw AppError.instanceLockFailed(detail: "无法打开锁文件 \(path)：\(errnoText())")
        }

        // LOCK_NB：拿不到就立刻返回，不阻塞 —— 第二个实例应当马上激活已有窗口。
        if flock(fd, LOCK_EX | LOCK_NB) != 0 {
            let code = errno
            close(fd)
            if code == EWOULDBLOCK { return false }
            throw AppError.instanceLockFailed(detail: "flock 失败：\(errnoText(code))")
        }

        fileDescriptor = fd
        return true
    }

    /// 释放锁。进程退出时内核也会释放，这里显式释放是为了支持测试中反复获取。
    func release() {
        guard fileDescriptor >= 0 else { return }
        _ = flock(fileDescriptor, LOCK_UN)
        close(fileDescriptor)
        fileDescriptor = -1
    }

    private func errnoText(_ code: Int32 = errno) -> String {
        String(cString: strerror(code))
    }
}
