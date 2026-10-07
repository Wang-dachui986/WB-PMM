import SwiftUI
import AppKit
import CoreServices

/// 应用入口（开发计划 §6.1、§7.1）。
///
/// 原生壳的职责边界，只有四件事：
///   1. 窗口与菜单栏图标
///   2. 两个内置子进程的生命周期
///   3. Web 面板无法启动时的**原生**故障页
///   4. 单实例与退出编排
///
/// 它**不提供页面导航** —— 应用级导航只有侧栏一处（设计规范 §2.2）。
#if !WBPMM_LIFECYCLE_CHECK
@main
struct WorkBuddyDesktopApp: App {

    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        // 主窗口。内容全部来自内置面板（WKWebView 内的 React 应用）。
        Window("WB-PMM控制台", id: AppDelegate.mainWindowID) {
            MainWindowView()
                .environmentObject(delegate.services)
                .environmentObject(delegate.settings)
        }
        .defaultSize(width: 1180, height: 780)
        .windowResizability(.contentMinSize)
        .commands {
            // 单窗口应用：去掉「新建」菜单组，避免出现「新建窗口」这种无意义入口。
            CommandGroup(replacing: .newItem) {}
            CommandGroup(replacing: .appTermination) {
                Button("转入后台") { delegate.moveToBackground() }
                    .keyboardShortcut("q")
            }
        }

    }
}
#endif

// MARK: - 应用委托

/// 应用委托：单实例判定、窗口驻留、退出编排。
///
/// 这些动作都有明确的时序要求，放在 AppDelegate 而不是 SwiftUI 场景里，
/// 是因为 `applicationWillFinishLaunching` 提供了「窗口出现之前」这个唯一的判定点。
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, ObservableObject {

    /// 主窗口标识。菜单栏的「打开主窗口」与 Dock 点击都靠它找回窗口。
    static let mainWindowID = "main"
    static var reopenMainWindow: (() -> Void)?
    static var isLoginBackgroundLaunch = false

    let settings: AppSettings
    let services: ServiceManager
    private var observers: [(NotificationCenter, NSObjectProtocol)] = []
    private(set) var menuBar: MenuBarController?
    private var explicitQuit = false
    private var launchPrepared = false

    override init() {
        // 在 init 里构造，保证 SwiftUI 求值 body 时二者已就绪。
        let settings = AppSettings.forApplication()
        self.settings = settings
        self.services = ServiceManager(settings: settings)
        super.init()
    }

    // MARK: 启动

    func applicationWillFinishLaunching(_ notification: Notification) {
        // 单实例判定必须在这里完成：此时主窗口尚未显示。
        // 判定失败直接退出，用户不会看到窗口一闪而过。
        guard services.prepareForLaunch() else {
            if services.lastError == nil {
                // 没有报错 ⇒ 锁被另一个实例持有 ⇒ 激活它
                activateExistingInstance()
            } else {
                AppLog.shared.error("启动前置检查失败：\(services.lastError ?? "")")
            }
            NSApp.terminate(nil)
            return
        }
        launchPrepared = true
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        guard launchPrepared else { return }
        menuBar = MenuBarController(services: services, onQuit: { [weak self] in self?.quitFromMenuBar() })
        let event = NSAppleEventManager.shared().currentAppleEvent
        let launchedAtLogin = event?.eventID == kAEOpenApplication
            && event?.paramDescriptor(forKeyword: keyAEPropData)?.enumCodeValue == keyAELaunchedAsLogInItem
        if launchedAtLogin {
            Self.isLoginBackgroundLaunch = true
            NSApp.hide(nil)
            NSApp.setActivationPolicy(.accessory)
        }
        AppLog.shared.info("应用启动完成（版本 \(Self.appVersion)）")
        let workspace = NSWorkspace.shared.notificationCenter
        for (name, sleep) in [(NSWorkspace.willSleepNotification, true), (NSWorkspace.didWakeNotification, false)] {
            let token = workspace.addObserver(forName: name, object: nil, queue: .main) { [weak self] _ in
                Task { @MainActor in await self?.services.handleLifecycle(sleep: sleep) }
            }
            observers.append((workspace, token))
        }
        let center = NotificationCenter.default
        let token = center.addObserver(forName: NSWindow.willCloseNotification, object: nil, queue: .main) { [weak self] notice in
            let window = notice.object as? NSWindow
            MainActor.assumeIsolated {
                guard let self, let window,
                      window.identifier?.rawValue == Self.mainWindowID,
                      !self.services.isShuttingDown else { return }
                self.moveToBackground()
            }
        }
        observers.append((center, token))
        Task {
            await services.startPanelService()
            if settings.shouldStartGateway(atLogin: launchedAtLogin) { await services.startGateway() }
        }
    }

    // MARK: 窗口驻留

    /// 关闭最后一个窗口后**不退出**：应用继续在菜单栏运行，API 服务不中断。
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        false
    }

    /// Dock 图标被点击且没有可见窗口时，把主窗口找回来。
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        if !flag { Self.showMainWindow() }
        return true
    }

    /// 打开（或唤回）主窗口。
    static func activateForMainWindow() {
        NSApp.setActivationPolicy(.regular)
        NSApp.unhide(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    static func showMainWindow() {
        activateForMainWindow()
        if let reopenMainWindow { reopenMainWindow() }
        else { NSApp.windows.first { $0.identifier?.rawValue == mainWindowID }?.makeKeyAndOrderFront(nil) }
    }

    // MARK: 退出

    /// 普通退出（Dock、⌘Q）只隐藏窗口，保留面板与 API 进程。
    func moveToBackground() {
        menuBar?.ensureVisible()
        NSApp.hide(nil)
        NSApp.setActivationPolicy(.accessory)
    }

    func quitFromMenuBar() {
        guard !explicitQuit, !services.isShuttingDown else { return }
        explicitQuit = true
        // Finish async work before entering AppKit's termination event loop.
        Task { @MainActor in
            await services.shutdown()
            if services.didFinishShutdown { NSApp.terminate(nil) }
            else { explicitQuit = false }
        }
    }

    /// 系统注销、重启或关机仍正常释放子进程，不拦截系统退出。
    static func isSystemTermination(_ event: NSAppleEventDescriptor?) -> Bool {
        guard let event, event.eventID == kAEQuitApplication,
              let reason = event.paramDescriptor(forKeyword: kAEQuitReason)?.enumCodeValue else { return false }
        return [kAEQuitAll, kAEShutDown, kAERestart, kAEReallyLogOut].contains(reason)
    }

    /// 退出前先停子进程，停完再放行。
    ///
    /// 用 `.terminateLater` + `reply(toApplicationShouldTerminate:)`，而不是在主线程上同步等待：
    /// `shutdown()` 是主 actor 方法，同步等待会和它自己死锁。
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if !launchPrepared || services.didFinishShutdown { return .terminateNow }
        if explicitQuit { return .terminateCancel }
        guard Self.isSystemTermination(NSAppleEventManager.shared().currentAppleEvent) else {
            moveToBackground()
            return .terminateCancel
        }
        if services.isShuttingDown { return .terminateLater }
        AppLog.shared.info("收到退出请求，开始停止内置服务")
        Task { @MainActor in
            await services.shutdown()
            NSApp.reply(toApplicationShouldTerminate: services.didFinishShutdown)
        }
        return .terminateLater
    }

    func applicationWillTerminate(_ notification: Notification) {
        for (center, token) in observers { center.removeObserver(token) }; observers.removeAll()
        AppLog.shared.info("应用已退出")
    }

    // MARK: 辅助

    /// 激活已在运行的另一个实例。
    private func activateExistingInstance() {
        guard let bundleID = Bundle.main.bundleIdentifier, !bundleID.isEmpty else {
            AppLog.shared.warn("无法确定 Bundle 标识，跳过激活已有实例")
            return
        }
        let me = ProcessInfo.processInfo.processIdentifier
        let others = NSRunningApplication
            .runningApplications(withBundleIdentifier: bundleID)
            .filter { $0.processIdentifier != me }

        guard let existing = others.first else {
            AppLog.shared.warn("单实例锁已被占用，但未找到对应进程（可能是同一进程内的重复获取）")
            return
        }
        AppLog.shared.info("已有实例 PID \(existing.processIdentifier)，已请求激活")
        existing.activate(options: [.activateAllWindows])
    }

    static var appVersion: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
    }
}
