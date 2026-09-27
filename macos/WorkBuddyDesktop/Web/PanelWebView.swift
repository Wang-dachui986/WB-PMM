import SwiftUI
import AppKit
import WebKit

/// 面板 Web 容器：在 WKWebView 与原生故障页之间切换。
///
/// 开发计划 §6.1 的硬要求：「面板故障时原生壳显示原生错误页，不能因为网页打不开
/// 连『重试』和『退出』也不可用」。因此故障页必须是**原生视图**，
/// 而不是 Web 里渲染的 HTML —— 页面打不开时 HTML 也渲染不出来。
struct PanelContainerView: View {

    @ObservedObject var services: ServiceManager
    @Environment(\.colorScheme) private var colorScheme
    @State private var failure: String?
    @State private var reloadToken = UUID()
    @State private var isLoading = true

    var body: some View {
        Group {
            if let failure {
                NativeFailureView(
                    title: "管理面板无法打开",
                    statusColor: .red,
                    detail: failure,
                    primaryActionTitle: "重试",
                    onPrimary: {
                        Task {
                            if await services.reconnectPanel() {
                                self.failure = nil
                                self.isLoading = true
                                self.reloadToken = UUID()
                            }
                        }
                    },
                    onQuit: { NSApp.terminate(nil) }
                )
            } else if services.canLoadPanel, let port = services.panelPort {
                PanelWebView(
                    port: port,
                    instanceID: services.instanceID,
                    isCurrentInstance: { id, port in services.isCurrentPanel(instanceID: id, port: port) },
                    performAction: { await services.performBridgeAction($0) },
                    performMaintenance: { try await services.performMaintenance($0, owner: $1) },
                    onConnectionLost: { await services.panelConnectionLost(instanceID: $0, ownerID: $1) },
                    performConfiguration: { try await services.performConfiguration($0, owner: $1) },
                    performUtility: { try await services.performUtility(action: $0, payload: $1, owner: $2) },
                    performGateway: { try await services.performGateway($0, owner: $1) },
                    performCheck: { try await services.performCheck($0, owner: $1) },
                    onDocumentRetired: { services.retireDocument($0) },
                    sessionCookieProvider: { services.panelSessionCookie },
                    sessionCookie: services.panelSessionCookie,
                    reloadToken: reloadToken,
                    statusProvider: { services.statusSnapshot() },
                    onLoadStateChange: { loading in isLoading = loading },
                    onFailure: { message in failure = message }
                )
                .id(services.instanceID)
                .overlay(alignment: .top) {
                    if isLoading {
                        HStack(spacing: 8) {
                            ProgressView()
                                .controlSize(.small)
                                .tint(DesktopPalette.accent(colorScheme))
                                .accessibilityHidden(true)

                            Text("正在载入管理面板")
                                .font(.system(size: 12, weight: .medium))
                                .foregroundStyle(DesktopPalette.secondaryText(colorScheme))
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 8)
                        .background(DesktopPalette.surface(colorScheme), in: RoundedRectangle(cornerRadius: 8))
                        .overlay {
                            RoundedRectangle(cornerRadius: 8)
                                .strokeBorder(Color(nsColor: .separatorColor))
                        }
                        .padding(.top, 12)
                        .allowsHitTesting(false)
                    }
                }
            } else {
                NativeFailureView(
                    title: "管理面板尚未就绪",
                    statusColor: DesktopPalette.warningStatus,
                    // 面板启动失败时，用户需要的是**原因**而不是状态名。
                    detail: services.lastError ?? services.panelState.label,
                    primaryActionTitle: "重试",
                    onPrimary: {
                        Task { await services.reconnectPanel() }
                    },
                    onQuit: { NSApp.terminate(nil) }
                )
            }
        }
        .background(DesktopPalette.workspace(colorScheme))
    }
}

/// WKWebView 的 SwiftUI 包装。
///
/// 会话 Cookie 处理（开发计划 §7.1 第 6 步）：用**专属**的非持久化
/// `WKWebsiteDataStore`，把原生层通过 `/api/login` 取得的 HttpOnly 会话 Cookie
/// 写入其 `WKHTTPCookieStore` 后再加载页面。这样：
///   - 不需要在 URL 里带任何令牌（URL 会进历史记录与日志）
///   - 不与系统 Safari / 其他 WKWebView 共享 Cookie
///   - 应用退出后会话自动消失（面板内存态会话本就重启清零）
struct PanelWebView: NSViewRepresentable {

    let port: Int
    let instanceID: String
    let isCurrentInstance: @MainActor (String, Int) -> Bool
    let performAction: @MainActor (String) async -> String?
    var performMaintenance: (@MainActor (NativeBridge.MaintenanceRequest, NativeBridge.DocumentOwner) async throws -> [String: Any])? = nil
    var onConnectionLost: (@MainActor (String, UUID) async -> Void)? = nil
    var performConfiguration: (@MainActor (NativeBridge.ConfigurationRequest, NativeBridge.DocumentOwner) async throws -> [String: Any])? = nil
    var performUtility: (@MainActor (String, [String: Any], NativeBridge.DocumentOwner) async throws -> [String: Any])? = nil
    var performGateway: (@MainActor (NativeBridge.Request, NativeBridge.DocumentOwner) async throws -> [String: Any])? = nil
    var performCheck: (@MainActor (NativeBridge.Request, NativeBridge.DocumentOwner) async throws -> [String: Any])? = nil
    var onDocumentRetired: (@MainActor (NativeBridge.DocumentOwner) -> Void)? = nil
    let sessionCookieProvider: @MainActor () -> HTTPCookie?
    /// 服务端下发的会话 Cookie；未建立会话时由原生容器显示重试入口。
    let sessionCookie: HTTPCookie?
    let reloadToken: UUID
    /// 供桥命令 `desktop.getStatus` 取状态快照。刻意传闭包而不是 ServiceManager，
    /// 避免 Web 层直接依赖服务编排器的全部能力。
    let statusProvider: @MainActor () -> [String: Any]
    let onLoadStateChange: (Bool) -> Void
    let onFailure: (String) -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(port: port, instanceID: instanceID,
                    isCurrentInstance: isCurrentInstance,
                    performAction: performAction,
                    sessionCookieProvider: sessionCookieProvider,
                    statusProvider: statusProvider,
                    onLoadStateChange: onLoadStateChange,
                    onFailure: onFailure,
                    performMaintenance: performMaintenance,
                    onConnectionLost: onConnectionLost,
                    performConfiguration: performConfiguration, performUtility: performUtility,
                    performGateway: performGateway, onDocumentRetired: onDocumentRetired, performCheck: performCheck)
    }

    func makeNSView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        // 非持久化存储：Cookie / 缓存不落盘，也不与系统浏览器共享
        config.websiteDataStore = WKWebsiteDataStore.nonPersistent()
        config.defaultWebpagePreferences.allowsContentJavaScript = true
        // 原生桥：只注册一个消息通道，具体动作白名单在 Bridge 内校验（WB-03 扩充）
        config.userContentController.add(context.coordinator, name: NativeBridge.messageName)

        let webView = WKWebView(frame: .zero, configuration: config)
        webView.navigationDelegate = context.coordinator
        webView.uiDelegate = context.coordinator
        webView.allowsMagnification = true
        // 面板自身有滚动容器，WebView 不需要橡皮筋式整体滚动
        webView.allowsBackForwardNavigationGestures = false
        #if DEBUG
        if #available(macOS 13.3, *) { webView.isInspectable = true }
        #endif

        context.coordinator.webView = webView
        context.coordinator.load(port: port, sessionCookie: sessionCookie, reloadToken: reloadToken)
        return webView
    }

    func updateNSView(_ webView: WKWebView, context: Context) {
        context.coordinator.load(port: port, sessionCookie: sessionCookie, reloadToken: reloadToken)
    }

    static func dismantleNSView(_ webView: WKWebView, coordinator: Coordinator) {
        coordinator.invalidate()
        webView.configuration.userContentController
            .removeScriptMessageHandler(forName: NativeBridge.messageName)
        webView.navigationDelegate = nil
        webView.uiDelegate = nil
    }

    @MainActor
    final class Coordinator: NSObject, WKNavigationDelegate, WKUIDelegate, WKScriptMessageHandler {

        let port: Int
        let instanceID: String
        private let isCurrentInstance: @MainActor (String, Int) -> Bool
        private let performAction: @MainActor (String) async -> String?
        private let performMaintenance: (@MainActor (NativeBridge.MaintenanceRequest, NativeBridge.DocumentOwner) async throws -> [String: Any])?
        private let onConnectionLost: (@MainActor (String, UUID) async -> Void)?
        private let performConfiguration: (@MainActor (NativeBridge.ConfigurationRequest, NativeBridge.DocumentOwner) async throws -> [String: Any])?
        private let performUtility: (@MainActor (String, [String: Any], NativeBridge.DocumentOwner) async throws -> [String: Any])?
        private let performGateway: (@MainActor (NativeBridge.Request, NativeBridge.DocumentOwner) async throws -> [String: Any])?
        private let performCheck: (@MainActor (NativeBridge.Request, NativeBridge.DocumentOwner) async throws -> [String: Any])?
        private var lifecycleObserver: NSObjectProtocol?
        private let onDocumentRetired: (@MainActor (NativeBridge.DocumentOwner) -> Void)?
        private var confirmation: (UUID, NativeBridge.DocumentOwner, NSAlert, CheckedContinuation<Bool, Never>, ContinuousClock.Instant)?
        private var confirmationExpiry: Task<Void, Never>?
        private var documentOwner = NativeBridge.DocumentOwner()
        private var currentNavigation: WKNavigation?
        private var pendingRequests: [String: (NativeBridge.DocumentOwner, Task<Void, Never>, String)] = [:]
        private let sessionCookieProvider: @MainActor () -> HTTPCookie?
        private var cookieLoadID = UUID()
        private var isActive = true
        private var isActiveInstance: Bool {
            isActive && webView != nil && isCurrentInstance(instanceID, port)
        }

        func invalidate() {
            guard isActive else { return }
            retireDocument()
            isActive = false
            if let lifecycleObserver { NotificationCenter.default.removeObserver(lifecycleObserver) }; lifecycleObserver = nil
            cookieLoadID = UUID()
            webView = nil
        }
        weak var webView: WKWebView?
        private let statusProvider: @MainActor () -> [String: Any]
        private let onLoadStateChange: (Bool) -> Void
        private let onFailure: (String) -> Void
        private var lastLoadedToken: UUID?
        private var lastSessionCookieValue: String?

        init(port: Int, instanceID: String,
             isCurrentInstance: @escaping @MainActor (String, Int) -> Bool,
             performAction: @escaping @MainActor (String) async -> String?,
             sessionCookieProvider: @escaping @MainActor () -> HTTPCookie?,
             statusProvider: @escaping @MainActor () -> [String: Any],
             onLoadStateChange: @escaping (Bool) -> Void,
             onFailure: @escaping (String) -> Void,
             performMaintenance: (@MainActor (NativeBridge.MaintenanceRequest, NativeBridge.DocumentOwner) async throws -> [String: Any])? = nil,
             onConnectionLost: (@MainActor (String, UUID) async -> Void)? = nil,
             performConfiguration: (@MainActor (NativeBridge.ConfigurationRequest, NativeBridge.DocumentOwner) async throws -> [String: Any])? = nil,
             performUtility: (@MainActor (String, [String: Any], NativeBridge.DocumentOwner) async throws -> [String: Any])? = nil,
             performGateway: (@MainActor (NativeBridge.Request, NativeBridge.DocumentOwner) async throws -> [String: Any])? = nil,
             onDocumentRetired: (@MainActor (NativeBridge.DocumentOwner) -> Void)? = nil,
             performCheck: (@MainActor (NativeBridge.Request, NativeBridge.DocumentOwner) async throws -> [String: Any])? = nil) {
            self.port = port
            self.instanceID = instanceID
            self.isCurrentInstance = isCurrentInstance
            self.performAction = performAction
            self.performMaintenance = performMaintenance
            self.onConnectionLost = onConnectionLost
            self.performConfiguration = performConfiguration; self.performUtility = performUtility
            self.performGateway = performGateway; self.onDocumentRetired = onDocumentRetired
            self.sessionCookieProvider = sessionCookieProvider
            self.statusProvider = statusProvider
            self.onLoadStateChange = onLoadStateChange
            self.onFailure = onFailure
            self.performCheck = performCheck
            super.init()
            lifecycleObserver = NotificationCenter.default.addObserver(forName: ServiceManager.lifecycleNotification, object: nil, queue: .main) { [weak self] notice in
                let instanceID = notice.userInfo?["instance_id"] as? String
                let epoch = notice.userInfo?["epoch"] as? String
                let kind = notice.userInfo?["kind"] as? String
                MainActor.assumeIsolated {
                    guard let self, instanceID == self.instanceID,
                          let epoch, UInt64(epoch) != nil,
                          let kind, ["sleep", "wake"].contains(kind) else { return }
                    let owner = self.documentOwner
                    guard self.isCurrentDocument(owner) else { return }
                    let value: [String: Any] = ["document_id": owner.id.uuidString, "epoch": epoch, "kind": kind]
                    let script = "if (window.__workbuddyDocumentID === '\(owner.id.uuidString)') { window.dispatchEvent(new CustomEvent('workbuddy:lifecycle', {detail: " + NativeBridge.encodeForJavaScript(value) + "})); }"
                    self.webView?.evaluateJavaScript(script, completionHandler: nil)
                }
            }
        }

        /// 把会话 Cookie 写入 WebView 专属 Cookie 存储后加载首页。
        ///
        /// 注入的是**服务端下发的原始 Cookie 对象**。曾经在原生层用
        /// `HTTPCookie(properties:)` 重建，结果 `.secure` 变成 `true`，
        /// 而面板是 `http://` —— 浏览器不会在明文 HTTP 上发送 Secure Cookie，
        /// 面板因此永远停在登录页，且「Cookie 已写入存储」的日志看起来完全正常。
        func load(port: Int, sessionCookie: HTTPCookie?, reloadToken: UUID) {
            guard let webView, isActiveInstance, self.port == port,
                  let cookie = sessionCookie else { return }
            let shouldNavigate = lastLoadedToken != reloadToken || webView.url == nil
            guard shouldNavigate || lastSessionCookieValue != cookie.value else { return }
            lastLoadedToken = reloadToken
            lastSessionCookieValue = cookie.value
            // A suspended window.confirm can delay WebKit cookie/navigation callbacks.
            // Native reload must retire its document synchronously before either await.
            if shouldNavigate { retireDocument() }
            let loadID = UUID()
            let owner = documentOwner
            cookieLoadID = loadID
            webView.configuration.websiteDataStore.httpCookieStore.setCookie(cookie) {
                Task { @MainActor in
                    guard self.cookieLoadID == loadID,
                          self.isCurrentDocument(owner), self.webView === webView else { return }
                    // 恢复会话只更新 Cookie；已提交的写操作和聊天不被重新加载。
                    if shouldNavigate, let url = URL(string: "http://127.0.0.1:\(port)/") {
                        self.onLoadStateChange(true)
                        webView.load(URLRequest(url: url))
                    }
                }
            }
        }

        // MARK: 导航策略

        /// 只允许主页面停留在面板自身的 origin；其余一律交给系统浏览器。
        ///
        /// 目的：面板是管理界面，任何外站都不应该在这个 WebView 里获得
        /// 「处在受信 origin 内」的位置感，也避免用户误以为外部页面是本应用的一部分。
        ///
        /// 用 async 版本而不是 `decisionHandler` 版本：后者在 SDK 里的闭包参数带
        /// `@MainActor` 标注，手写签名容易「nearly matches」而静默失效 ——
        /// 静默失效意味着导航策略根本没生效，属于不能容忍的失败模式。
        func webView(_ webView: WKWebView,
                     decidePolicyFor navigationAction: WKNavigationAction) async -> WKNavigationActionPolicy {
            guard isActiveInstance, self.webView === webView,
                  let url = navigationAction.request.url else {
                return .cancel
            }

            let isMainFrame = navigationAction.targetFrame?.isMainFrame ?? true

            if isPanelOrigin(url) {
                if navigationAction.targetFrame?.isMainFrame == true {
                    // A fragment-only link retains its document; reload and full navigation replace it.
                    var current = webView.url.flatMap { URLComponents(url: $0, resolvingAgainstBaseURL: false) }
                    var next = URLComponents(url: url, resolvingAgainstBaseURL: false)
                    let sameFragmentLink = navigationAction.navigationType == .linkActivated && current?.fragment != next?.fragment
                    current?.fragment = nil; next?.fragment = nil
                    if !sameFragmentLink || current?.url != next?.url { retireDocument() }
                }
                return .allow
            }

            // 非面板 origin：主页面跳转交给系统浏览器；子 frame 直接拦截。
            if isMainFrame, let scheme = url.scheme?.lowercased(),
               scheme == "http" || scheme == "https" {
                AppLog.shared.info("面板请求跳转到外部地址，已改用系统浏览器打开")
                NSWorkspace.shared.open(url)
            } else {
                AppLog.shared.warn("已拦截非面板 origin 的子 frame 导航")
            }
            return .cancel
        }

        /// 判断 URL 是否属于面板自身的 origin（协议 + 127.0.0.1 + 实际面板端口）。
        private func isPanelOrigin(_ url: URL) -> Bool {
            guard let scheme = url.scheme?.lowercased(), scheme == "http" else { return false }
            guard let host = url.host, host == "127.0.0.1", url.user == nil, url.password == nil else { return false }
            return url.port == port
        }

        // MARK: 加载状态

        func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) {
            guard isActiveInstance, self.webView === webView else { return }
            currentNavigation = navigation
        }

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            guard isActiveInstance, self.webView === webView, navigation === currentNavigation else { return }
            onLoadStateChange(false)
        }

        func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
            guard isActiveInstance, self.webView === webView, navigation != nil, navigation === currentNavigation else { return }
            reportFailure(error)
        }

        func webView(_ webView: WKWebView,
                     didFailProvisionalNavigation navigation: WKNavigation!,
                     withError error: Error) {
            guard isActiveInstance, self.webView === webView, navigation != nil, navigation === currentNavigation else { return }
            reportFailure(error)
        }

        func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
            guard isActiveInstance, self.webView === webView else { return }
            retireDocument()
            onLoadStateChange(false)
            onFailure("面板渲染进程已结束。这通常是内存不足导致的，点击重试可重新加载。")
        }

        private func reportFailure(_ error: Error) {
            guard isActiveInstance else { return }
            let nsError = error as NSError
            guard nsError.domain != NSURLErrorDomain || nsError.code != NSURLErrorCancelled else { return }
            retireDocument()
            onLoadStateChange(false)
            // 面板进程退出时最常见的表现是连接被拒绝
            if nsError.domain == NSURLErrorDomain,
               nsError.code == NSURLErrorCannotConnectToHost
                || nsError.code == NSURLErrorNetworkConnectionLost {
                onFailure("无法连接到内置管理面板（127.0.0.1:\(port)）。面板进程可能已退出。")
            } else {
                onFailure("加载管理面板失败：\(error.localizedDescription)")
            }
        }

        // MARK: 原生桥入口

        private func isCurrentDocument(_ owner: NativeBridge.DocumentOwner) -> Bool {
            isActiveInstance && documentOwner === owner && owner.isActive
        }

        private func retireDocument() {
            let previous = documentOwner
            previous.invalidate()
            onDocumentRetired?(previous)
            if let confirmation { finishConfirmation(id: confirmation.0, accepted: false) }
            for (_, entry) in pendingRequests where entry.0 === previous { entry.1.cancel() }
            documentOwner = NativeBridge.DocumentOwner()
            currentNavigation = nil
            // Keep other application scripts; replace only this document identity injection.
            if let controller = webView?.configuration.userContentController {
                let marker = "// workbuddy-document-id\n"
                let retained = controller.userScripts.filter { !$0.source.hasPrefix(marker) }
                controller.removeAllUserScripts()
                retained.forEach(controller.addUserScript)
                controller.addUserScript(WKUserScript(source: marker +
                    "Object.defineProperty(window, '__workbuddyDocumentID', {value: '\(documentOwner.id.uuidString)', writable: false, configurable: false});",
                    injectionTime: .atDocumentStart, forMainFrameOnly: true))
            }
            guard let onConnectionLost else { return }
            let instance = instanceID
            Task { await onConnectionLost(instance, previous.id) }
        }

        /// 处理来自页面的桥消息。
        ///
        /// `WKScriptMessageHandler` 在 SDK 里标了 `WK_SWIFT_UI_ACTOR`（即 `@MainActor`），
        /// 因此这里直接以主 actor 方法实现，不需要跨 actor 转发 —— 也就不会出现
        /// 「非 Sendable 的 `message.body` 被带过 actor 边界」的问题。
        func userContentController(_ userContentController: WKUserContentController,
                                   didReceive message: WKScriptMessage) {
            let frame = message.frameInfo

            // 来源校验必须在原生侧做，且必须在解析命令之前做：
            // 只有主 frame 且 origin 精确等于（http + 127.0.0.1 + 实际面板端口）才受理。
            guard message.webView === webView, isActiveInstance,
                  NativeBridge.isTrustedOrigin(isMainFrame: frame.isMainFrame,
                                               scheme: frame.securityOrigin.protocol,
                                               host: frame.securityOrigin.host,
                                               port: frame.securityOrigin.port,
                                               panelPort: port) else {
                AppLog.shared.warn("已拒绝来源不明的原生桥消息（子 frame 或外部 origin）")
                return
            }

            let owner = documentOwner
            guard var envelope = message.body as? [String: Any],
                  envelope.removeValue(forKey: "document_id") as? String == owner.id.uuidString,
                  let request = NativeBridge.parse(envelope) else {
                AppLog.shared.warn("原生桥消息格式不合法，已忽略")
                return
            }

            let cancelable = (request.action == "account.maintenance" && request.payload["operation"] as? String == "begin") ||
                (request.action == "files.importAuth" && ["preview", "confirm"].contains(request.payload["operation"] as? String ?? "")) ||
                (["gateway.applyConfig", "api.rotateKey"].contains(request.action) && request.payload["operation"] == nil) ||
                (["gateway.stop", "gateway.restart"].contains(request.action) && request.payload.isEmpty) ||
                (request.action == "desktop.checkProxy" && request.payload["operation"] == nil)
            if cancelable, pendingRequests[request.id] != nil { return }
            if case let .cancelPending(target, importing) = NativeBridge.accountRequest(request),
               let entry = pendingRequests[target], entry.0 === owner, entry.2 == (importing ? "files.importAuth" : "account.maintenance") {
                // Revoke queued Tasks before they can enter ServiceManager or a modal loop.
                entry.1.cancel()
            }
            if case let .cancelPending(action, target) = NativeBridge.configurationRequest(request),
               let entry = pendingRequests[target], entry.0 === owner, entry.2 == action { entry.1.cancel() }
            if ["gateway.stop", "gateway.restart", "desktop.checkProxy"].contains(request.action),
               Set(request.payload.keys) == ["operation", "request_id"], request.payload["operation"] as? String == "cancel_pending",
               let target = request.payload["request_id"] as? String,
               let entry = pendingRequests[target], entry.0 === owner, entry.2 == request.action { entry.1.cancel() }
            let task = Task { @MainActor in
                defer { if cancelable, self.pendingRequests[request.id]?.0 === owner { self.pendingRequests.removeValue(forKey: request.id) } }
                guard isCurrentDocument(owner) else { return }
                if Task.isCancelled {
                    deliver(NativeBridge.failure(request.id, code: "cancelled", message: "操作已取消"), owner: owner)
                    return
                }
                let gatewayHandler: (@MainActor (NativeBridge.Request) async throws -> [String: Any])?
                if let perform = self.performGateway {
                    gatewayHandler = { operation in
                        guard self.isCurrentDocument(owner) else { throw NativeBridge.OperationFailure(code: "stale_document") }
                        return try await perform(operation, owner)
                    }
                } else { gatewayHandler = nil }
                let response = await NativeBridge.dispatch(request, statusProvider: statusProvider,
                    performAction: { action in
                        guard self.isCurrentDocument(owner) else { return "stale_document" }
                        return await self.performAction(action)
                    }, performMaintenance: { operation in
                        guard self.isCurrentDocument(owner), let perform = self.performMaintenance else {
                            throw NativeBridge.MaintenanceError.not_supported
                        }
                        let result = try await perform(operation, owner)
                        guard self.isCurrentDocument(owner) else { throw NativeBridge.MaintenanceError.stale_maintenance }
                        return result
                    }, performConfiguration: { operation in
                        guard self.isCurrentDocument(owner), let perform = self.performConfiguration else { throw NativeBridge.OperationFailure(code: "not_supported") }
                        return try await perform(operation, owner)
                    }, performUtility: { action, payload in
                        guard self.isCurrentDocument(owner), let perform = self.performUtility else { throw NativeBridge.OperationFailure(code: "not_supported") }
                        return try await perform(action, payload, owner)
                    }, performGateway: gatewayHandler, performCheck: { request in
                        guard self.isCurrentDocument(owner), let perform = self.performCheck else { throw NativeBridge.OperationFailure(code: "not_supported") }
                        let result = try await perform(request, owner)
                        guard self.isCurrentDocument(owner) else { throw NativeBridge.OperationFailure(code: "stale_document") }
                        return result
                    })
                guard isCurrentDocument(owner) else { return }
                if request.action == "desktop.reconnect", response["ok"] as? Bool == true,
                   let cookie = sessionCookieProvider(), let webView {
                    await webView.configuration.websiteDataStore.httpCookieStore.setCookie(cookie)
                    guard isCurrentDocument(owner) else { return }
                }
                deliver(response, owner: owner)
                if request.action == "desktop.reconnect", response["ok"] as? Bool == false,
                   (response["error"] as? [String: String])?["code"] == "operation_failed" {
                    onFailure("无法恢复管理连接，请重试")
                }
            }
            if cancelable { pendingRequests[request.id] = (owner, task, request.action) }
        }

        // WebKit's real confirm callback; only the current main document may open a sheet.
        func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String,
                     initiatedByFrame frame: WKFrameInfo) async -> Bool {
            let owner = documentOwner
            guard self.webView === webView, isCurrentDocument(owner), !webView.isLoading,
                  NativeBridge.isTrustedOrigin(isMainFrame: frame.isMainFrame, scheme: frame.securityOrigin.protocol,
                                               host: frame.securityOrigin.host, port: frame.securityOrigin.port, panelPort: port),
                  let url = frame.request.url, isPanelOrigin(url), confirmation == nil,
                  let window = webView.window, message.utf8.count <= 4096 else { return false }
            // The initiating JS is suspended by confirm; do not evaluate JS here.
            // Navigation synchronously retires this owner and cancels its sheet.
            let alert = NSAlert(); alert.messageText = "请确认当前页面操作"; alert.informativeText = message
            alert.addButton(withTitle: "取消"); alert.addButton(withTitle: "确定")
            let id = UUID()
            return await withCheckedContinuation { continuation in
                confirmation = (id, owner, alert, continuation, ContinuousClock.now.advanced(by: .seconds(25)))
                confirmationExpiry = Task { [weak self] in
                    do { try await Task.sleep(for: .seconds(25)) } catch { return }
                    self?.finishConfirmation(id: id, accepted: false)
                }
                alert.beginSheetModal(for: window) { [weak self] reply in
                    self?.finishConfirmation(id: id, accepted: reply == .alertSecondButtonReturn)
                }
            }
        }

        private func finishConfirmation(id: UUID, accepted: Bool) {
            guard let value = confirmation, value.0 == id else { return }
            confirmation = nil; confirmationExpiry?.cancel(); confirmationExpiry = nil
            value.2.window.sheetParent?.endSheet(value.2.window, returnCode: .cancel)
            value.3.resume(returning: accepted && ContinuousClock.now < value.4 && isCurrentDocument(value.1))
        }

        /// 把响应回写给页面。
        ///
        /// 前端需要在页面里预先挂上 `window.__workbuddyBridgeReceive`；
        /// 没有挂时用 `&&` 短路，不产生未捕获异常（原型的桥接层尚未实现，属 WB-03）。
        private func deliver(_ response: [String: Any], owner: NativeBridge.DocumentOwner) {
            guard isCurrentDocument(owner) else { return }
            let js = "if (window.__workbuddyDocumentID === '\(owner.id.uuidString)') { window.__workbuddyBridgeReceive && window.__workbuddyBridgeReceive("
                + NativeBridge.encodeForJavaScript(response) + "); }"
            webView?.evaluateJavaScript(js) { _, error in
                guard self.isCurrentDocument(owner) else { return }
                if let error {
                    AppLog.shared.warn("原生桥响应回写失败：\(error.localizedDescription)")
                }
            }
        }
    }
}

// MARK: - 原生故障页

/// 原生故障页。
///
/// 刻意用 SwiftUI 原生控件而不是 HTML：这个页面存在的意义就是「Web 内容起不来」，
/// 用 Web 实现会在最需要它的时候同样失效。因此「重试」和「退出」必须始终可用。
struct NativeFailureView: View {

    @Environment(\.colorScheme) private var colorScheme

    let title: String
    let statusColor: Color
    let detail: String
    let primaryActionTitle: String
    let onPrimary: () -> Void
    let onQuit: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Image(systemName: "exclamationmark.triangle.fill")
                .font(.system(size: 28))
                .foregroundStyle(statusColor)
                .accessibilityHidden(true)

            Text(title)
                .font(.custom("Songti SC Bold", size: 26))
                .foregroundStyle(DesktopPalette.text(colorScheme))
                .accessibilityAddTraits(.isHeader)
                .padding(.top, 20)

            Text(detail)
                .font(.system(size: 14))
                .foregroundStyle(DesktopPalette.secondaryText(colorScheme))
                .multilineTextAlignment(.leading)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.top, 12)

            HStack(spacing: 12) {
                Button(action: onPrimary) {
                    Text(primaryActionTitle)
                        .foregroundStyle(.white)
                }
                    .buttonStyle(.borderedProminent)
                    .tint(DesktopPalette.buttonFill(colorScheme))
                    .keyboardShortcut(.defaultAction)
                Button("打开日志目录", action: openLogDirectory)
                    .buttonStyle(.bordered)
                Button("退出应用", action: onQuit)
                    .buttonStyle(.borderless)
                    .keyboardShortcut("q")
            }
            .controlSize(.large)
            .tint(DesktopPalette.accent(colorScheme))
            .padding(.top, 28)
        }
        .padding(32)
        .frame(maxWidth: 560, alignment: .leading)
        .background(DesktopPalette.surface(colorScheme), in: RoundedRectangle(cornerRadius: 16))
        .overlay {
            RoundedRectangle(cornerRadius: 16)
                .strokeBorder(Color(nsColor: .separatorColor))
                .allowsHitTesting(false)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(DesktopPalette.workspace(colorScheme))
    }

    private func openLogDirectory() {
        NSWorkspace.shared.open(AppPaths.logDirectory)
    }
}
