import Foundation
import Combine
import AppKit
import Network
import Darwin

/// 服务编排器（开发计划 §6.1、§7.1、§8）。
///
/// 本应用是**唯一的进程管理者**：面板与网关两个子进程的生命周期全部由这里负责，
/// 不接受外部服务管家同时托管。
///
/// 三个贯穿始终的纪律：
///  1. **重入控制在原生层**，不靠按钮置灰。每个动作进入前检查是否有操作在途，
///     有则直接拒绝并给出提示（§7.2）。
///  2. **不做自动重启**。进程崩了就停在 `failed` 由用户决定（§8.2）。
///     无限自愈循环会把一次配置错误放大成持续的进程风暴。
///  3. **终止只针对自己创建的子进程**，核对 PID 与可执行路径（§8.2）。
@MainActor
final class ServiceManager: ObservableObject {

    // MARK: - 对外状态

    @Published private(set) var gatewayState: GatewayState = .stopped {
        didSet { if gatewayState != oldValue { publishGatewayStatus() } }
    }
    @Published private(set) var panelState: PanelState = .notStarted
    @Published private(set) var lastError: String? {
        didSet { if lastError != oldValue { publishGatewayStatus() } }
    }
    @Published private(set) var panelPort: Int?
    /// 网关 /healthz 的最近一次结果，用于展示「账号可用数」。
    @Published private(set) var gatewayHealth: GatewayHealth? {
        didSet { if gatewayHealth != oldValue { publishGatewayStatus() } }
    }
    /// 最近一次状态更新时间（用于「尚未刷新」标记）。
    @Published private(set) var stateUpdatedAt: Date = Date()

    let settings: AppSettings
    @Published private(set) var committedSnapshot: GatewayConfigFile.Snapshot?
    var apiPort: Int? { gatewayPort ?? committedSnapshot?.port }
    var apiBaseURL: String? { apiPort.map { "http://127.0.0.1:\($0)/v1" } }

    /// 面板会话 Cookie。仅在本进程内存中，不落盘、不进日志。
    ///
    /// 存的是**服务端下发的原始 Cookie 对象**，不是在原生层重新构造的 ——
    /// 自行构造时 `.secure` 会被写成 `true`，而面板是 `http://`，
    /// 于是浏览器静默丢弃它，面板永远停在登录页。详见 `PanelSessionClient.Session`。
    ///
    /// **必须是 `@Published`。** `PanelWebView` 靠它的变化把 Cookie 注入 WKWebView
    /// 并重新加载页面。不发布的话 SwiftUI 不会重新求值 → `updateNSView` 不被调用 →
    /// 注入永远不会发生。
    @Published private(set) var panelSessionCookie: HTTPCookie?
    private(set) var panelSessionUsername: String?

    // MARK: - 内部

    private let instanceLock = InstanceLock()
    private var panelProcess: ManagedProcess?
    private var gatewayProcess: ManagedProcess?
    private var gatewayStartedAt: (ManagedProcess, ContinuousClock.Instant)?
    static let lifecycleNotification = Notification.Name("WorkBuddyDesktopLifecycle")
    static let gatewayStatusNotification = Notification.Name("WorkBuddyDesktopGatewayStatus")
    private var lifecycleEpoch: UInt64 = 0
    private var gatewayStatusEpoch: UInt64 = 0
    private var gatewayStatusPublishPending = false
    private var sleeping = false

    private func publishGatewayStatus() {
        guard !gatewayStatusPublishPending else { return }
        gatewayStatusPublishPending = true
        Task { @MainActor [weak self] in
            await Task.yield() // Coalesce state, health and error assignments from one transition.
            guard let self else { return }
            self.gatewayStatusPublishPending = false
            self.gatewayStatusEpoch &+= 1
            NotificationCenter.default.post(name: Self.gatewayStatusNotification, object: nil,
                userInfo: ["instance_id": self.instanceID, "epoch": String(self.gatewayStatusEpoch)])
        }
    }
    @MainActor private final class ProxyAttempt {
        let id: String; let owner: NativeBridge.DocumentOwner; let revision: String; let instance: String
        let connection: NWConnection
        let deadline = ContinuousClock.now.advanced(by: .seconds(5))
        var outcome = "unavailable"
        var timer: Task<Void, Never>?
        var continuation: CheckedContinuation<[String: Any], Error>?
        var cancellations: [CheckedContinuation<Void, Never>] = []
        init(id: String, owner: NativeBridge.DocumentOwner, revision: String, instance: String, connection: NWConnection) {
            self.id = id; self.owner = owner; self.revision = revision; self.instance = instance; self.connection = connection
        }
    }
    private var proxyAttempt: ProxyAttempt?

    /// 当前在途的操作名。非空时拒绝新的进程操作（重入控制）。
    private var operationInFlight: String?

    /// 本次启动的实例 ID，随面板启动下发并回传校验。
    private(set) var instanceID = ""

    /// 内部口令：每次启动重新生成，只存在于本进程内存与面板内存。
    private var panelSecret = ""
    private var panelControlKey = ""
    private var panelUsername = PanelHandshake.desktopUsername

    /// 网关管理密钥：每次网关启动重新生成，只走私有 stdin，保留在原生内存供后续管理调用。
    private var gatewayManagementKey: String?
    private var gatewayPort: Int?

    /// 网关探活轮询任务。
    private var healthTask: Task<Void, Never>?
    private var gatewayStartupTask: Task<Bool, Never>?

    enum MaintenanceStopMode { case wait, abort }
    private let maintenanceLifetime: TimeInterval
    private let accountInteractionLifetime: TimeInterval
    private let maintenanceConfirmation: ((Bool) async -> MaintenanceStopMode?)?
    private var maintenanceGeneration: UInt64 = 0
    private var maintenance: MaintenanceFlow?
    private let authImport = NativeAuthImport()
    private var accountAttempt: AccountAttempt?
    private var authPreview: AuthPreview?
    private var serviceFlow: ServiceFlow?
    private var lastAppliedServiceRequest: (id: String, action: String, ownerID: UUID?)?
    var utilityDirectoryOpener: ((URL) -> Bool)? // Isolated opener seam; JS receives only fixed kinds.
    private var documentOwners: [UUID: (String, NativeBridge.DocumentOwner)] = [:]
    private lazy var utilities = DesktopUtilities(settings: settings, snapshot: { [weak self] in
        guard let self else { return .init(gateway: .stopped, panel: .notStarted, apiPort: nil) }
        return .init(gateway: self.gatewayState, panel: self.panelState, apiPort: self.apiPort,
                     healthyAccounts: self.gatewayHealth?.healthy, totalAccounts: self.gatewayHealth?.total,
                     listenHost: self.committedSnapshot?.host,
                     apiKeyConfigured: self.committedSnapshot.map { !$0.apiKey.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty })
    }, openDirectory: { [weak self] url in self?.utilityDirectoryOpener?(url) ?? NSWorkspace.shared.open(url) })
    var operationConfirmation: ((String, Bool) async -> MaintenanceStopMode?)?

    private struct GatewayBinding {
        let process: ManagedProcess
        let port: Int
        let key: String
        let generation: UInt64
    }
    @MainActor private final class ServiceFlow {
        let requestID: String
        let action: String
        let owner: NativeBridge.DocumentOwner?
        let panel: ManagedProcess?
        let panelPort: Int?
        let instance: String
        let controlKey: String
        let cookie: HTTPCookie?
        let old: GatewayConfigFile.Snapshot
        let gateway: GatewayBinding?
        let deadline = ContinuousClock.now.advanced(by: .seconds(900))
        var wasRunning: Bool { gateway != nil }
        var cancelled = false
        var phase = "confirming"
        var alert: NSAlert?
        var expiry: Task<Void, Never>?
        var work: Task<Void, Error>?
        var result: [String: Any]?
        var prepared: PanelSessionClient.PreparedConfig?
        var prepareUncertain = false
        var commitStarted = false
        var commitUncertain = false
        var candidateProcess: ManagedProcess?
        var finishStarted = false
        var finishAcknowledged = false
        var drainRequested = false
        var gatewayStopped = false
        var restoredOld = false
        var settled = false
        init(requestID: String, action: String, owner: NativeBridge.DocumentOwner?, panel: ManagedProcess?, panelPort: Int?,
             instance: String, controlKey: String, cookie: HTTPCookie?, old: GatewayConfigFile.Snapshot, gateway: GatewayBinding?) {
            self.requestID = requestID; self.action = action; self.owner = owner; self.panel = panel; self.panelPort = panelPort
            self.instance = instance; self.controlKey = controlKey; self.cookie = cookie; self.old = old; self.gateway = gateway
        }
    }
    var authorizationOpener: ((URL) -> Bool)? // Isolated test seam; JS never supplies URLs.
    var importFileChooser: (() async -> URL?)? { get { authImport.chooseFile } set { authImport.chooseFile = newValue } }

    @MainActor private final class AccountAttempt {
        let id: String?
        let importing: Bool
        let owner: NativeBridge.DocumentOwner?
        var cancelled = false
        var alert: NSAlert?
        var completed = false
        var waiters: [CheckedContinuation<Void, Never>] = []
        init(id: String?, importing: Bool, owner: NativeBridge.DocumentOwner?) { self.id = id; self.importing = importing; self.owner = owner }
        func complete() { completed = true; let pending = waiters; waiters.removeAll(); pending.forEach { $0.resume() } }
        func wait() async { if !completed { await withCheckedContinuation { waiters.append($0) } } }
    }
    private final class AuthPreview {
        let id = UUID().uuidString
        let credential: NativeAuthImport.Credential
        let requestID: String
        let owner: NativeBridge.DocumentOwner?
        let instance: String
        let expires: Date
        let overwriteRequired: Bool
        var expiry: Task<Void, Never>?
        init(credential: NativeAuthImport.Credential, owner: NativeBridge.DocumentOwner?, instance: String, overwriteRequired: Bool, lifetime: TimeInterval, requestID: String) {
            self.requestID = requestID
            self.expires = Date().addingTimeInterval(lifetime)
            self.credential = credential; self.owner = owner; self.instance = instance; self.overwriteRequired = overwriteRequired
        }
    }


    // ponytail: one maintenance transaction holds the existing native operation lock.
    // Concurrent account maintenance requires a shared writer design before relaxing it.
    private final class MaintenanceFlow {
        let panel: ManagedProcess
        let port: Int
        let instance: String
        let controlKey: String
        let generation: UInt64
        let action: String
        let uids: [String]
        let expiresAt: Date
        let gateway: ManagedProcess?
        let gatewayPort: Int?
        let gatewayKey: String?
        let wasRunning: Bool
        let owner: NativeBridge.DocumentOwner?
        var attempt: AccountAttempt?
        var cancelled = false
        var gatewayStopped = false
        var drainRequested = false
        var permitRequested = false
        var failure = NativeBridge.MaintenanceError.operation_failed
        var phase = "waiting"
        var preparation: Task<Bool, Never>?
        var cleanup: Task<Bool, Never>?
        var expiry: Task<Void, Never>?

        init(panel: ManagedProcess, port: Int, instance: String, controlKey: String,
             generation: UInt64, action: String, uids: [String], expiresAt: Date,
             gateway: ManagedProcess?, gatewayPort: Int?, gatewayKey: String?, wasRunning: Bool, owner: NativeBridge.DocumentOwner?) {
            self.panel = panel; self.port = port; self.instance = instance; self.controlKey = controlKey
            self.generation = generation; self.action = action; self.uids = uids; self.expiresAt = expiresAt
            self.gateway = gateway; self.gatewayPort = gatewayPort; self.gatewayKey = gatewayKey
            self.wasRunning = wasRunning
            self.owner = owner
        }
    }

    /// 应用是否已进入退出流程。
    private(set) var isShuttingDown = false

    /// 退出流程是否已完整跑完（子进程已停止、单实例锁已释放）。
    ///
    /// AppKit 的 `applicationShouldTerminate` 用它判断是否需要再次进入退出流程 ——
    /// 没有这个标记就会在 `reply(toApplicationShouldTerminate:)` 后二次触发、重复停进程。
    private(set) var didFinishShutdown = false

    struct GatewayHealth: Equatable {
        let healthy: Int
        let total: Int
    }

    init(settings: AppSettings, maintenanceLifetime: TimeInterval = 900, accountInteractionLifetime: TimeInterval = 120,
         maintenanceConfirmation: ((Bool) async -> MaintenanceStopMode?)? = nil) {
        self.settings = settings
        // Injectable only to exercise real expiry and native confirmation in isolated checks.
        self.accountInteractionLifetime = accountInteractionLifetime.isFinite ? min(120, max(0.1, accountInteractionLifetime)) : 120
        self.maintenanceLifetime = maintenanceLifetime.isFinite ? min(900, max(0.1, maintenanceLifetime)) : 900
        self.maintenanceConfirmation = maintenanceConfirmation
    }

    // MARK: - 启动

    /// 启动第一步：准备数据目录、取单实例锁、生成实例 ID、校正网关配置。
    ///
    /// **必须在主窗口出现之前调用。** 取不到锁时调用方应立即激活已有实例并退出，
    /// 而不是让用户先看到一个空窗口、再被关掉。
    /// - Returns: 是否取得单实例锁。false 表示已有实例在运行。
    func prepareForLaunch() -> Bool {
        lastError = nil

        do {
            try AppPaths.prepare()
        } catch {
            lastError = error.localizedDescription
            AppLog.shared.record(.directoryPreparationFailed, level: .error)
            return false
        }

        let gotLock: Bool
        do {
            gotLock = try instanceLock.acquire()
        } catch {
            lastError = error.localizedDescription
            AppLog.shared.record(.instanceLockFailed, level: .error)
            return false
        }
        guard gotLock else {
            AppLog.shared.record(.existingInstance)
            return false
        }

        instanceID = PanelHandshake.makeInstanceID()
        AppLog.shared.record(.instanceStarted)

        // 生成 / 校正网关配置（端口取用户设置，密钥缺失时新生成）。
        // 这里失败**不阻断启动** —— 面板仍要能打开，让用户看到原因并去改端口。
        do {
            committedSnapshot = try GatewayConfigFile.ensureInitial(port: settings.apiPort)
            AppLog.shared.record(.configurationReady)
        } catch {
            lastError = "写入网关配置失败：\(error.localizedDescription)"
            AppLog.shared.record(.configurationFailed, level: .error)
        }

        return true
    }

    /// 启动第二步：拉起内置面板。**必须在取得单实例锁之后调用。**
    ///
    /// 面板始终运行 —— 网关停止时用户仍要能看设置、导入账号、排查问题。
    @discardableResult
    func startPanelService() async -> Bool {
        await reconnectPanel()
    }

    /// 页面重试与桥重连共用此入口；存活面板只重建会话，退出面板重新握手。
    @discardableResult
    func reconnectPanel() async -> Bool {
        guard beginOperation("恢复管理连接") else { return false }
        defer { endOperation() }
        if panelProcess?.isRunning == true, panelPort != nil {
            return await establishPanelSession()
        }
        if let previous = panelProcess {
            guard await previous.stop(timeout: 2) else { return false }
            panelProcess = nil
        }
        guard !isShuttingDown else { return false }
        return await startPanel()
    }

    /// 启动面板。面板始终运行 —— 网关停止时用户仍要能看设置、导入账号、排查问题。
    private func startPanel() async -> Bool {
        let snapshot: GatewayConfigFile.Snapshot
        do { snapshot = try GatewayConfigFile.readSnapshot(); committedSnapshot = snapshot }
        catch { panelState = .failed(reason: "已有配置无效，已保留原文件"); lastError = panelState.label; return false }
        panelPort = nil
        panelSessionCookie = nil
        panelSessionUsername = nil
        instanceID = PanelHandshake.makeInstanceID()
        let launchID = instanceID
        panelState = .starting
        stateUpdatedAt = Date()

        let secret = PanelHandshake.randomSecret()
        let controlKey = PanelHandshake.randomSecret()
        guard !secret.isEmpty, !controlKey.isEmpty, secret != controlKey else {
            panelState = .failed(reason: "无法生成内部凭据（安全随机数不可用）")
            lastError = "无法生成内部凭据（安全随机数不可用）"
            return false
        }
        panelSecret = secret
        panelControlKey = controlKey

        let helper: URL
        do {
            helper = try AppPaths.helperExecutable(named: "wbgui")
        } catch {
            panelState = .failed(reason: error.localizedDescription)
            lastError = error.localizedDescription
            return false
        }

        let proc = ManagedProcess(
            name: "wbgui",
            executableURL: helper,
            arguments: ["--desktop"],
            environment: ChildEnvironment.build(),
            source: .panel
        )

        // 就绪消息通过回调送回主线程
        let readySignal = ReadySignal()
        proc.onReady = { result in
            switch result {
            case .success(let ready): readySignal.fulfill(ready)
            case .failure:
                readySignal.fail(AppError.panelHandshakeMalformed(detail: "内置面板握手无效"))
            }
        }
        proc.onExit = { [weak self, weak proc] code in
            readySignal.fail(AppError.panelExited(code: code, tail: ""))
            Task { @MainActor in
                guard let self, let proc, !self.isShuttingDown, self.panelProcess === proc else { return }
                self.panelProcess = nil
                self.panelPort = nil
                self.panelSessionCookie = nil
                self.panelSessionUsername = nil
                self.panelControlKey = ""
                let reason = "内置管理面板意外退出（退出码 \(code)）"
                self.panelState = .failed(reason: reason)
                self.lastError = reason
                self.stateUpdatedAt = Date()
                AppLog.shared.record(.panelExited, source: .panel, level: .error, exitCode: code)
                await self.panelConnectionLost(instanceID: launchID)
            }
        }

        do {
            try proc.start(instanceID: instanceID)
        } catch {
            panelState = .failed(reason: "无法启动内置管理面板：\(error.localizedDescription)")
            lastError = "无法启动内置管理面板：\(error.localizedDescription)"
            return false
        }
        panelProcess = proc

        // 下发启动参数（内部口令只走这条管道，不进命令行、不进日志）
        let payload = PanelHandshake.makeInitPayload(
            instanceID: instanceID,
            username: panelUsername,
            password: panelSecret,
            controlKey: panelControlKey,
            gatewayURL: "http://127.0.0.1:\(snapshot.port)",
            authDir: AppPaths.authDirectory.path,
            configFile: AppPaths.gatewayConfigFile.path,
            backupDir: AppPaths.backupDirectory.path,
            gatewayAPIKey: snapshot.apiKey,
            dangerousOps: false
        )
        do {
            try proc.writeStdinLine(payload)
        } catch {
            panelState = .failed(reason: "无法向内置面板下发启动参数：\(error.localizedDescription)")
            lastError = "无法向内置面板下发启动参数：\(error.localizedDescription)"
            _ = await proc.forceStop()
            return false
        }

        // 等待就绪，10 秒超时
        let ready: PanelReady
        do {
            ready = try await readySignal.wait(timeout: 10)
        } catch {
            guard !isShuttingDown, panelProcess === proc else { return false }
            // 只展示固定事件与丢弃计数；内置组件原始 stdout/stderr 不进入错误详情。
            let tail = proc.outputTail
            let reason = tail.isEmpty
                ? error.localizedDescription
                : error.localizedDescription + "\n\n内置组件最后输出：\n" + tail
            lastError = reason
            panelState = .failed(reason: reason)
            stateUpdatedAt = Date()
            AppLog.shared.record(.panelStartupFailed, source: .panel, level: .error)
            // 超时后停止本次启动的子进程，不留半死状态
            if await proc.forceStop(), panelProcess === proc { panelProcess = nil }
            return false
        }

        guard !isShuttingDown, panelProcess === proc, proc.isRunning else { return false }
        // 握手必须属于当前子进程和本次启动。
        guard ready.instanceID == launchID, ready.pid == proc.pid else {
            let reason = "面板回报的实例 ID 与本次启动不一致，已停止该面板进程"
            lastError = reason
            panelState = .failed(reason: reason)
            if await proc.forceStop(), panelProcess === proc { panelProcess = nil }
            return false
        }

        panelPort = ready.port
        panelState = .ready(port: ready.port, instanceID: ready.instanceID)
        stateUpdatedAt = Date()
        AppLog.shared.record(.panelReady, source: .panel)

        return await establishPanelSession()
    }

    /// 用内部口令登录面板，取得 HttpOnly 会话 Cookie 供 WKWebView 使用。
    ///
    /// 复用面板既有的会话系统，不另写一套账号体系（开发计划 §7.1 第 6 步）。
    private func establishPanelSession() async -> Bool {
        guard let port = panelPort, let proc = panelProcess, proc.isRunning,
              !isShuttingDown else { return false }
        let launchID = instanceID
        let client = PanelSessionClient(port: port)
        do {
            let session = try await client.login(username: panelUsername, password: panelSecret)
            guard !isShuttingDown, panelProcess === proc, proc.isRunning,
                  instanceID == launchID, panelPort == port else { return false }
            panelSessionCookie = session.cookie
            panelSessionUsername = session.username
            panelState = .ready(port: port, instanceID: launchID)
            lastError = nil
            AppLog.shared.record(.panelSessionReady, source: .panel)
            return true
        } catch {
            guard !isShuttingDown, panelProcess === proc, proc.isRunning,
                  instanceID == launchID else { return false }
            panelSessionCookie = nil
            panelState = .sessionExpired
            lastError = "无法建立管理连接：\(error.localizedDescription)"
            AppLog.shared.record(.panelSessionFailed, source: .panel, level: .error)
            return false
        }
    }

    // MARK: - 网关生命周期

    /// 启动网关。
    @discardableResult
    func startGateway() async -> Bool {
        guard beginOperation("启动 API 服务") else { return false }
        defer { endOperation() }
        return await runGatewayStartup()
    }

    private func runGatewayStartup() async -> Bool {
        let task = Task { await startGatewayProcess() }
        gatewayStartupTask = task
        defer { gatewayStartupTask = nil }
        return await task.value
    }

    private func startGatewayProcess() async -> Bool {
        guard !isShuttingDown else { return false }
        guard gatewayState.allowsStart else {
            lastError = "当前状态（\(gatewayState.label)）不允许启动"
            return false
        }

        let snapshot: GatewayConfigFile.Snapshot
        do {
            snapshot = try GatewayConfigFile.readSnapshot()
            guard snapshot.revision == committedSnapshot?.revision,
                  !snapshot.apiKey.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
                throw GatewayConfigFile.Failure.invalidConfiguration
            }
        } catch {
            gatewayState = .failed(reason: "配置已变化或 API 密钥为空，请通过配置页或密钥轮换修复")
            lastError = gatewayState.detail; return false
        }
        let port = snapshot.port

        // 端口冲突：明确告知，不杀陌生进程，不静默换端口
        let portCheck = PortInspector.probe(port: port, host: snapshot.host)
        if portCheck == .unavailable {
            gatewayState = .failed(reason: "无法检查 API 端口，请在本机准备检查中重试")
            lastError = gatewayState.detail; stateUpdatedAt = Date(); return false
        }
        if portCheck == .occupied {
            let occupant = PortInspector.describeOccupant(port: port)
            let reason = occupant.map { "API 端口 \(port) 已被 \($0) 占用" }
                ?? "API 端口 \(port) 已被其他程序占用"
            gatewayState = .failed(reason: reason)
            lastError = AppError.gatewayPortInUse(port: port).localizedDescription
            stateUpdatedAt = Date()
            AppLog.shared.record(.portOccupied, source: .gateway, level: .warning)
            return false
        }

        gatewayState = .starting
        stateUpdatedAt = Date()
        lastError = nil
        gatewayManagementKey = nil
        gatewayPort = nil

        let managementKey = PanelHandshake.randomSecret()
        guard !managementKey.isEmpty else {
            let reason = "无法生成网关管理凭据（安全随机数不可用）"
            gatewayState = .failed(reason: reason)
            lastError = reason
            return false
        }

        let helper: URL
        do {
            helper = try AppPaths.helperExecutable(named: "wb2api")
        } catch {
            gatewayState = .failed(reason: error.localizedDescription)
            lastError = error.localizedDescription
            return false
        }

        let proc = ManagedProcess(
            name: "wb2api",
            executableURL: helper,
            arguments: ["-desktop", "-config", AppPaths.gatewayConfigFile.path],
            environment: ChildEnvironment.build(),
            source: .gateway
        )
        proc.onExit = { [weak self, weak proc] code in
            Task { @MainActor in
                guard let self, let proc, !self.isShuttingDown, self.gatewayProcess === proc else { return }
                // 停止流程中的正常退出不当作失败
                if case .stopping = self.gatewayState { return }
                let tail = proc.outputTail
                self.gatewayProcess = nil
                self.stopHealthPolling()
                self.gatewayHealth = nil
                let reason = AppError.gatewayExited(code: code, tail: tail).localizedDescription
                self.gatewayManagementKey = nil
                self.gatewayPort = nil
                self.gatewayState = .failed(reason: "API 服务意外退出（退出码 \(code)）")
                self.lastError = reason
                self.stateUpdatedAt = Date()
                AppLog.shared.record(.gatewayExited, source: .gateway, level: .error, exitCode: code)
            }
        }

        // Keep the candidate before launch so partial startup/handshake failure is also recoverable.
        if let flow = serviceFlow, flow.phase == "starting", flow.prepared != nil, !flow.restoredOld {
            flow.candidateProcess = proc
        }
        do {
            try proc.start()
            gatewayStartedAt = (proc, ContinuousClock.now)
            let handshake = try JSONSerialization.data(withJSONObject: [
                "management_key": managementKey,
            ], options: [])
            try proc.writeStdinLine(handshake)
        } catch {
            let reason = "无法启动 API 服务：\(error.localizedDescription)"
            gatewayState = .failed(reason: reason)
            lastError = reason
            _ = await proc.forceStop()
            return false
        }
        gatewayManagementKey = managementKey
        gatewayPort = port
        gatewayProcess = proc

        let deadline = Date().addingTimeInterval(10)
        while Date() < deadline {
            guard !isShuttingDown, gatewayProcess === proc else { return false }
            if Task.isCancelled {
                _ = await stopGatewayProcess(gracePeriod: 2)
                return false
            }
            if !proc.isRunning { break }
            let health = await probeHealth(port: port)
            guard !isShuttingDown, gatewayProcess === proc, proc.isRunning else { return false }
            if Task.isCancelled {
                _ = await stopGatewayProcess(gracePeriod: 2)
                return false
            }
            if let health {
                // 503 + 零账号也是有效探活，无需等待超时或触发重启。
                applyHealth(health, port: port, processAlive: true)
                startHealthPolling()
                return true
            }
            try? await Task.sleep(nanoseconds: 200_000_000)
        }
        guard !isShuttingDown, gatewayProcess === proc else { return false }
        gatewayState = .stopping
        let exited = await proc.stop(timeout: 2)
        if exited, gatewayProcess === proc { gatewayProcess = nil }
        guard !isShuttingDown else { return false }
        gatewayManagementKey = nil
        gatewayPort = nil
        gatewayState = .failed(reason: "API 服务未能在 10 秒内通过探活")
        lastError = gatewayState.detail
        stateUpdatedAt = Date()
        return false
    }

    /// 停止网关。面板保持运行。
    @discardableResult
    func stopGateway(gracePeriod: TimeInterval = 10) async -> Bool {
        // Only a standalone start may be cancelled here. Config/maintenance own their startup.
        if gatewayState == .starting, operationInFlight == "启动 API 服务", let startup = gatewayStartupTask, !isShuttingDown {
            startup.cancel()
            _ = await startup.value
            return gatewayProcess == nil && gatewayState == .stopped
        }
        do { _ = try await beginServiceOperation(action: "gateway.stop", requestID: UUID().uuidString, owner: nil); return true }
        catch { return false }
    }

    private func stopGatewayProcess(gracePeriod: TimeInterval = 10) async -> Bool {
        gatewayState = .stopping
        stateUpdatedAt = Date()
        stopHealthPolling()
        let proc = gatewayProcess
        let exited = await proc?.stop(timeout: gracePeriod) ?? true
        if exited, gatewayProcess === proc { gatewayProcess = nil; gatewayManagementKey = nil; gatewayPort = nil }
        gatewayHealth = nil
        gatewayState = exited ? .stopped : .failed(reason: "API 服务未能退出")
        lastError = exited ? nil : gatewayState.detail
        stateUpdatedAt = Date()
        return exited
    }

    @discardableResult
    func restartGateway() async -> Bool {
        do { _ = try await beginServiceOperation(action: "gateway.restart", requestID: UUID().uuidString, owner: nil); return true }
        catch { return false }
    }

    /// 退出立即撤销进程身份；所有在途启动/登录在 await 返回后均检查此状态。
    func shutdown() async {
        guard !isShuttingDown else { return }
        isShuttingDown = true
        cancelProxyAttempt()
        for (_, entry) in documentOwners { retireDocument(entry.1) }
        if let flow = serviceFlow { cancelServiceFlow(flow); flow.expiry?.cancel(); serviceFlow = nil }
        cancelAccountAttempt()
        clearAuthPreview()
        stopHealthPolling()
        gatewayStartupTask?.cancel()
        maintenance?.cancelled = true
        maintenance?.failure = .shutting_down
        maintenance?.expiry?.cancel()
        maintenance?.preparation?.cancel()
        maintenance?.cleanup?.cancel()
        maintenance = nil // shutdown confirms both process exits; no stale task may restore them.
        let deadline = Date().addingTimeInterval(10)
        async let gatewayExited = stopGatewayProcess(gracePeriod: max(0, deadline.timeIntervalSinceNow))
        let panel = panelProcess
        panelPort = nil
        panelSessionCookie = nil
        panelSessionUsername = nil
        panelSecret = ""
        panelControlKey = ""
        let panelExited = await panel?.stop(timeout: max(0, deadline.timeIntervalSinceNow)) ?? true
        let apiExited = await gatewayExited
        if panelExited { panelProcess = nil }
        panelState = .notStarted
        guard panelExited, apiExited, gatewayProcess == nil else {
            lastError = "内置服务尚未完全退出"
            return
        }
        instanceLock.release()
        didFinishShutdown = true
        AppLog.shared.record(.shutdownCompleted)
    }

    // MARK: - Fixed desktop operations

    func registerDocument(_ owner: NativeBridge.DocumentOwner) {
        guard owner.isActive else { return }
        documentOwners[owner.id] = (instanceID, owner)
    }

    func retireDocument(_ owner: NativeBridge.DocumentOwner) {
        owner.invalidate()
        utilities.invalidate(owner: owner)
        if proxyAttempt?.owner === owner { cancelProxyAttempt() }
        documentOwners.removeValue(forKey: owner.id)
        if serviceFlow?.owner === owner, let flow = serviceFlow { cancelServiceFlow(flow) }
    }

    /// Only actual workspace notifications (or an isolated test) call this; no auto start or replay.
    func handleLifecycle(sleep: Bool) async {
        guard !isShuttingDown else { return }
        sleeping = sleep
        lifecycleEpoch &+= 1
        let epoch = lifecycleEpoch
        NotificationCenter.default.post(name: Self.lifecycleNotification, object: nil,
            userInfo: ["instance_id": instanceID, "epoch": String(epoch), "kind": sleep ? "sleep" : "wake"])
        cancelProxyAttempt()
        if sleep { stopHealthPolling(); return }
        guard let process = gatewayProcess, process.isRunning, let port = gatewayPort else { return }
        let health = await probeHealth(port: port)
        guard !isShuttingDown, !sleeping, lifecycleEpoch == epoch,
              gatewayProcess === process, process.isRunning, gatewayPort == port else { return }
        // Config/maintenance owns state while changing processes; a wake cannot unlock or replace it.
        if !isBusy { applyHealth(health, port: port, processAlive: true) }
        startHealthPolling()
    }

    func performCheck(_ request: NativeBridge.Request, owner: NativeBridge.DocumentOwner) async throws -> [String: Any] {
        guard owner.isActive, !Task.isCancelled, !isShuttingDown else { throw NativeBridge.OperationFailure(code: "cancelled") }
        registerDocument(owner)
        if request.action == "desktop.checkReadiness" {
            guard request.payload.isEmpty else { throw NativeBridge.OperationFailure(code: "invalid_payload") }
            if serviceFlow != nil {
                return ["checked_at": ISO8601DateFormatter().string(from: Date()), "ready": false,
                        "config_result": "busy", "checks": []]
            }
            var items: [[String: String]] = []
            for name in ["wbgui", "wb2api"] {
                items.append(["check": name, "result": (try? AppPaths.helperExecutable(named: name)) == nil ? "helper_missing" : "ok"])
            }
            for (name, url) in [("data", AppPaths.dataDirectory), ("logs", AppPaths.logDirectory),
                                ("auths", AppPaths.authDirectory), ("backups", AppPaths.backupDirectory), ("run", AppPaths.runDirectory)] {
                items.append(["check": name, "result": Self.privateWriteCheck(url) ? "ok" : "directory_unavailable"])
            }
            var result: [String: Any] = ["checked_at": ISO8601DateFormatter().string(from: Date()), "checks": items]
            do {
                let snapshot = try GatewayConfigFile.readSnapshot()
                guard snapshot.revision == committedSnapshot?.revision else {
                    result["config_result"] = "configuration_changed"; result["ready"] = false
                    return result
                }
                let own = gatewayProcess?.isRunning == true && gatewayPort == snapshot.port
                result["revision"] = snapshot.revision; result["port"] = snapshot.port
                result["port_result"] = own ? "owned" : PortInspector.probe(port: snapshot.port, host: snapshot.host).rawValue
                result["config_result"] = snapshot.apiKey.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "key_missing" : "ok"
                result["ready"] = items.allSatisfy { $0["result"] == "ok" } &&
                    (result["port_result"] as? String == "free" || own) && result["config_result"] as? String == "ok"
            } catch { result["config_result"] = "configuration_unavailable"; result["ready"] = false }
            return result
        }
        guard request.action == "desktop.checkProxy" else { throw NativeBridge.OperationFailure(code: "not_supported") }
        let payload = request.payload
        if payload["operation"] as? String == "cancel_pending" {
            guard Set(payload.keys) == ["operation", "request_id"], let id = payload["request_id"] as? String,
                  !id.isEmpty, id.utf8.count <= 128 else { throw NativeBridge.OperationFailure(code: "invalid_payload") }
            if let attempt = proxyAttempt, attempt.id == id, attempt.owner === owner {
                await withCheckedContinuation { continuation in
                    attempt.cancellations.append(continuation); cancelProxyAttempt()
                }
            }
            return ["cancelled": true]
        }
        guard Set(payload.keys) == ["revision"], let revision = payload["revision"] as? String,
              NativeBridge.validRevision(revision) else { throw NativeBridge.OperationFailure(code: "invalid_payload") }
        guard proxyAttempt == nil, !isBusy, !sleeping else { throw NativeBridge.OperationFailure(code: "busy") }
        let snapshot = try GatewayConfigFile.readSnapshot()
        guard snapshot.revision == revision, committedSnapshot?.revision == revision else { throw NativeBridge.OperationFailure(code: "revision_conflict") }
        if snapshot.proxyURL.isEmpty {
            return ["revision": revision, "checked_at": ISO8601DateFormatter().string(from: Date()), "result": "not_applicable", "tcp_only": true]
        }
        guard let endpoint = GatewayConfigFile.proxyEndpoint(snapshot.proxyURL), let port = NWEndpoint.Port(rawValue: endpoint.port) else {
            throw NativeBridge.OperationFailure(code: "invalid_configuration")
        }
        let host = endpoint.host.hasPrefix("[") ? String(endpoint.host.dropFirst().dropLast()) : endpoint.host
        let connection = NWConnection(host: NWEndpoint.Host(host), port: port, using: .tcp)
        let attempt = ProxyAttempt(id: request.id, owner: owner, revision: revision, instance: instanceID, connection: connection)
        proxyAttempt = attempt
        return try await withCheckedThrowingContinuation { continuation in
            attempt.continuation = continuation
            connection.stateUpdateHandler = { [weak self, weak attempt] state in
                Task { @MainActor in
                    guard let self, let attempt, self.proxyAttempt === attempt else { return }
                    switch state {
                    case .ready:
                        if attempt.outcome != "cancelled" && attempt.outcome != "expired" { attempt.outcome = "reachable" }
                        connection.cancel()
                    case .failed: connection.cancel()
                    case .cancelled: self.finishProxyAttempt(attempt)
                    default: break
                    }
                }
            }
            attempt.timer = Task { [weak self, weak attempt] in
                do { try await Task.sleep(for: .seconds(5)) } catch { return }
                guard let self, let attempt, self.proxyAttempt === attempt else { return }
                attempt.outcome = "expired"; connection.cancel()
            }
            connection.start(queue: .global(qos: .utility)) // TCP handshake only; never send/receive HTTP data.
        }
    }

    private func cancelProxyAttempt() {
        guard let attempt = proxyAttempt else { return }
        attempt.outcome = "cancelled"; attempt.connection.cancel()
    }
    private func finishProxyAttempt(_ attempt: ProxyAttempt) {
        guard proxyAttempt === attempt else { return }
        proxyAttempt = nil; attempt.timer?.cancel(); attempt.connection.stateUpdateHandler = nil
        var code: String?
        if !attempt.owner.isActive || isShuttingDown || attempt.instance != instanceID || attempt.outcome == "cancelled" { code = "cancelled" }
        else if ContinuousClock.now >= attempt.deadline || attempt.outcome == "expired" { code = "expired" }
        else if (try? GatewayConfigFile.readSnapshot().revision) != attempt.revision || committedSnapshot?.revision != attempt.revision { code = "revision_conflict" }
        if let code { attempt.continuation?.resume(throwing: NativeBridge.OperationFailure(code: code)) }
        else { attempt.continuation?.resume(returning: ["revision": attempt.revision, "checked_at": ISO8601DateFormatter().string(from: Date()), "result": attempt.outcome, "tcp_only": true]) }
        attempt.continuation = nil
        for continuation in attempt.cancellations { continuation.resume() }; attempt.cancellations.removeAll()
    }

    /// Explicit readiness only: a single byte in each exact private directory, always removed.
    private static func privateWriteCheck(_ url: URL) -> Bool {
        var directory = open("/", O_RDONLY | O_DIRECTORY | O_CLOEXEC)
        guard directory >= 0 else { return false }
        defer { close(directory) }
        for component in url.pathComponents.dropFirst() {
            guard component != ".", component != ".." else { return false }
            let next = openat(directory, component, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
            guard next >= 0 else { return false }; close(directory); directory = next
        }
        var status = stat()
        guard fstat(directory, &status) == 0, status.st_uid == getuid(), status.st_mode & 0o7777 == 0o700 else { return false }
        let name = ".readiness-" + UUID().uuidString
        let fd = openat(directory, name, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o600)
        guard fd >= 0 else { return false }
        defer { close(fd); unlinkat(directory, name, 0) }
        var byte: UInt8 = 0
        return Darwin.write(fd, &byte, 1) == 1
    }

    func performUtility(action: String, payload: [String: Any], owner: NativeBridge.DocumentOwner) async throws -> [String: Any] {
        registerDocument(owner)
        do { return try await utilities.dispatch(action: action, payload: payload, owner: owner) }
        catch { throw NativeBridge.OperationFailure(code: (error as? DesktopUtilities.Failure)?.rawValue ?? "operation_failed") }
    }

    func performConfiguration(_ request: NativeBridge.ConfigurationRequest, owner: NativeBridge.DocumentOwner) async throws -> [String: Any] {
        guard owner.isActive else { throw NativeBridge.OperationFailure(code: "stale_document") }
        registerDocument(owner)
        switch request {
        case let .apply(revision, patch, requestID):
            return try await beginServiceOperation(action: "gateway.applyConfig", requestID: requestID, owner: owner, revision: revision, patch: patch)
        case let .rotate(revision, requestID):
            return try await beginServiceOperation(action: "api.rotateKey", requestID: requestID, owner: owner, revision: revision)
        case let .cancelPending(action, requestID):
            return try await cancelServiceOperation(action: action, requestID: requestID, owner: owner)
        case .copyKey:
            guard beginOperation("复制 API 密钥") else { throw NativeBridge.OperationFailure(code: "busy") }
            defer { endOperation() }
            let snapshot = try GatewayConfigFile.readSnapshot()
            guard owner.isActive, snapshot.revision == committedSnapshot?.revision,
                  !snapshot.apiKey.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
                throw NativeBridge.OperationFailure(code: "CONFIG_CONFLICT")
            }
            NSPasteboard.general.clearContents()
            guard NSPasteboard.general.setString(snapshot.apiKey, forType: .string) else { throw NativeBridge.OperationFailure(code: "operation_failed") }
            return ["copied": true]
        }
    }

    func performGateway(_ request: NativeBridge.Request, owner: NativeBridge.DocumentOwner) async throws -> [String: Any] {
        guard owner.isActive else { throw NativeBridge.OperationFailure(code: "stale_document") }
        registerDocument(owner)
        if request.payload.isEmpty {
            return try await beginServiceOperation(action: request.action, requestID: request.id, owner: owner)
        }
        guard Set(request.payload.keys) == ["operation", "request_id"],
              request.payload["operation"] as? String == "cancel_pending",
              let id = request.payload["request_id"] as? String, !id.isEmpty, id.utf8.count <= 128 else {
            throw NativeBridge.OperationFailure(code: "invalid_payload")
        }
        return try await cancelServiceOperation(action: request.action, requestID: id, owner: owner)
    }

    private func cancelServiceOperation(action: String, requestID: String, owner: NativeBridge.DocumentOwner) async throws -> [String: Any] {
        if let completed = lastAppliedServiceRequest, completed.id == requestID,
           completed.action == action, completed.ownerID == owner.id { return ["cancelled": false, "applied": true] }
        guard let flow = serviceFlow, flow.action == action, flow.requestID == requestID else {
            // No matching live flow can be cancelled. Do not label an unobserved old result as cancelled.
            return ["cancelled": false, "outcome_unknown": true]
        }
        guard flow.owner === owner else { throw NativeBridge.OperationFailure(code: "stale_document") }
        cancelServiceFlow(flow)
        _ = try? await flow.work?.value
        guard serviceFlow !== flow else { throw NativeBridge.OperationFailure(code: "cleanup_failed") }
        return flow.finishAcknowledged ? ["cancelled": false, "applied": true] : ["cancelled": true, "applied": false]
    }

    private func beginServiceOperation(action: String, requestID: String, owner: NativeBridge.DocumentOwner?,
                                       revision: String? = nil, patch: [String: Any]? = nil) async throws -> [String: Any] {
        guard !Task.isCancelled, owner?.isActive != false else { throw NativeBridge.OperationFailure(code: "cancelled") }
        guard beginOperation(action) else { throw NativeBridge.OperationFailure(code: isShuttingDown ? "shutting_down" : "busy") }
        let snapshot: GatewayConfigFile.Snapshot
        do {
            guard let committed = committedSnapshot else { throw NativeBridge.OperationFailure(code: "CONFIG_INVALID") }
            snapshot = revision == nil ? committed : try GatewayConfigFile.readSnapshot()
            guard snapshot.revision == committed.revision, revision == nil || revision == snapshot.revision else {
                throw NativeBridge.OperationFailure(code: "CONFIG_CONFLICT")
            }
            if revision != nil {
                guard panelProcess?.isRunning == true, panelPort != nil, panelSessionCookie != nil, !panelControlKey.isEmpty else {
                    throw NativeBridge.OperationFailure(code: "operation_failed")
                }
            }
        } catch { endOperation(); throw error }
        var binding: GatewayBinding?
        if let proc = gatewayProcess {
            guard proc.isRunning, let port = gatewayPort, let key = gatewayManagementKey,
                  maintenanceGeneration < UInt64.max else { endOperation(); throw NativeBridge.OperationFailure(code: "operation_failed") }
            maintenanceGeneration += 1
            binding = GatewayBinding(process: proc, port: port, key: key, generation: maintenanceGeneration)
        }
        let flow = ServiceFlow(requestID: requestID, action: action, owner: owner, panel: panelProcess, panelPort: panelPort,
                               instance: instanceID, controlKey: panelControlKey, cookie: panelSessionCookie, old: snapshot, gateway: binding)
        serviceFlow = flow
        flow.expiry = Task { [weak self, weak flow] in
            do { try await Task.sleep(for: .seconds(900)) } catch { return }
            guard let self, let flow, self.serviceFlow === flow else { return }
            self.cancelServiceFlow(flow)
        }
        // Unstructured work stays correlated through JS cancellation and commit transport failure.
        let work = Task { flow.result = try await runServiceOperation(flow, revision: revision, patch: patch) }
        flow.work = work
        try await work.value
        return flow.result ?? [:]
    }

    private func checkServiceFlow(_ flow: ServiceFlow) throws {
        guard serviceFlow === flow, !flow.cancelled, flow.owner?.isActive != false,
              !isShuttingDown, ContinuousClock.now < flow.deadline else { throw NativeBridge.OperationFailure(code: "cancelled") }
        if flow.action == "gateway.applyConfig" || flow.action == "api.rotateKey" || flow.owner != nil {
            guard panelProcess === flow.panel, flow.panel?.isRunning == true, panelPort == flow.panelPort,
                  instanceID == flow.instance, panelControlKey == flow.controlKey else { throw NativeBridge.OperationFailure(code: "stale_document") }
        }
    }

    private func closeServiceAlert(_ flow: ServiceFlow) {
        guard let alert = flow.alert else { return }
        flow.alert = nil
        if let parent = alert.window.sheetParent { parent.endSheet(alert.window, returnCode: .cancel) }
        else if NSApp?.modalWindow === alert.window { NSApp.abortModal() }
        alert.window.orderOut(nil)
    }

    private func cancelServiceFlow(_ flow: ServiceFlow) {
        guard serviceFlow === flow else { return }
        flow.cancelled = true
        closeServiceAlert(flow)
        if flow.phase == "starting" { gatewayStartupTask?.cancel() }
    }

    private func confirmServiceOperation(_ flow: ServiceFlow) async -> MaintenanceStopMode? {
        if let operationConfirmation { return await operationConfirmation(flow.action, flow.wasRunning) }
        if let maintenanceConfirmation { return await maintenanceConfirmation(flow.wasRunning) }
        let alert = NSAlert(); flow.alert = alert
        alert.messageText = flow.action == "api.rotateKey" ? "轮换 API 密钥？" : flow.action == "gateway.applyConfig" ? "应用网关配置？" : flow.action == "gateway.restart" ? "重启 API 服务？" : "停止 API 服务？"
        alert.informativeText = flow.wasRunning ? "等待完成会先停止接收新请求。立即中止会中断在途请求；等待期间可以取消。" : "API 当前停止，应用配置后仍保持停止。"
        alert.addButton(withTitle: flow.wasRunning ? "等待完成" : "继续")
        if flow.wasRunning { alert.addButton(withTitle: "立即中止") }
        alert.addButton(withTitle: "取消")
        let expiry = Task { [weak self, weak flow] in
            do { try await Task.sleep(for: .seconds(120)) } catch { return }
            guard let self, let flow else { return }; self.cancelServiceFlow(flow)
        }
        defer { expiry.cancel(); closeServiceAlert(flow) }
        switch alert.runModal() {
        case .alertFirstButtonReturn: return .wait
        case .alertSecondButtonReturn where flow.wasRunning: return .abort
        default: return nil
        }
    }

    private func stopForServiceFlow(_ flow: ServiceFlow, mode: MaintenanceStopMode) async throws {
        try checkServiceFlow(flow)
        guard let binding = flow.gateway else { return }
        guard gatewayProcess === binding.process, binding.process.isRunning else { throw NativeBridge.OperationFailure(code: "operation_failed") }
        if mode == .wait {
            flow.phase = "waiting"
            if operationConfirmation == nil && maintenanceConfirmation == nil {
                guard let window = NSApp?.keyWindow ?? NSApp?.mainWindow ?? NSApp?.windows.first else { throw NativeBridge.OperationFailure(code: "busy") }
                let alert = NSAlert(); flow.alert = alert
                alert.messageText = "正在等待已接收的请求完成"
                alert.informativeText = "新的请求将暂停接收。取消后会恢复准入，API 继续运行。"
                alert.addButton(withTitle: "取消等待")
                alert.beginSheetModal(for: window) { [weak self, weak flow] _ in
                    guard let self, let flow, flow.alert === alert else { return }; self.cancelServiceFlow(flow)
                }
            }
            flow.drainRequested = true
            var drained: Bool
            do { drained = try await gatewayDrain(binding, operation: "begin") }
            catch NativeBridge.MaintenanceError.not_supported { flow.drainRequested = false; throw NativeBridge.MaintenanceError.not_supported }
            while !drained {
                try checkServiceFlow(flow)
                try await Task.sleep(for: .milliseconds(200))
                try checkServiceFlow(flow)
                drained = try await gatewayDrain(binding, operation: "status")
            }
        }
        try checkServiceFlow(flow)
        closeServiceAlert(flow); flow.phase = "stopping"
        guard gatewayProcess === binding.process, await stopGatewayProcess(gracePeriod: mode == .abort ? 0 : 10) else {
            throw NativeBridge.OperationFailure(code: "operation_failed")
        }
        flow.gatewayStopped = true
        try checkServiceFlow(flow)
    }

    private func runServiceOperation(_ flow: ServiceFlow, revision: String?, patch: [String: Any]?) async throws -> [String: Any] {
        do {
            try checkServiceFlow(flow)
            guard let mode = await confirmServiceOperation(flow) else { throw NativeBridge.OperationFailure(code: "cancelled") }
            try checkServiceFlow(flow)
            if let revision {
                flow.phase = "preparing"; flow.prepareUncertain = true
                do {
                    flow.prepared = try await PanelSessionClient(port: flow.panelPort!).prepareConfig(instance: flow.instance, controlKey: flow.controlKey, revision: revision, patch: patch)
                    flow.prepareUncertain = false
                } catch let error as PanelSessionClient.ConfigFailure { flow.prepareUncertain = error.uncertain; throw error }
                try checkServiceFlow(flow)
            }
            try await stopForServiceFlow(flow, mode: mode)
            try checkServiceFlow(flow)
            if let prepared = flow.prepared {
                flow.phase = "committing"; flow.commitStarted = true; flow.commitUncertain = true
                let client = PanelSessionClient(port: flow.panelPort!)
                let view: PanelSessionClient.ConfigView
                do {
                    view = try await client.commitConfig(instance: flow.instance, controlKey: flow.controlKey, backupID: prepared.backupID)
                    flow.commitUncertain = false
                } catch let error as PanelSessionClient.ConfigFailure { flow.commitUncertain = error.uncertain; throw error }
                try checkServiceFlow(flow)
                let snapshot = try GatewayConfigFile.readSnapshot()
                guard view.revision == prepared.candidateRevision, snapshot.revision == view.revision,
                      snapshot.port == view.port, snapshot.allowLAN == view.allowLAN else { throw NativeBridge.OperationFailure(code: "CONFIG_CONFLICT") }
                committedSnapshot = snapshot
                if !flow.wasRunning { gatewayState = .stopped }
                if flow.wasRunning {
                    flow.phase = "starting"
                    guard await runGatewayStartup() else { throw NativeBridge.OperationFailure(code: "operation_failed") }
                }
                try checkServiceFlow(flow)
                flow.phase = "finishing"; flow.finishStarted = true
                try await acknowledgeConfigFinish(flow, backupID: prepared.backupID)
                flow.finishAcknowledged = true
                lastAppliedServiceRequest = (flow.requestID, flow.action, flow.owner?.id)
                // Acknowledged finish cannot be restored: report applied even if cancellation arrived meanwhile.
                completeServiceFlow(flow)
                return ["config": view.json, "applied": true]
            }
            if flow.action == "gateway.restart" {
                flow.phase = "starting"; flow.restoredOld = true
                guard await runGatewayStartup() else { throw NativeBridge.OperationFailure(code: "operation_failed") }
                try checkServiceFlow(flow)
            }
            completeServiceFlow(flow)
            return ["ok": true]
        } catch {
            closeServiceAlert(flow)
            let cleaned = await cleanupServiceFlow(flow)
            if cleaned { completeServiceFlow(flow) }
            else if serviceFlow === flow {
                flow.phase = "blocked"
                lastError = "操作结果尚未安全确认，已保留保护锁。请保留配置并退出应用后检查，不能覆盖外部修改。"
                gatewayState = .failed(reason: lastError!)
            }
            let code = (error as? PanelSessionClient.ConfigFailure)?.code ?? (error as? NativeBridge.OperationFailure)?.code ?? (error as? NativeBridge.MaintenanceError)?.rawValue ?? "operation_failed"
            if cleaned { lastError = code == "not_supported" ? "网关不支持等待完成，API 保持运行；请明确选择立即中止或取消。" : code == "cancelled" ? nil : "操作未完成，已完成安全清理。" }
            throw NativeBridge.OperationFailure(code: cleaned ? code : "cleanup_failed")
        }
    }

    private func acknowledgeConfigFinish(_ flow: ServiceFlow, backupID: String) async throws {
        let client = PanelSessionClient(port: flow.panelPort!)
        do {
            try await client.finishConfig(instance: flow.instance, controlKey: flow.controlKey, backupID: backupID)
        } catch let error as PanelSessionClient.ConfigFailure where error.uncertain {
            // Exact-ID finish is idempotent in both applied and cancelled preparation.
            // A real successful ACK is still required; commit is never retried.
            try await client.finishConfig(instance: flow.instance, controlKey: flow.controlKey, backupID: backupID)
        }
    }

    private func cleanupServiceFlow(_ flow: ServiceFlow) async -> Bool {
        guard serviceFlow === flow, !isShuttingDown else { return false }
        flow.phase = "cleaning"
        // Stop only the candidate captured when this flow launched it. Do this before
        // depending on a live panel or a successful config GET during recovery.
        if let candidate = flow.candidateProcess {
            if gatewayProcess === candidate {
                guard await stopGatewayProcess(gracePeriod: 2), !candidate.isRunning else { return false }
            } else if candidate.isRunning {
                guard await candidate.stop(timeout: 2), !candidate.isRunning else { return false }
            }
        }
        // Once finish was sent, failure cannot prove the backup still exists. A
        // missing real ACK keeps protection; never attempt an impossible restore.
        guard !flow.finishStarted, !flow.prepareUncertain else { return false }
        if let prepared = flow.prepared {
            guard panelProcess === flow.panel, flow.panel?.isRunning == true, panelPort == flow.panelPort,
                  panelControlKey == flow.controlKey, instanceID == flow.instance, let cookie = flow.cookie else { return false }
            let client = PanelSessionClient(port: flow.panelPort!)
            do {
                // The GET is evidence of current bytes, not a server-completion fence.
                let current = try await client.configView(cookie: cookie)
                if flow.commitStarted && current.revision == prepared.candidateRevision {
                    guard gatewayProcess == nil else { return false }
                    let restored = try await client.restoreConfig(instance: flow.instance, controlKey: flow.controlKey,
                                                                   backupID: prepared.backupID, currentRevision: prepared.candidateRevision)
                    guard restored.revision == flow.old.revision, try GatewayConfigFile.readSnapshot().revision == flow.old.revision else { return false }
                    committedSnapshot = flow.old
                } else if current.revision == flow.old.revision && !flow.commitUncertain {
                    do { try await acknowledgeConfigFinish(flow, backupID: prepared.backupID) }
                    catch let error as PanelSessionClient.ConfigFailure {
                        // A completed, explicitly rejected commit can already have removed its transaction.
                        guard flow.commitStarted, !error.uncertain, error.code == "CONFIG_TRANSACTION" else { return false }
                    }
                } else { return false }
            } catch { return false }
        }
        if flow.drainRequested && !flow.gatewayStopped, let binding = flow.gateway {
            guard (try? await gatewayDrain(binding, operation: "end")) == true else { return false }
        }
        if flow.gatewayStopped && flow.wasRunning && gatewayProcess?.isRunning != true {
            guard !flow.restoredOld else { return flow.prepared == nil && gatewayProcess == nil }
            flow.restoredOld = true
            guard (try? GatewayConfigFile.readSnapshot().revision) == flow.old.revision else { return false }
            committedSnapshot = flow.old; gatewayState = .stopped
            return await runGatewayStartup()
        }
        return true
    }

    private func completeServiceFlow(_ flow: ServiceFlow) {
        guard serviceFlow === flow else { return }
        flow.settled = true; flow.expiry?.cancel(); closeServiceAlert(flow)
        serviceFlow = nil; endOperation(); stateUpdatedAt = Date()
        publishGatewayStatus()
    }

    // MARK: - 账号维护

    func performMaintenance(_ request: NativeBridge.MaintenanceRequest, owner: NativeBridge.DocumentOwner? = nil) async throws -> [String: Any] {
        guard owner?.isActive != false else { throw NativeBridge.MaintenanceError.stale_maintenance }
        switch request {
        case let .begin(action, uids, requestID):
            return try await beginMaintenance(action: action, uids: uids, owner: owner, requestID: requestID)
        case let .cancelPending(requestID, importing):
            if importing, let preview = authPreview, preview.requestID == requestID, preview.owner === owner {
                clearAuthPreview(); return ["cancelled": true, "drained": true]
            }
            guard let attempt = accountAttempt, attempt.id == requestID, attempt.importing == importing,
                  attempt.owner === owner else { throw NativeBridge.MaintenanceError.stale_maintenance }
            cancelAccountAttempt()
            if let flow = maintenance, flow.attempt === attempt {
                guard await finishMaintenance(flow) else { throw NativeBridge.MaintenanceError.operation_failed }
            }
            await attempt.wait()
            return ["cancelled": true, "drained": true]
        case let .openBrowser(sessionID):
            let flow = try activeMaintenance(owner: owner, action: "login")
            let url = try await PanelSessionClient(port: flow.port).authorizationURL(sessionID: sessionID, instance: flow.instance,
                                                                                  generation: flow.generation, controlKey: flow.controlKey)
            guard (try activeMaintenance(owner: owner, action: "login")) === flow else { throw NativeBridge.MaintenanceError.stale_maintenance }
            guard authorizationOpener?(url) ?? NSWorkspace.shared.open(url) else { throw NativeBridge.MaintenanceError.operation_failed }
            return ["opened": true]
        case let .importPreview(requestID): return try await previewImport(owner: owner, requestID: requestID)
        case let .importConfirm(previewID, overwrite, requestID): return try await confirmImport(previewID: previewID, overwrite: overwrite, owner: owner, requestID: requestID)
        case let .importCancel(previewID):
            guard let preview = authPreview, preview.id == previewID, preview.owner === owner else { throw NativeBridge.MaintenanceError.stale_maintenance }
            clearAuthPreview()
            return ["cancelled": true]
        case let .setEnabled(uid, enabled): return try await setAccountEnabled(uid: uid, enabled: enabled, owner: owner)
        case let .delete(uid):
            let flow = try activeMaintenance(owner: owner, action: "delete")
            guard flow.uids == [uid], let cookie = panelSessionCookie else { throw NativeBridge.MaintenanceError.stale_maintenance }
            try await PanelSessionClient(port: flow.port).deleteAccount(uid: uid, cookie: cookie, instance: flow.instance, generation: flow.generation)
            guard (try activeMaintenance(owner: owner, action: "delete")) === flow else { throw NativeBridge.MaintenanceError.stale_maintenance }
            settings.removeAccountNote(uid: uid)
            return ["ok": true, "message": "账号凭证已删除"]
        case let .end(instance, generation):
            guard let flow = maintenance, flow.instance == instance, flow.generation == generation,
                  flow.owner === owner else {
                throw NativeBridge.MaintenanceError.stale_maintenance
            }
            flow.failure = .cancelled
            guard await finishMaintenance(flow) else { throw NativeBridge.MaintenanceError.operation_failed }
            return ["drained": true]
        }
    }

    private func confirmMaintenance(wasRunning: Bool, attempt: AccountAttempt) async -> MaintenanceStopMode? {
        if let maintenanceConfirmation { return await maintenanceConfirmation(wasRunning) }
        let alert = NSAlert()
        attempt.alert = alert
        defer { attempt.alert = nil }
        alert.messageText = "开始账号维护？"
        alert.informativeText = wasRunning
            ? "维护会暂时停止 API，完成后恢复。等待完成会先停止接收新请求；立即中止会中断在途请求。"
            : "维护结束后 API 将保持停止。"
        alert.addButton(withTitle: wasRunning ? "等待完成" : "继续")
        if wasRunning { alert.addButton(withTitle: "立即中止") }
        alert.addButton(withTitle: "取消")
        switch alert.runModal() {
        case .alertFirstButtonReturn: return .wait
        case .alertSecondButtonReturn where wasRunning: return .abort
        default: return nil
        }
    }

    private func beginMaintenance(action: String, uids: [String], owner: NativeBridge.DocumentOwner?, requestID: String? = nil, importing: Bool = false) async throws -> [String: Any] {
        guard !Task.isCancelled, owner?.isActive != false else { throw NativeBridge.MaintenanceError.cancelled }
        guard beginOperation("维护账号") else {
            throw isShuttingDown ? NativeBridge.MaintenanceError.shutting_down : .busy
        }
        guard let panel = panelProcess, panel.isRunning, let port = panelPort,
              panelSessionCookie != nil, !panelControlKey.isEmpty, maintenanceGeneration < UInt64.max else {
            endOperation()
            throw NativeBridge.MaintenanceError.operation_failed
        }
        let attempt = AccountAttempt(id: requestID, importing: importing, owner: owner)
        accountAttempt = attempt
        defer {
            attempt.complete()
            if maintenance?.attempt !== attempt, accountAttempt === attempt { accountAttempt = nil }
        }
        let timeout = Task { [weak self, weak attempt] in
            do { try await Task.sleep(for: .seconds(self?.accountInteractionLifetime ?? 120)) } catch { return }
            guard let self, let attempt, self.accountAttempt === attempt else { return }
            self.cancelAccountAttempt()
        }
        defer { timeout.cancel() }
        let wasRunning = gatewayProcess?.isRunning == true
        let mode = await confirmMaintenance(wasRunning: wasRunning, attempt: attempt)
        timeout.cancel()
        guard let mode, !isShuttingDown, !attempt.cancelled, !Task.isCancelled,
              owner?.isActive != false else {
            endOperation()
            throw NativeBridge.MaintenanceError.cancelled
        }
        guard panelProcess === panel, panel.isRunning, panelPort == port else {
            endOperation()
            throw NativeBridge.MaintenanceError.operation_failed
        }
        maintenanceGeneration += 1
        let flow = MaintenanceFlow(panel: panel, port: port, instance: instanceID, controlKey: panelControlKey,
                                   generation: maintenanceGeneration, action: action, uids: uids,
                                   expiresAt: Date().addingTimeInterval(maintenanceLifetime),
                                   gateway: gatewayProcess, gatewayPort: gatewayPort,
                                   gatewayKey: gatewayManagementKey, wasRunning: wasRunning, owner: owner)
        flow.attempt = attempt
        maintenance = flow
        stateUpdatedAt = Date()
        flow.preparation = Task { await prepareMaintenance(flow, mode: mode) }
        flow.expiry = Task { [weak self, weak flow] in
            do { try await Task.sleep(for: .seconds(self?.maintenanceLifetime ?? 900)) }
            catch { return }
            guard let self, let flow, self.maintenance === flow else { return }
            flow.failure = .expired
            _ = await self.finishMaintenance(flow)
        }
        let granted = await flow.preparation!.value
        guard granted, maintenance === flow, !flow.cancelled, !isShuttingDown, owner?.isActive != false else {
            _ = await finishMaintenance(flow)
            throw flow.failure
        }
        return ["instance_id": flow.instance, "generation": String(flow.generation),
                "action": flow.action, "uids": flow.uids,
                "expires_at": ISO8601DateFormatter().string(from: flow.expiresAt)]
    }

    private func isPreparing(_ flow: MaintenanceFlow) -> Bool {
        maintenance === flow && !flow.cancelled && flow.attempt?.cancelled != true && !isShuttingDown &&
            flow.owner?.isActive != false &&
            isCurrentPanel(instanceID: flow.instance, port: flow.port) && panelProcess === flow.panel &&
            panelControlKey == flow.controlKey && Date() < flow.expiresAt
    }

    private func prepareMaintenance(_ flow: MaintenanceFlow, mode: MaintenanceStopMode) async -> Bool {
        do {
            guard isPreparing(flow) else { return false }
            if flow.wasRunning && mode == .wait {
                // Always close admission, even if no request is currently observable.
                flow.drainRequested = true
                var drained: Bool
                do { drained = try await gatewayDrain(flow, operation: "begin") }
                catch NativeBridge.MaintenanceError.not_supported {
                    flow.drainRequested = false // Only an unsupported begin proves no drain was installed.
                    throw NativeBridge.MaintenanceError.not_supported
                }
                while !drained && isPreparing(flow) {
                    try await Task.sleep(for: .milliseconds(200))
                    guard isPreparing(flow) else { return false }
                    drained = try await gatewayDrain(flow, operation: "status")
                }
            }
            guard isPreparing(flow) else { return false }
            flow.phase = "stopping"
            guard await stopGatewayProcess(gracePeriod: mode == .abort ? 0 : 10) else { return false }
            flow.gatewayStopped = true
            guard isPreparing(flow), gatewayProcess == nil else { return false }
            gatewayState = .maintenance
            stateUpdatedAt = Date()
            // A lost response may still mean the permit was installed, so cleanup must revoke it.
            flow.permitRequested = true
            flow.phase = "granting"
            let granted = try await PanelSessionClient(port: flow.port).maintenance(
                operation: "begin", instanceID: flow.instance, generation: flow.generation,
                controlKey: flow.controlKey, action: flow.action, uids: flow.uids, expiresAt: flow.expiresAt)
            if granted && isPreparing(flow) { flow.phase = "active"; return true }
            return false
        } catch {
            if let error = error as? NativeBridge.MaintenanceError {
                flow.failure = error
            }
            guard maintenance === flow, !isShuttingDown else { return false }
            lastError = flow.failure == .not_supported
                ? "网关尚不支持等待请求完成；API 保持运行，请明确选择立即中止或取消。"
                : "无法进入账号维护，正在收回许可。"
            return false
        }
    }

    /// This fixed endpoint uses the current owned gateway's private key, never a panel Cookie.
    private func gatewayDrain(_ flow: MaintenanceFlow, operation: String) async throws -> Bool {
        guard let proc = flow.gateway, let port = flow.gatewayPort, let key = flow.gatewayKey else {
            throw NativeBridge.MaintenanceError.operation_failed
        }
        return try await gatewayDrain(GatewayBinding(process: proc, port: port, key: key, generation: flow.generation), operation: operation)
    }

    private func gatewayDrain(_ binding: GatewayBinding, operation: String) async throws -> Bool {
        let proc = binding.process, port = binding.port, key = binding.key
        guard gatewayProcess === proc, proc.isRunning, gatewayPort == port, gatewayManagementKey == key,
              let url = URL(string: "http://127.0.0.1:\(port)/admin/desktop/drain") else {
            throw NativeBridge.MaintenanceError.operation_failed
        }
        let config = URLSessionConfiguration.ephemeral
        config.connectionProxyDictionary = [:]
        config.httpCookieStorage = nil
        config.httpShouldSetCookies = false
        config.timeoutIntervalForRequest = 5
        config.timeoutIntervalForResource = 5
        let session = URLSession(configuration: config, delegate: NoRedirect(), delegateQueue: nil)
        defer { session.invalidateAndCancel() }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue(key, forHTTPHeaderField: "X-WorkBuddy-Management-Key")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["operation": operation, "generation": binding.generation])
        struct Reply: Decodable { let ok: Bool; let generation: UInt64; let drained: Bool; let accepting: Bool }
        let (data, response) = try await session.data(for: request)
        guard gatewayProcess === proc, proc.isRunning, gatewayPort == port, gatewayManagementKey == key,
              let http = response as? HTTPURLResponse else { throw NativeBridge.MaintenanceError.operation_failed }
        if http.statusCode == 404 || http.statusCode == 501 { throw NativeBridge.MaintenanceError.not_supported }
        guard http.statusCode == 200, let reply = try? JSONDecoder().decode(Reply.self, from: data),
              reply.ok, reply.generation == binding.generation, reply.accepting == (operation == "end") else {
            throw NativeBridge.MaintenanceError.operation_failed
        }
        return operation == "end" || reply.drained
    }

    private func finishMaintenance(_ flow: MaintenanceFlow) async -> Bool {
        guard maintenance === flow else { return true }
        flow.cancelled = true
        flow.phase = "ending"
        flow.expiry?.cancel()
        if let cleanup = flow.cleanup { return await cleanup.value }
        let cleanup = Task { await cleanupMaintenance(flow) }
        flow.cleanup = cleanup
        return await cleanup.value
    }

    private func cleanupMaintenance(_ flow: MaintenanceFlow) async -> Bool {
        _ = await flow.preparation?.value
        guard maintenance === flow else { return true }
        var drained = !flow.permitRequested || !flow.panel.isRunning
        if !drained {
            drained = (try? await PanelSessionClient(port: flow.port).maintenance(
                operation: "end", instanceID: flow.instance, generation: flow.generation,
                controlKey: flow.controlKey)) == true
        }
        guard maintenance === flow else { return true }
        if !drained {
            // A timeout is not a drain acknowledgment. Confirm this exact panel has exited.
            drained = await flow.panel.stop(timeout: 2)
            if drained, panelProcess === flow.panel {
                panelProcess = nil; panelPort = nil; panelSessionCookie = nil; panelSessionUsername = nil
                panelSecret = ""; panelControlKey = ""
                panelState = .failed(reason: "账号写任务未能确认结束，已停止内置面板。请恢复管理连接。")
            }
        }
        guard maintenance === flow, drained else {
            flow.cleanup = nil
            flow.phase = "blocked"
            lastError = "账号维护尚未安全结束，API 保持停止。请重试结束维护或退出应用。"
            return false
        }
        // Cancelling a wait must reopen admission before releasing the native lock.
        if flow.drainRequested && !flow.gatewayStopped && flow.gateway?.isRunning == true {
            guard (try? await gatewayDrain(flow, operation: "end")) == true else {
                flow.cleanup = nil
                flow.phase = "blocked"
                lastError = "无法恢复网关请求准入，请重试取消维护或退出应用。"
                return false
            }
        }
        guard maintenance === flow else { return true }
        let needsRestore = flow.wasRunning && gatewayProcess?.isRunning != true
        if flow.gatewayStopped || needsRestore { gatewayState = .stopped }
        var restored = true
        if needsRestore && !isShuttingDown { restored = await runGatewayStartup() }
        guard maintenance === flow else { return restored }
        maintenance = nil
        if accountAttempt === flow.attempt { accountAttempt = nil }
        endOperation()
        stateUpdatedAt = Date()
        return restored
    }

    /// WebKit teardown, renderer failure and the owned panel's exit share the same cleanup.
    func panelConnectionLost(instanceID: String, ownerID: UUID? = nil) async {
        for (_, entry) in documentOwners where entry.0 == instanceID && (ownerID == nil || entry.1.id == ownerID) { retireDocument(entry.1) }
        if let flow = serviceFlow, flow.instance == instanceID, ownerID == nil || flow.owner?.id == ownerID { cancelServiceFlow(flow) }
        if instanceID == self.instanceID, let attempt = accountAttempt, ownerID == nil || attempt.owner?.id == ownerID { cancelAccountAttempt() }
        if let preview = authPreview, preview.instance == instanceID, ownerID == nil || preview.owner?.id == ownerID { clearAuthPreview() }
        guard let flow = maintenance, flow.instance == instanceID,
              ownerID == nil || flow.owner?.id == ownerID else { return }
        flow.failure = .cancelled
        _ = await finishMaintenance(flow)
    }

    private func cancelAccountAttempt() {
        guard let attempt = accountAttempt else { return }
        attempt.cancelled = true
        if let alert = attempt.alert, NSApp.modalWindow === alert.window { NSApp.abortModal() }
        if attempt.importing { authImport.cancel() }
    }

    private func clearAuthPreview() { authPreview?.expiry?.cancel(); authPreview = nil }

    private func activeMaintenance(owner: NativeBridge.DocumentOwner?, action: String) throws -> MaintenanceFlow {
        guard let flow = maintenance, flow.owner === owner, flow.action == action, flow.phase == "active", isPreparing(flow) else {
            throw NativeBridge.MaintenanceError.stale_maintenance
        }
        return flow
    }

    private func previewImport(owner: NativeBridge.DocumentOwner?, requestID: String) async throws -> [String: Any] {
        guard !Task.isCancelled, beginOperation("选择凭证文件") else { throw NativeBridge.MaintenanceError.busy }
        let attempt = AccountAttempt(id: requestID, importing: true, owner: owner)
        accountAttempt = attempt
        clearAuthPreview()
        let instance = instanceID
        let timeout = Task { [weak self, weak attempt] in
            do { try await Task.sleep(for: .seconds(self?.accountInteractionLifetime ?? 120)) } catch { return }
            guard let self, let attempt, self.accountAttempt === attempt else { return }
            self.cancelAccountAttempt()
        }
        defer { timeout.cancel(); attempt.complete(); if accountAttempt === attempt { accountAttempt = nil }; endOperation() }
        guard let url = await authImport.choose(), !attempt.cancelled, !Task.isCancelled, owner?.isActive != false,
              let port = panelPort, let cookie = panelSessionCookie, instanceID == instance else { throw NativeBridge.MaintenanceError.cancelled }
        let credential = try NativeAuthImport.read(url)
        let region = try await PanelSessionClient(port: port).existingRegion(uid: credential.uid, cookie: cookie)
        guard !attempt.cancelled, !Task.isCancelled, owner?.isActive != false, isCurrentPanel(instanceID: instance, port: port) else { throw NativeBridge.MaintenanceError.cancelled }
        guard region == nil || region == credential.region else { throw NativeBridge.MaintenanceError.region_conflict }
        let preview = AuthPreview(credential: credential, owner: owner, instance: instance, overwriteRequired: region != nil, lifetime: accountInteractionLifetime, requestID: requestID)
        authPreview = preview
        preview.expiry = Task { [weak self, weak preview] in
            do { try await Task.sleep(for: .seconds(self?.accountInteractionLifetime ?? 120)) } catch { return }
            guard let self, let preview, self.authPreview === preview else { return }
            self.clearAuthPreview()
        }
        return ["preview_id": preview.id, "uid": credential.uid, "region": credential.region,
                "expires_at": credential.expiresAt, "overwrite_required": preview.overwriteRequired]
    }

    private func confirmImport(previewID: String, overwrite: Bool, owner: NativeBridge.DocumentOwner?, requestID: String) async throws -> [String: Any] {
        guard let preview = authPreview, preview.id == previewID, preview.owner === owner, preview.instance == instanceID,
              Date() < preview.expires, owner?.isActive != false, !Task.isCancelled else { throw NativeBridge.MaintenanceError.stale_maintenance }
        guard !preview.overwriteRequired || overwrite else { throw NativeBridge.MaintenanceError.overwrite_required }
        clearAuthPreview() // one-shot: only this captured native value can reach the fixed import.
        _ = try await beginMaintenance(action: "import", uids: [preview.credential.uid], owner: owner, requestID: requestID, importing: true)
        let flow = try activeMaintenance(owner: owner, action: "import")
        do {
            guard let cookie = panelSessionCookie else { throw NativeBridge.MaintenanceError.operation_failed }
            try await PanelSessionClient(port: flow.port).importCredential(preview.credential.raw, overwrite: overwrite,
                                                                          cookie: cookie, instance: flow.instance, generation: flow.generation)
            guard (try activeMaintenance(owner: owner, action: "import")) === flow else { throw NativeBridge.MaintenanceError.stale_maintenance }
            guard await finishMaintenance(flow) else { throw NativeBridge.MaintenanceError.operation_failed }
            return ["ok": true]
        } catch {
            _ = await finishMaintenance(flow)
            throw error
        }
    }

    private func setAccountEnabled(uid: String, enabled: Bool, owner: NativeBridge.DocumentOwner?) async throws -> [String: Any] {
        guard beginOperation("更改账号可用状态") else { throw NativeBridge.MaintenanceError.busy }
        defer { endOperation() }
        guard let proc = gatewayProcess, proc.isRunning, let port = gatewayPort, let key = gatewayManagementKey,
              let url = URL(string: "http://127.0.0.1:\(port)/admin/accounts/\(uid)/\(enabled ? "enable" : "disable")") else { throw NativeBridge.MaintenanceError.operation_failed }
        let config = URLSessionConfiguration.ephemeral
        config.connectionProxyDictionary = [:]; config.httpCookieStorage = nil
        config.timeoutIntervalForRequest = 5; config.timeoutIntervalForResource = 5
        let client = URLSession(configuration: config, delegate: NoRedirect(), delegateQueue: nil)
        defer { client.invalidateAndCancel() }
        var request = URLRequest(url: url); request.httpMethod = "POST"
        request.setValue(key, forHTTPHeaderField: "X-WorkBuddy-Management-Key")
        request.httpBody = Data("{}".utf8)
        let (data, response) = try await client.data(for: request)
        guard owner?.isActive != false, !Task.isCancelled, gatewayProcess === proc, proc.isRunning, gatewayPort == port,
              gatewayManagementKey == key, (response as? HTTPURLResponse)?.statusCode == 200,
              let value = try? JSONSerialization.jsonObject(with: data) as? [String: Any], value["uid"] as? String == uid,
              value["manual_disabled"] as? Bool == !enabled else { throw NativeBridge.MaintenanceError.operation_failed }
        return ["ok": true]
    }

    // MARK: - 探活

    /// 探活网关 `/healthz`。
    ///
    /// 该端点不需要 API Key；返回体形如 `{"healthy":1,"total":2,"service":"workbuddy2api"}`。
    /// 进程未起或已死时连接会被拒绝，返回 nil。
    func probeHealth(port: Int) async -> GatewayHealth? {
        guard let url = URL(string: "http://127.0.0.1:\(port)/healthz") else { return nil }
        var request = URLRequest(url: url)
        request.timeoutInterval = 3
        // 本地回环调用必须直连，不经过任何代理
        request.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData

        let config = URLSessionConfiguration.ephemeral
        config.connectionProxyDictionary = [:]      // 显式清空代理（§16 T22）
        config.requestCachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        config.timeoutIntervalForRequest = 3
        let session = URLSession(configuration: config)

        do {
            let (data, response) = try await session.data(for: request)
            guard response is HTTPURLResponse,
                  let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                  object["service"] as? String == "workbuddy2api",
                  let healthy = object["healthy"] as? Int,
                  let total = object["total"] as? Int else {
                return nil
            }
            return GatewayHealth(healthy: healthy, total: total)
        } catch {
            return nil
        }
    }

    /// 按探活结果落状态。
    ///
    /// 这里是「进程活着 ≠ 可用」的落点：进程在跑但 healthy == 0 时是
    /// `runningUnavailable`（警告色），不是失败，也不触发重启。
    private func applyHealth(_ health: GatewayHealth?, port: Int, processAlive: Bool) {
        guard processAlive else {
            gatewayState = .failed(reason: "API 服务进程未运行")
            stateUpdatedAt = Date()
            return
        }
        gatewayHealth = health
        if let health, health.healthy > 0 {
            gatewayState = .runningReady
        } else {
            gatewayState = .runningUnavailable
        }
        stateUpdatedAt = Date()
    }

    private func startHealthPolling() {
        stopHealthPolling()
        guard let port = gatewayPort else { return }
        healthTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 5_000_000_000)
                guard let self, !Task.isCancelled else { return }
                // 只在运行中状态轮询，避免与启动/停止流程打架
                guard self.gatewayState.isProcessAlive else { continue }
                guard let proc = self.gatewayProcess, proc.isRunning else { continue }
                let health = await self.probeHealth(port: port)
                await MainActor.run {
                    guard !Task.isCancelled, !self.isShuttingDown,
                          self.gatewayProcess === proc, self.gatewayPort == port, proc.isRunning else { return }
                    // 维护中状态由账号流程管理，探活不覆盖它
                    if case .maintenance = self.gatewayState { return }
                    if case .starting = self.gatewayState { return }
                    if case .stopping = self.gatewayState { return }
                    self.applyHealth(health, port: port, processAlive: proc.isRunning)
                }
            }
        }
    }

    private func stopHealthPolling() {
        healthTask?.cancel()
        healthTask = nil
    }

    // MARK: - 重入控制

    /// 进入一个进程操作。返回 false 表示已有操作在途，调用方应直接返回。
    private func beginOperation(_ name: String) -> Bool {
        guard !isShuttingDown else { return false }
        if let current = operationInFlight {
            lastError = "正在\(current)，请等待完成后再操作"
            AppLog.shared.record(.operationRejected, level: .warning)
            return false
        }
        operationInFlight = name
        return true
    }

    private func endOperation() {
        operationInFlight = nil
    }

    /// 当前是否有进程操作在途（UI 用于显示忙碌指示）。
    var isBusy: Bool { operationInFlight != nil }

    // MARK: - 供桥与 UI 使用

    var canLoadPanel: Bool { panelState.isReady && panelSessionCookie != nil }

    func isCurrentPanel(instanceID: String, port: Int) -> Bool {
        !isShuttingDown && self.instanceID == instanceID && panelPort == port
            && panelProcess?.isRunning == true
    }

    func performBridgeAction(_ action: String) async -> String? {
        if isShuttingDown { return "shutting_down" }
        if isBusy && !(action == "gateway.stop" && gatewayState == .starting && gatewayStartupTask != nil) {
            return "busy"
        }
        let succeeded: Bool
        switch action {
        case "gateway.start": succeeded = await startGateway()
        case "gateway.stop": succeeded = await stopGateway()
        case "gateway.restart": succeeded = await restartGateway()
        case "desktop.reconnect": succeeded = await reconnectPanel()
        case "stats.reset": succeeded = await resetStatistics()
        default: return "unsupported_action"
        }
        return succeeded ? nil : (isShuttingDown ? "shutting_down" : "operation_failed")
    }

    /// 固定管理操作；目标来自当前已启动的子进程，不能被正在编辑的端口设置改写。
    private func resetStatistics() async -> Bool {
        guard beginOperation("重置本次运行统计") else { return false }
        defer { endOperation() }
        guard gatewayState == .runningReady || gatewayState == .runningUnavailable,
              let proc = gatewayProcess, proc.isRunning,
              let port = gatewayPort, let key = gatewayManagementKey,
              let url = URL(string: "http://127.0.0.1:\(port)/v1/stats/reset") else { return false }
        let config = URLSessionConfiguration.ephemeral
        config.connectionProxyDictionary = [:]
        config.httpCookieStorage = nil
        config.httpShouldSetCookies = false
        config.timeoutIntervalForRequest = 5
        config.timeoutIntervalForResource = 5
        let session = URLSession(configuration: config, delegate: NoRedirect(), delegateQueue: nil)
        defer { session.invalidateAndCancel() }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue(key, forHTTPHeaderField: "X-WorkBuddy-Management-Key")
        struct Result: Decodable { let ok: Bool }
        do {
            let (data, response) = try await session.data(for: request)
            guard !isShuttingDown, gatewayProcess === proc, proc.isRunning,
                  gatewayPort == port, gatewayManagementKey == key,
                  gatewayState == .runningReady || gatewayState == .runningUnavailable,
                  let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode),
                  let result = try? JSONDecoder().decode(Result.self, from: data) else { return false }
            return result.ok
        } catch { return false }
    }

    /// 本次启动的状态快照（开发计划 §7.2 `desktop.getStatus`）。
    func statusSnapshot() -> [String: Any] {
        var dict: [String: Any] = [
            "gateway_state": stateName(gatewayState),
            "gateway_label": gatewayState.label,
            "gateway_detail": gatewayState.detail,
            "panel_state": panelState.label,
            "instance_id": instanceID,
            "updated_at": ISO8601DateFormatter().string(from: stateUpdatedAt),
            "busy": isBusy,
        ]
        if let port = apiPort { dict["api_port"] = port }
        if let base = apiBaseURL { dict["api_base_url"] = base }
        let lanEnabled = committedSnapshot?.allowLAN ?? false
        dict["lan_enabled"] = lanEnabled
        if lanEnabled, gatewayProcess?.isRunning == true, let port = gatewayPort {
            dict["lan_api_base_urls"] = PortInspector.lanIPv4Addresses().map { "http://\($0):\(port)/v1" }
        } else { dict["lan_api_base_urls"] = [String]() }
        if let (process, start) = gatewayStartedAt, gatewayProcess === process, process.isRunning {
            dict["uptime_seconds"] = max(0, start.duration(to: .now).components.seconds)
        }
        dict["lifecycle_epoch"] = String(lifecycleEpoch)
        if let port = panelPort { dict["panel_port"] = port }
        if let health = gatewayHealth {
            dict["healthy_accounts"] = health.healthy
            dict["total_accounts"] = health.total
        }
        if let flow = maintenance {
            dict["maintenance_generation"] = String(flow.generation)
            dict["maintenance_action"] = flow.action
            dict["maintenance_phase"] = flow.phase
        }
        if let lastError { dict["last_error"] = lastError }
        return dict
    }

    private func stateName(_ state: GatewayState) -> String {
        switch state {
        case .stopped: return "stopped"
        case .starting: return "starting"
        case .runningReady: return "running_ready"
        case .runningUnavailable: return "running_unavailable"
        case .maintenance: return "maintenance"
        case .stopping: return "stopping"
        case .failed: return "failed"
        }
    }
}

// MARK: - 面板就绪信号

/// 面板 ready 消息的一次性等待器。
///
/// 单独成类是因为它要跨线程：回调来自子进程读取队列，等待发生在主 actor。
private final class ReadySignal: @unchecked Sendable {
    private let lock = NSLock()
    private var ready: PanelReady?
    private var error: Error?
    private var waiter: CheckedContinuation<PanelReady, Error>?

    func fulfill(_ value: PanelReady) {
        lock.lock()
        guard ready == nil, error == nil else { lock.unlock(); return }
        ready = value
        let waiter = self.waiter
        self.waiter = nil
        lock.unlock()
        waiter?.resume(returning: value)
    }

    func fail(_ err: Error) {
        lock.lock()
        guard ready == nil, error == nil else { lock.unlock(); return }
        error = err
        let waiter = self.waiter
        self.waiter = nil
        lock.unlock()
        waiter?.resume(throwing: err)
    }

    func wait(timeout: TimeInterval) async throws -> PanelReady {
        try await withCheckedThrowingContinuation { continuation in
            lock.lock()
            if let ready {
                lock.unlock()
                continuation.resume(returning: ready)
                return
            }
            if let error {
                lock.unlock()
                continuation.resume(throwing: error)
                return
            }
            waiter = continuation
            lock.unlock()

            DispatchQueue.global().asyncAfter(deadline: .now() + timeout) { [weak self] in
                guard let self else { return }
                self.lock.lock()
                guard self.ready == nil, self.error == nil else { self.lock.unlock(); return }
                let err = AppError.panelHandshakeTimeout(seconds: Int(timeout))
                self.error = err
                let waiter = self.waiter
                self.waiter = nil
                self.lock.unlock()
                waiter?.resume(throwing: err)
            }
        }
    }
}

// MARK: - 端口占用者描述

extension PortInspector {
    /// 供 ServiceManager 使用；内部转发到 `occupantDescription`。
    static func describeOccupant(port: Int) -> String? {
        occupantDescription(port: port)
    }
}
