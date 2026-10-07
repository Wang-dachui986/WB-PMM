import AppKit
import Foundation
import SwiftUI
import WebKit

/// Executed only by verify-m02-isolated.py with temporary fake helpers.
@MainActor final class BridgeTap: NSObject, WKScriptMessageHandler {
    var coordinator: PanelWebView.Coordinator!
    var messages: [WKScriptMessage] = []
    var hold = false
    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        messages.append(message)
        if !hold { coordinator.userContentController(controller, didReceive: message) }
    }
}

@main struct WebKitCheck {
    @MainActor static func main() {
        let app = NSApplication.shared
        app.setActivationPolicy(.prohibited)
        Task { @MainActor in
            do { try await run(); print("M02_WEBKIT_PASS"); exit(0) }
            catch { fputs("M02_WEBKIT_FAIL: \(error)\n", stderr); exit(1) }
        }
        app.run()
    }

    @MainActor static func run() async throws {
        enum Failure: Error { case check(String) }
        func check(_ condition: Bool, _ label: String) throws {
            guard condition else { throw Failure.check(label) }
            print("PASS WK \(label)")
        }
        func wait(_ label: String, _ condition: () -> Bool) async throws {
            for _ in 0..<300 {
                if condition() { return }
                try await Task.sleep(for: .milliseconds(20))
            }
            throw Failure.check("timeout: " + label)
        }
        let defaults = UserDefaults(suiteName: "workbuddy-m02-webkit-test")!
        defaults.setVolatileDomain(["apiPort": Int(ProcessInfo.processInfo.environment["WB_TEST_PORT"]!)!], forName: UserDefaults.argumentDomain)
        let services = ServiceManager(settings: AppSettings(defaults: defaults))
        try check(services.prepareForLaunch(), "isolated launch")
        try check(!(await services.startPanelService()), "first native login fails after real ready handshake")
        try check(services.panelPort != nil && services.panelSessionCookie == nil && !services.canLoadPanel,
                  "failed first login selects native retry instead of empty web view")
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 800, height: 600),
                              styleMask: [.titled], backing: .buffered, defer: false)
        let native = NSHostingView(rootView: PanelContainerView(services: services))
        window.contentView = native
        native.layoutSubtreeIfNeeded()
        func hasWebView(_ view: NSView) -> Bool { view is WKWebView || view.subviews.contains(where: hasWebView) }
        try check(!hasWebView(native), "first failure mounts native container")
        try check(await services.reconnectPanel(), "native retry establishes session")
        try check(services.canLoadPanel, "recovered session permits web content")
        // Remove SwiftUI before its update creates another WKWebView; exercise the same coordinator below.
        window.contentView = NSView()
        let port = services.panelPort!
        let instance = services.instanceID
        var loaded = false
        var failures = 0
        var actions = 0
        var delayResponse = false
        var releaseResponse: CheckedContinuation<Void, Never>?
        func makeView() -> (WKWebView, PanelWebView.Coordinator, BridgeTap) {
            let tap = BridgeTap()
            let coordinator = PanelWebView.Coordinator(port: port, instanceID: instance,
                isCurrentInstance: { services.isCurrentPanel(instanceID: $0, port: $1) },
                performAction: { action in
                    actions += 1
                    if delayResponse {
                        await withCheckedContinuation { releaseResponse = $0 }
                        return nil
                    }
                    return await services.performBridgeAction(action)
                }, sessionCookieProvider: { services.panelSessionCookie },
                statusProvider: { services.statusSnapshot() },
                onLoadStateChange: { loaded = !$0 }, onFailure: { _ in failures += 1 })
            tap.coordinator = coordinator
            let config = WKWebViewConfiguration()
            config.websiteDataStore = .nonPersistent()
            config.userContentController.add(tap, name: NativeBridge.messageName)
            let web = WKWebView(frame: NSRect(x: 0, y: 0, width: 800, height: 600), configuration: config)
            coordinator.webView = web; web.navigationDelegate = coordinator; web.uiDelegate = coordinator
            return (web, coordinator, tap)
        }
        let (web, coordinator, tap) = makeView()
        window.contentView = web
        let reload = UUID()
        coordinator.load(port: port, sessionCookie: services.panelSessionCookie, reloadToken: reload)
        try await wait("page loaded") { loaded }
        func js(_ script: String) async throws -> Any? { try await web.evaluateJavaScript(script) }
        func awaitJS(_ label: String, _ script: String) async throws {
            for _ in 0..<300 {
                if (try await js(script)) as? Bool == true { return }
                try await Task.sleep(for: .milliseconds(20))
            }
            throw Failure.check("timeout JS: " + label)
        }
        _ = try await js("window.cookieCheck=null; fetch('/api/session').then(r=>r.json()).then(x=>window.cookieCheck=x.authenticated); null")
        try await awaitJS("HttpOnly cookie", "window.cookieCheck === true")
        try check((try await js("document.cookie.includes('wbgui_session')")) as? Bool == false, "actual HttpOnly cookie is usable but invisible to JavaScript")
        _ = try await js("window.good=null; m02.desktopCommand('gateway.start').then(x=>window.good=true); null")
        try await awaitJS("valid bridge", "window.good === true")
        try check(services.gatewayState == .runningUnavailable, "real WK bridge invokes native gateway lifecycle")
        // Request/stream use the actual bundled api.ts; server forces 401 without replay.
        let oldCookie = services.panelSessionCookie!.value
        let before401 = actions
        _ = try await js("window.writeResult=null; window.streamClosed=false; window.loggedOut=0; m02.setUnauthorizedHandler(()=>window.loggedOut++); m02.api.saveConfig({test:true}).catch(e=>window.writeResult=e.status); m02.streamChat({model:'mock',messages:[]},()=>{},()=>{},()=>window.streamClosed=true); null")
        try await awaitJS("401 reconnect", "window.writeResult === 401 && window.streamClosed === true")
        try check(actions == before401 + 1 && services.panelSessionCookie?.value != oldCookie, "simultaneous 401 performs one native reconnect")
        _ = try await js("window.after401=null; fetch('/api/session').then(r=>r.json()).then(x=>window.after401=x.authenticated); null")
        try await awaitJS("new cookie installed", "window.after401 === true")
        _ = try await js("window.counts=null; fetch('/counts').then(r=>r.json()).then(x=>window.counts=x); null")
        try await awaitJS("no replay", "window.counts && window.counts.writes===1 && window.counts.streams===1 && window.loggedOut===0")
        try check(true, "real HTTP write and stream each sent once; renewed Cookie works")
        // A real same-origin iframe message must reach the handler and be rejected there.
        let beforeFrame = actions
        _ = try await js("let frame=document.createElement('iframe'); frame.src='/frame'; document.body.appendChild(frame); null")
        try await wait("iframe bridge message") { tap.messages.contains { !$0.frameInfo.isMainFrame } }
        try await Task.sleep(for: .milliseconds(100))
        try check(actions == beforeFrame, "actual iframe bridge call refused")
        // Allow a local test-only navigation to localhost to exercise the origin guard itself.
        web.navigationDelegate = nil
        web.load(URLRequest(url: URL(string: "http://localhost:\(port)/foreign")!))
        try await wait("foreign origin bridge message") { tap.messages.contains { $0.frameInfo.securityOrigin.host == "localhost" } }
        try check(actions == beforeFrame, "actual foreign-origin main-frame bridge refused")
        web.navigationDelegate = coordinator
        loaded = false
        coordinator.load(port: port, sessionCookie: services.panelSessionCookie, reloadToken: UUID())
        try await wait("return to panel") { loaded }
        // Capture a real message, then invalidate before the scheduled native task can run.
        tap.hold = true
        let messageCount = tap.messages.count
        _ = try await js("window.webkit.messageHandlers.workbuddyNative.postMessage({id:'queued-old',action:'gateway.stop',payload:{},document_id:window.__workbuddyDocumentID}); null")
        try await wait("queued bridge") { tap.messages.count > messageCount }
        let queued = tap.messages.last!
        coordinator.userContentController(web.configuration.userContentController, didReceive: queued)
        PanelWebView.dismantleNSView(web, coordinator: coordinator)
        let beforeDismantle = actions
        try await Task.sleep(for: .milliseconds(100))
        coordinator.userContentController(web.configuration.userContentController, didReceive: queued)
        coordinator.webView(web, didFail: nil, withError: NSError(domain: NSURLErrorDomain, code: -1004))
        coordinator.webViewWebContentProcessDidTerminate(web)
        try check(actions == beforeDismantle && coordinator.webView == nil && failures == 0,
                  "dismantled same-instance coordinator rejects queued messages and callbacks")
        // A response already waiting on native work must not reach a detached page.
        let (waitingWeb, waitingCoordinator, waitingTap) = makeView()
        window.contentView = waitingWeb; loaded = false
        waitingCoordinator.load(port: port, sessionCookie: services.panelSessionCookie, reloadToken: UUID())
        try await wait("replacement loaded") { loaded }
        delayResponse = true
        _ = try await waitingWeb.evaluateJavaScript("window.responses=0; window.__workbuddyBridgeReceive=()=>window.responses++; window.webkit.messageHandlers.workbuddyNative.postMessage({id:'late',action:'gateway.stop',payload:{},document_id:window.__workbuddyDocumentID}); null")
        try await wait("in-flight native response") { releaseResponse != nil }
        PanelWebView.dismantleNSView(waitingWeb, coordinator: waitingCoordinator)
        releaseResponse?.resume(); releaseResponse = nil
        try await Task.sleep(for: .milliseconds(150))
        try check((try await waitingWeb.evaluateJavaScript("window.responses")) as? Int == 0,
                  "detached page receives no late response")
        _ = waitingTap
        let (cookieWeb, cookieCoordinator, cookieTap) = makeView()
        cookieCoordinator.load(port: port, sessionCookie: services.panelSessionCookie, reloadToken: UUID())
        PanelWebView.dismantleNSView(cookieWeb, coordinator: cookieCoordinator)
        try await Task.sleep(for: .milliseconds(150))
        try check(cookieWeb.url == nil, "detached Cookie callback cannot navigate")
        _ = cookieTap
        // Both fake helpers intentionally ignore EOF and TERM; this measures shared grace + kill.
        let began = Date()
        await services.shutdown()
        let elapsed = Date().timeIntervalSince(began)
        try check(services.didFinishShutdown && elapsed < 12.5, "two resistant helpers share 10-second grace budget (\(String(format: "%.2f", elapsed))s)")
    }
}
