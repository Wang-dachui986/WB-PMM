import Foundation

/// 网关服务状态机（开发计划 §8.1）。
///
/// 关键区分：**进程活着 ≠ 服务可用**。
/// 进程在跑但没有可用账号时是 `runningUnavailable`，不是 `runningReady`，
/// 也不是 `failed` —— 把它当成失败会导致无意义的重启，把用户引向错误的操作。
enum GatewayState: Equatable {
    /// 未运行
    case stopped
    /// 正在启动（进程已创建，尚未通过探活）
    case starting
    /// 进程在跑，且探活显示有可用账号
    case runningReady
    /// 进程在跑，但当前没有可用账号 / 上游繁忙
    case runningUnavailable
    /// 账号维护进行中，API 暂停
    case maintenance
    /// 正在停止（已发信号，等待退出）
    case stopping
    /// 启动失败或进程异常退出
    case failed(reason: String)

    /// 用户可见的文案。
    var label: String {
        switch self {
        case .stopped: return "已停止"
        case .starting: return "正在启动"
        case .runningReady: return "运行中 · 可调用"
        case .runningUnavailable: return "运行中 · 暂不可调用"
        case .maintenance: return "维护中"
        case .stopping: return "正在停止"
        case .failed: return "启动失败"
        }
    }

    /// 一句话说明「这个状态意味着什么」。
    var detail: String {
        switch self {
        case .stopped: return "API 服务未运行"
        case .starting: return "正在拉起网关进程…"
        case .runningReady: return "服务可用，可以发送请求"
        case .runningUnavailable: return "进程正常，但当前没有可用账号"
        case .maintenance: return "账号维护进行中，API 已暂停"
        case .stopping: return "正在等待在途请求结束…"
        case .failed(let reason): return reason
        }
    }

    /// 是否处于「进程存活」的状态（含不可用与维护中）。
    var isProcessAlive: Bool {
        switch self {
        case .starting, .runningReady, .runningUnavailable, .maintenance, .stopping:
            return true
        case .stopped, .failed:
            return false
        }
    }

    /// 是否正在过渡（UI 应禁用重复操作）。
    var isTransitional: Bool {
        switch self {
        case .starting, .stopping: return true
        default: return false
        }
    }

    /// 允许的操作。UI 据此启用/禁用按钮，**同时**原生层对每个动作做重入控制 ——
    /// 仅靠按钮置灰不能保证互斥（开发计划 §7.2）。
    var allowsStart: Bool {
        switch self {
        case .stopped, .failed: return true
        default: return false
        }
    }

    var allowsStop: Bool {
        switch self {
        case .starting, .runningReady, .runningUnavailable, .maintenance: return true
        default: return false
        }
    }

    var allowsRestart: Bool {
        switch self {
        case .runningReady, .runningUnavailable, .failed: return true
        default: return false
        }
    }

    /// 状态徽标的语义色调，与 `docs/design.md` §3.4 的映射一致。
    var tone: Tone {
        switch self {
        case .stopped: return .idle
        case .starting, .maintenance: return .accent
        case .runningReady: return .ok
        // 警告色而非危险色：进程是好的，只是暂时没有可用账号
        case .runningUnavailable: return .warn
        case .stopping: return .idle
        case .failed: return .danger
        }
    }

    enum Tone {
        case ok, warn, danger, accent, idle
    }
}

/// 面板（wbgui）连接状态。与网关状态**分开**表达。
///
/// 开发计划 §8.1：「面板连接状态与网关状态分开」。管理会话过期不等于 API 不可用，
/// 两者必须能独立呈现，否则用户会误以为服务挂了而去重启。
enum PanelState: Equatable {
    case notStarted
    case starting
    case ready(port: Int, instanceID: String)
    case sessionExpired
    case failed(reason: String)

    var isReady: Bool {
        if case .ready = self { return true }
        return false
    }

    var port: Int? {
        if case .ready(let port, _) = self { return port }
        return nil
    }

    var label: String {
        switch self {
        case .notStarted: return "未启动"
        case .starting: return "正在启动"
        case .ready: return "已就绪"
        case .sessionExpired: return "管理连接已过期"
        case .failed: return "启动失败"
        }
    }
}
