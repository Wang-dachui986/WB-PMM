import AppKit
import Foundation
import WebKit

/// Real WebKit messages/navigation, driven only by verify-m04-native.py and its fake helpers.
@MainActor final class M04Tap: NSObject, WKScriptMessageHandler, WKNavigationDelegate {
    var coordinator: PanelWebView.Coordinator!
    var messages: [WKScriptMessage] = []
    var hold = false
    var beforePolicy: (() -> Void)?
    var finished = 0
    var lastNavigation: WKNavigation?
    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        messages.append(message)
        if !hold { coordinator.userContentController(controller, didReceive: message) }
    }
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction) async -> WKNavigationActionPolicy {
        let callback = beforePolicy; beforePolicy = nil
        callback?()
        return await coordinator.webView(webView, decidePolicyFor: action)
    }
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        finished += 1
        coordinator.webView(webView, didFinish: navigation)
    }
    func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) {
        lastNavigation = navigation
        coordinator.webView(webView, didStartProvisionalNavigation: navigation)
    }
    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        coordinator.webView(webView, didFail: navigation, withError: error)
    }
    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        coordinator.webView(webView, didFailProvisionalNavigation: navigation, withError: error)
    }
}

@main struct M04WebKitCheck {
    @MainActor static func main() {
        let app = NSApplication.shared
        app.setActivationPolicy(ProcessInfo.processInfo.environment["WB_CENTRAL_ONLY"] == "1" ? .accessory : .prohibited)
        Task { @MainActor in
            do { if ProcessInfo.processInfo.environment["WB_CENTRAL_ONLY"] == "1" { try await central() } else { try await run() }; print("M04_WEBKIT_PASS"); exit(0) }
            catch { FileHandle.standardError.write(Data("M04_WEBKIT_FAIL: \(error)\n".utf8)); exit(1) }
        }
        app.run()
    }

    @MainActor static func run() async throws {
        enum Failure: Error { case check(String) }
        func check(_ ok: Bool, _ label: String) throws {
            guard ok else { throw Failure.check(label) }
            FileHandle.standardOutput.write(Data("PASS M04 WK \(label)\n".utf8))
        }
        func wait(_ label: String, _ predicate: () -> Bool) async throws {
            for _ in 0..<300 {
                if predicate() { return }
                try await Task.sleep(for: .milliseconds(20))
            }
            throw Failure.check("timeout: " + label)
        }
        func fixture(_ value: [String: Any] = [:]) throws {
            try JSONSerialization.data(withJSONObject: value).write(to: AppPaths.dataDirectory.appendingPathComponent("fixture.json"), options: .atomic)
        }
        func count(_ name: String) -> Int {
            let raw = (try? String(contentsOf: AppPaths.dataDirectory.appendingPathComponent("events.jsonl"), encoding: .utf8)) ?? ""
            return raw.split(separator: "\n").filter {
                (try? JSONSerialization.jsonObject(with: Data($0.utf8)) as? [String: Any])?["event"] as? String == name
            }.count
        }
        let defaults = UserDefaults(suiteName: "workbuddy-m04-webkit")!
        defaults.setVolatileDomain(["apiPort": Int(ProcessInfo.processInfo.environment["WB_TEST_PORT"]!)!], forName: UserDefaults.argumentDomain)
        var holdConfirmation = false
        var confirmation: CheckedContinuation<Void, Never>?
        let services = ServiceManager(settings: AppSettings(defaults: defaults), maintenanceLifetime: 30,
            maintenanceConfirmation: { _ in
                if holdConfirmation { await withCheckedContinuation { confirmation = $0 } }
                return .abort
            })
        try check(services.prepareForLaunch(), "isolated data")
        try fixture()
        try check(await services.startPanelService(), "owned fake panel ready")
        let port = services.panelPort!
        let tap = M04Tap()
        var calls = 0
        var owners: [NativeBridge.DocumentOwner] = []
        var lost: [UUID] = []
        var holdResponse = false
        var response: CheckedContinuation<Void, Never>?
        let coordinator = PanelWebView.Coordinator(port: port, instanceID: services.instanceID,
            isCurrentInstance: { services.isCurrentPanel(instanceID: $0, port: $1) },
            performAction: { await services.performBridgeAction($0) },
            sessionCookieProvider: { services.panelSessionCookie }, statusProvider: { services.statusSnapshot() },
            onLoadStateChange: { _ in }, onFailure: { _ in },
            performMaintenance: { request, owner in
                calls += 1; owners.append(owner)
                let value = try await services.performMaintenance(request, owner: owner)
                if holdResponse { await withCheckedContinuation { response = $0 } }
                return value
            }, onConnectionLost: { instance, owner in
                lost.append(owner)
                await services.panelConnectionLost(instanceID: instance, ownerID: owner)
            })
        tap.coordinator = coordinator
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        config.userContentController.add(tap, name: NativeBridge.messageName)
        let web = WKWebView(frame: NSRect(x: 0, y: 0, width: 800, height: 600), configuration: config)
        coordinator.webView = web; web.navigationDelegate = tap; web.uiDelegate = coordinator
        let window = NSWindow(contentRect: web.frame, styleMask: [.titled], backing: .buffered, defer: false)
        window.contentView = web
        coordinator.load(port: port, sessionCookie: services.panelSessionCookie, reloadToken: UUID())
        try await wait("initial document") { tap.finished == 1 }
        func js(_ code: String) async throws -> Any? { try await web.evaluateJavaScript(code) }
        func awaitJS(_ label: String, _ code: String) async throws {
            for _ in 0..<300 {
                if (try await js(code)) as? Bool == true { return }
                try await Task.sleep(for: .milliseconds(20))
            }
            throw Failure.check("timeout JS: " + label)
        }
        func navigate() async throws {
            let before = tap.finished
            web.load(URLRequest(url: URL(string: "http://127.0.0.1:\(port)/next?document=\(before)")!))
            try await wait("replacement document") { tap.finished > before }
        }
        let begin = "{operation:'begin',action:'refresh',uids:['fake001']}"
        try check((try await js("!!window.__workbuddyDocumentID && !Object.getOwnPropertyDescriptor(window,'__workbuddyDocumentID').writable && !Object.getOwnPropertyDescriptor(window,'__workbuddyDocumentID').configurable")) as? Bool == true, "document-start ID is read-only")
        _ = try await js("window.statusOK=false; m04.desktopCommand('desktop.getStatus').then(()=>window.statusOK=true); null")
        try await awaitJS("production bridge document metadata", "window.statusOK")
        _ = try await js("window.__workbuddyBridgeReceive=r=>window.received.push(r); window.webkit.messageHandlers.workbuddyNative.postMessage({id:'missing',action:'account.maintenance',payload:\(begin)}); window.webkit.messageHandlers.workbuddyNative.postMessage({id:'wrong',action:'account.maintenance',payload:\(begin),document_id:'old'}); null")
        try await Task.sleep(for: .milliseconds(100))
        try check(calls == 0, "missing and wrong document IDs cannot begin maintenance")

        tap.hold = true
        let messages = tap.messages.count
        _ = try await js("send('queued',\(begin)); null")
        try await wait("held old message") { tap.messages.count > messages }
        let queued = tap.messages.last!
        // Enqueue the real old message in the same callback that receives a real main navigation.
        tap.beforePolicy = { coordinator.userContentController(config.userContentController, didReceive: queued) }
        let oldID = try await js("window.__workbuddyDocumentID") as! String
        let oldNavigation = tap.lastNavigation
        try await navigate()
        tap.hold = false
        try await Task.sleep(for: .milliseconds(100))
        try check(calls == 0 && count("permit:begin") == 0, "queued old begin is invalidated before its native Task runs")
        try check((try await js("window.__workbuddyDocumentID")) as? String != oldID, "same-origin navigation rotates document owner")
        coordinator.webView(web, didFail: oldNavigation, withError: NSError(domain: NSURLErrorDomain, code: NSURLErrorNetworkConnectionLost))
        coordinator.userContentController(config.userContentController, didReceive: queued)
        try await Task.sleep(for: .milliseconds(100))
        try check(calls == 0, "late old WebKit message cannot borrow new document identity")

        try fixture(["begin_delay": 0.35])
        _ = try await js("send('inflight',\(begin)); null")
        try await wait("grant in flight") { count("permit:begin") == 1 }
        let oldOwner = owners.last!
        try await navigate()
        try await wait("old lease drained") { !services.isBusy }
        try check(!oldOwner.isActive && lost.contains(oldOwner.id) && count("permit:end") == 1, "navigation revokes and drains in-flight old lease")
        try check((try await js("received.length")) as? Int == 0, "in-flight old response never reaches replacement document")

        try fixture()
        holdResponse = true
        _ = try await js("send('held-response',\(begin)); null")
        try await wait("active response held") { response != nil }
        let responseOwner = owners.last!
        try await navigate()
        try await wait("active old lease cleanup") { !services.isBusy }
        holdResponse = false
        _ = try await js("send('new-flow',\(begin)); null")
        try await awaitJS("new document begin", "received.some(r=>r.id==='new-flow' && r.ok)")
        let newOwner = owners.last!
        response?.resume(); response = nil
        try await Task.sleep(for: .milliseconds(100))
        try check((try await js("received.every(r=>r.id!=='held-response')")) as? Bool == true, "awaited old response is fenced after a new flow starts")
        let generation = UInt64(services.statusSnapshot()["maintenance_generation"] as! String)!
        do {
            _ = try await services.performMaintenance(.end(instanceID: services.instanceID, generation: generation), owner: NativeBridge.DocumentOwner())
            throw Failure.check("foreign owner ended current flow")
        } catch NativeBridge.MaintenanceError.stale_maintenance { }
        await services.panelConnectionLost(instanceID: services.instanceID, ownerID: responseOwner.id)
        try check(services.isBusy && newOwner !== responseOwner, "foreign end and delayed old-owner cleanup cannot cancel new flow")
        _ = try await services.performMaintenance(.end(instanceID: services.instanceID, generation: generation), owner: newOwner)

        holdConfirmation = true
        let beforeConfirmation = count("permit:begin")
        _ = try await js("send('confirmation',\(begin)); null")
        try await wait("native confirmation suspended") { confirmation != nil }
        let confirmingOwner = owners.last!
        try await navigate()
        confirmation?.resume(); confirmation = nil; holdConfirmation = false
        try await wait("confirmation cleanup") { !services.isBusy }
        try check(!confirmingOwner.isActive && count("permit:begin") == beforeConfirmation, "navigation during native confirmation prevents permit grant")
        try check((try await js("received.length")) as? Int == 0, "confirmation result does not enter the new document")
        // Deliver queued begin + cancel in one actor turn, as real WebKit IPC can do.
        tap.hold = true
        let queuedStart = tap.messages.count
        let beforeQueuedCancel = count("permit:begin")
        _ = try await js("send('cancel-queued',\(begin)); send('cancel-queued-stop',{operation:'cancel_pending',request_id:'cancel-queued'}); null")
        try await wait("queued begin and cancel messages") { tap.messages.count >= queuedStart + 2 }
        for message in tap.messages.suffix(2) { coordinator.userContentController(config.userContentController, didReceive: message) }
        tap.hold = false
        try await awaitJS("cancelled queued response", "received.some(r=>r.id==='cancel-queued' && !r.ok)")
        try check(count("permit:begin") == beforeQueuedCancel && !services.isBusy, "precise pending cancel fences a queued native begin")

        holdConfirmation = true
        _ = try await js("window.pendingOutcome='waiting'; window.pendingFlow=m04.beginMaintenance('refresh',['fake001']); pendingFlow.ready.then(()=>window.pendingOutcome='active',()=>window.pendingOutcome='cancelled'); null")
        try await wait("pending production confirmation") { confirmation != nil }
        let beginID = (tap.messages.last!.body as! [String: Any])["id"] as! String
        // An invalid envelope cannot cancel a native Task before payload validation.
        _ = try await js("window.webkit.messageHandlers.workbuddyNative.postMessage({id:'bad-cancel',action:'account.maintenance',payload:{operation:'cancel_pending',request_id:'\(beginID)',extra:true},document_id:window.__workbuddyDocumentID}); null")
        try await Task.sleep(for: .milliseconds(60))
        try check(services.isBusy, "malformed pending cancel has no native side effect")
        _ = try await js("window.pendingCancelled=false; pendingFlow.cancel().then(()=>window.pendingCancelled=true); null")
        try await Task.sleep(for: .milliseconds(60))
        try check((try await js("window.pendingCancelled")) as? Bool == false, "production bridge waits for native confirmation cleanup")
        confirmation?.resume(); confirmation = nil; holdConfirmation = false
        try await awaitJS("production cancellation acknowledgement", "window.pendingCancelled && window.pendingOutcome==='cancelled'")
        try check(count("permit:begin") == beforeQueuedCancel && !services.isBusy, "confirmation cancel cannot leave a late permit")
        PanelWebView.dismantleNSView(web, coordinator: coordinator)
        await services.shutdown()
        try check(services.didFinishShutdown, "real WebKit probe shuts down owned helpers")
        _ = window
    }
}

private final class CentralMemoryDefaults: UserDefaults {
    private var values: [String: Any] = [:]
    init(port: Int) { super.init(suiteName: "central-memory-only")!; values["apiPort"] = port }
    override func set(_ value: Any?, forKey key: String) { values[key] = value }
    override func integer(forKey key: String) -> Int { values[key] as? Int ?? 0 }
    override func bool(forKey key: String) -> Bool { values[key] as? Bool ?? false }
    override func string(forKey key: String) -> String? { values[key] as? String }
    override func dictionary(forKey key: String) -> [String: Any]? { values[key] as? [String: Any] }
}

extension M04WebKitCheck {
    @MainActor static func central() async throws {
        enum Failure: Error { case check(String) }
        func check(_ value: Bool, _ label: String) throws {
            guard value else { throw Failure.check(label) }
            print("PASS CENTRAL WK " + label)
        }
        func wait(_ label: String, _ predicate: () -> Bool) async throws {
            for _ in 0..<500 { if predicate() { return }; try await Task.sleep(for: .milliseconds(20)) }
            throw Failure.check("timeout " + label)
        }
        func fixture(_ value: [String: Any] = [:]) throws {
            try JSONSerialization.data(withJSONObject: value).write(to: AppPaths.dataDirectory.appendingPathComponent("fixture.json"), options: .atomic)
        }
        func events(_ name: String) -> Int {
            let lines = (try? String(contentsOf: AppPaths.dataDirectory.appendingPathComponent("events.jsonl"), encoding: .utf8)) ?? ""
            return lines.split(separator: "\n").filter { (try? JSONSerialization.jsonObject(with: Data($0.utf8)) as? [String: Any])?["event"] as? String == name }.count
        }
        let services = ServiceManager(settings: AppSettings(defaults: CentralMemoryDefaults(port: Int(ProcessInfo.processInfo.environment["WB_TEST_PORT"]!)!)), maintenanceConfirmation: { _ in .wait })
        var opened: [URL] = []
        services.utilityDirectoryOpener = { opened.append($0); return true }
        try check(services.prepareForLaunch(), "isolated in-memory preferences and temp data")
        try fixture()
        try check(await services.startPanelService(), "owned synthetic control panel ready")
        let targetPorts = ProcessInfo.processInfo.environment["WB_TEST_TARGET_PORTS"]!.split(separator: ",").map { Int($0)! }
        let port = services.panelPort!, tap = M04Tap()
        var checkOwners: [NativeBridge.DocumentOwner] = []
        let coordinator = PanelWebView.Coordinator(port: port, instanceID: services.instanceID,
            isCurrentInstance: { services.isCurrentPanel(instanceID: $0, port: $1) },
            performAction: { await services.performBridgeAction($0) }, sessionCookieProvider: { services.panelSessionCookie },
            statusProvider: { services.statusSnapshot() }, onLoadStateChange: { _ in }, onFailure: { _ in },
            onConnectionLost: { await services.panelConnectionLost(instanceID: $0, ownerID: $1) },
            performConfiguration: { try await services.performConfiguration($0, owner: $1) },
            performUtility: { try await services.performUtility(action: $0, payload: $1, owner: $2) },
            performGateway: { try await services.performGateway($0, owner: $1) },
            onDocumentRetired: { services.retireDocument($0) },
            performCheck: { request, owner in checkOwners.append(owner); return try await services.performCheck(request, owner: owner) })
        tap.coordinator = coordinator
        let config = WKWebViewConfiguration(); config.websiteDataStore = .nonPersistent()
        config.userContentController.add(tap, name: NativeBridge.messageName)
        let web = WKWebView(frame: NSRect(x: 0, y: 0, width: 900, height: 700), configuration: config)
        coordinator.webView = web; web.navigationDelegate = tap; web.uiDelegate = coordinator
        let window = NSWindow(contentRect: web.frame, styleMask: [.titled], backing: .buffered, defer: false)
        window.contentView = web; window.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true)
        func js(_ source: String) async throws -> Any? { try await web.evaluateJavaScript(source) }
        func until(_ label: String, _ source: String) async throws {
            for _ in 0..<500 { if (try await js(source)) as? Bool == true { return }; try await Task.sleep(for: .milliseconds(20)) }
            throw Failure.check("JS timeout " + label)
        }
        func command(_ action: String, _ payload: String = "{}") async throws -> [String: Any] {
            _ = try await js("window.commandResult=null;m04.desktopCommand('\(action)',\(payload)).then(v=>window.commandResult={ok:true,data:v},e=>window.commandResult={ok:false,code:e.code,message:e.message});null")
            try await until(action, "window.commandResult!==null")
            return (try await js("window.commandResult")) as! [String: Any]
        }
        func navigate(_ path: String = "/config") async throws {
            let before = tap.finished
            web.load(URLRequest(url: URL(string: "http://127.0.0.1:\(port)\(path)?n=\(before)")!))
            try await wait("document navigation") { tap.finished > before }
            try await until("React mount", "!!document.querySelector('h1')")
        }
        func button(in view: NSView, title: String) -> NSButton? {
            if let b = view as? NSButton, b.title == title { return b }
            for child in view.subviews { if let b = button(in: child, title: title) { return b } }
            return nil
        }
        func answer(_ title: String) async throws {
            try await wait("native sheet \(title)") { window.attachedSheet != nil }
            guard let content = window.attachedSheet?.contentView, let control = button(in: content, title: title) else { throw Failure.check("native button " + title) }
            control.performClick(nil)
            try await wait("sheet ended") { window.attachedSheet == nil }
        }
        do {
            coordinator.load(port: port, sessionCookie: services.panelSessionCookie, reloadToken: UUID())
            try await wait("initial page") { tap.finished == 1 }
            try await until("config input", "!!document.getElementById('config-port')")
            if ProcessInfo.processInfo.environment["WB_FINISH_WEBKIT"] == "1" {
                let ready = try await command("desktop.checkReadiness")
                try check((ready["data"] as? [String: Any])?["ready"] as? Bool == true, "readiness fixed command reaches actual Coordinator and private filesystem")
                let invalid = try await command("desktop.checkReadiness", "{path:'/arbitrary'}")
                try check(invalid["ok"] as? Bool == false && invalid["code"] as? String == "invalid_payload", "readiness arbitrary payload rejected on real bridge")
                let revision = try GatewayConfigFile.readSnapshot().revision
                let direct = try await command("desktop.checkProxy", "{revision:'\(revision)'}")
                try check((direct["data"] as? [String: Any])?["result"] as? String == "not_applicable", "direct proxy reports not applicable through real bridge")
                _ = try await js("window.lifecycle=[];m04.subscribeDesktopLifecycle(e=>window.lifecycle.push(e));null")
                await services.handleLifecycle(sleep: true); await services.handleLifecycle(sleep: false)
                try await until("lifecycle", "window.lifecycle.length===2")
                try check((try await js("window.lifecycle.map(e=>e.kind).join(',')==='sleep,wake' && BigInt(window.lifecycle[1].epoch)>BigInt(window.lifecycle[0].epoch)")) as? Bool == true, "native lifecycle dispatch captures owner and monotonic epoch")
                let oldID = try await js("window.__workbuddyDocumentID") as! String
                try await navigate()
                _ = try await js("window.lifecycle=[];m04.subscribeDesktopLifecycle(e=>window.lifecycle.push(e));window.dispatchEvent(new CustomEvent('workbuddy:lifecycle',{detail:{document_id:'\(oldID)',epoch:'999',kind:'sleep'}}));null")
                try check((try await js("window.lifecycle.length===0")) as? Bool == true, "old document lifecycle event cannot reach new document subscription")
                await services.handleLifecycle(sleep: false)
                try await until("new lifecycle", "window.lifecycle.length===1")
                try check(services.gatewayState == .stopped, "wake event never auto-starts a stopped owned gateway")
                _ = try await js("window.gatewayStatusEvents=0;window.stopGatewayStatus=m04.subscribeDesktopGatewayStatus(()=>window.gatewayStatusEvents++);null")
                let documentID = try await js("window.__workbuddyDocumentID") as! String
                let navigations = tap.finished
                try check(await services.startGateway(), "synthetic gateway starts through native manager")
                try await until("start status event", "window.gatewayStatusEvents>0")
                let afterStartDocumentID = try await js("window.__workbuddyDocumentID") as? String
                try check(services.gatewayState == .runningUnavailable && tap.finished == navigations &&
                          afterStartDocumentID == documentID,
                          "start pushes status to current document without navigation")
                services.operationConfirmation = { _, _ in .abort }
                let beforeRestart = try await js("window.gatewayStatusEvents") as! Int
                try check(await services.restartGateway(), "synthetic gateway restarts through native manager")
                try await until("restart status event", "window.gatewayStatusEvents>\(beforeRestart)")
                let beforeStop = try await js("window.gatewayStatusEvents") as! Int
                try check(await services.stopGateway(), "synthetic gateway stops through native manager")
                try await until("stop status event", "window.gatewayStatusEvents>\(beforeStop)")
                services.operationConfirmation = nil
                try check(services.gatewayState == .stopped && tap.finished == navigations,
                          "restart and stop notify the same document without reload")
                let beforeInvalid = try await js("window.gatewayStatusEvents") as! Int
                _ = try await js("window.dispatchEvent(new CustomEvent('workbuddy:gateway-status',{detail:{document_id:'\(oldID)',epoch:'999999'}}));null")
                _ = try await js("window.dispatchEvent(new CustomEvent('workbuddy:gateway-status',{detail:{document_id:'\(documentID)',epoch:'1'}}));null")
                try check((try await js("window.gatewayStatusEvents===\(beforeInvalid)")) as? Bool == true,
                          "old document and stale epoch cannot inject status events")
                let patch = try await command("gateway.applyConfig", "{revision:'\(revision)',patch:{desktop:{proxy_url:'http://127.0.0.1:\(targetPorts[0])'}}}")
                try check(patch["ok"] as? Bool == true, "proxy target only comes from committed configuration transaction")
                let proxyRevision = try GatewayConfigFile.readSnapshot().revision
                _ = try await js("window.proxyOutcome='pending';window.checkAbort=new AbortController();m04.desktopCommand('desktop.checkProxy',{revision:'\(proxyRevision)'},{signal:checkAbort.signal}).then(()=>window.proxyOutcome='success',()=>window.proxyOutcome='cancelled');checkAbort.abort();null")
                try await until("queued proxy cancel", "window.proxyOutcome==='cancelled'")
                try check(true, "queued bridge proxy cancellation keeps envelope identity and awaits ACK")
                _ = try await js("window.proxyOutcome='pending';window.checkAbort=new AbortController();m04.desktopCommand('desktop.checkProxy',{revision:'\(proxyRevision)'},{signal:checkAbort.signal}).then(()=>window.proxyOutcome='success',()=>window.proxyOutcome='cancelled');null")
                try await Task.sleep(for: .milliseconds(60))
                let owner = checkOwners.last!
                try await navigate()
                try check(!owner.isActive, "real main document navigation retires the pending proxy owner")
                try await Task.sleep(for: .milliseconds(60))
                _ = try await js("window.proxyOutcome='pending';window.checkAbort=new AbortController();m04.desktopCommand('desktop.checkProxy',{revision:'\(proxyRevision)'},{signal:checkAbort.signal}).then(()=>window.proxyOutcome='success',e=>window.proxyOutcome=e.code);null")
                try await Task.sleep(for: .milliseconds(60))
                try check((try await js("window.proxyOutcome==='pending'")) as? Bool == true, "replacement document can start a fresh check after old native cleanup")
                _ = try await js("checkAbort.abort();null")
                try await until("fresh proxy cancel", "window.proxyOutcome!=='pending'")
                PanelWebView.dismantleNSView(web, coordinator: coordinator)
                await services.shutdown(); window.orderOut(nil)
                try check(services.didFinishShutdown, "finish bridge check closes all own helpers")
                return
            }
            let tail = try await command("logs.tail", "{source:'app',limit:5}")
            try check(tail["ok"] as? Bool == true && (tail["data"] as? [String: Any])?["run_id"] is String, "logs.tail runs through real Coordinator and lifetime Utilities")
            let prefs = try await command("preferences.get")
            try check(prefs["ok"] as? Bool == true, "preferences.get real fixed dispatch")
            let update = try await command("preferences.update", "{theme:'dark',note:{uid:'synthetic',value:'local note'}}")
            try check((update["data"] as? [String: Any])?["theme"] as? String == "dark", "preferences.update real dispatch uses injected memory storage")
            let directory = try await command("desktop.openDirectory", "{kind:'logs'}")
            try check(directory["ok"] as? Bool == true && opened == [AppPaths.logDirectory], "directory command reaches exact native path with isolated opener")
            let preview = try await command("diagnostics.export", "{operation:'preview'}")
            let previewID = (preview["data"] as! [String: Any])["preview_id"] as! String
            try check(preview["ok"] as? Bool == true, "diagnostics preview real command returns safe metadata")
            _ = try await js("window.clearResult=null;m04.desktopCommand('logs.clearArchives').then(v=>window.clearResult=v);null")
            try await answer("取消")
            try await until("archive cancellation", "window.clearResult?.cancelled===true")
            try check(true, "sixth Utilities command opens real native confirmation and cancels without writes")
            try await navigate()
            let stale = try await command("diagnostics.export", "{operation:'save',preview_id:'\(previewID)'}")
            try check(stale["ok"] as? Bool == false && stale["code"] as? String == "stale_preview", "navigation synchronously invalidates previous owner diagnostic preview")
            _ = try await js("m04.desktopCommand('logs.clearArchives').catch(()=>{});null")
            try await wait("archive prompt before navigation") { window.attachedSheet != nil }
            try await navigate()
            try check(window.attachedSheet == nil, "owner retirement closes pending Utilities native sheet")

            // This is the actual ConfigPage and native WKUIDelegate, with window.confirm untouched.
            let originalConfirm = try await js("String(window.confirm)") as! String
            try check(originalConfirm.contains("native code"), "real window.confirm is not replaced by a test stub")
            _ = try await js("input('config-port','\(targetPorts[0])');null")
            try await until("dirty config", "document.body.textContent.includes('有未保存修改')")
            _ = try await js("setTimeout(()=>document.querySelector('a[href=\"/api-models\"]').click(),0);null")
            try await answer("取消")
            try check((try await js("!!document.getElementById('config-port')")) as? Bool == true, "real ConfigPage confirm No retains dirty draft")
            _ = try await js("setTimeout(()=>document.querySelector('a[href=\"/api-models\"]').click(),0);null")
            try await answer("确定")
            try await until("API page after native Yes", "document.querySelector('h1')?.textContent==='API 与模型'")
            try check(true, "real ConfigPage confirm Yes allows navigation")
            try await navigate()
            _ = try await js("input('config-port','\(targetPorts[1])');null")
            try await until("dirty again", "document.body.textContent.includes('有未保存修改')")
            _ = try await js("setTimeout(()=>document.querySelector('a[href=\"/api-models\"]').click(),0);null")
            try await wait("confirm before main navigation") { window.attachedSheet != nil }
            // Native main navigation must retire the suspended old confirmation once.
            coordinator.load(port: port, sessionCookie: services.panelSessionCookie, reloadToken: UUID())
            try await wait("confirm invalidated") { window.attachedSheet == nil }
            try await until("replacement config", "!!document.getElementById('config-port') && !document.body.textContent.includes('有未保存修改')")
            try check(true, "main document replacement cancels real pending confirm once")

            for (index, timedOut) in [false, true].enumerated() {
                let before = events("config:finished")
                try fixture(["finish_delay":0.3,"drop_finish_reply":true])
                if timedOut {
                    _ = try await js("window.nativeDeadline=null;window.observedTimeouts=[];window.originalTimeout=window.setTimeout;window.setTimeout=(fn,ms,...args)=>{window.observedTimeouts.push(ms);if(ms===1050000)window.nativeDeadline=fn;return window.originalTimeout(fn,ms,...args)};null")
                }
                _ = try await js("input('config-port','\(targetPorts[2+index])');null")
                try await until("save enabled", "!button('保存并应用').disabled")
                _ = try await js("button('保存并应用').click();null")
                try await wait("finish applied") { events("config:finished") > before }
                if timedOut { _ = try await js("window.nativeDeadline();null") }
                else { _ = try await js("button('取消应用').click();null") }
                try await until("truthful already applied UI", "document.body.textContent.includes('配置已生效') && document.body.textContent.includes('请重新读取')")
                try check(!services.isBusy, timedOut ? "production JS deadline waits for finish ACK and ConfigPage reports applied" : "production AbortSignal waits for finish ACK and ConfigPage reports applied")
                if timedOut {
                    try check((try await js("window.observedTimeouts.includes(150000)")) as? Bool == true, "cancel ACK keeps a bounded cleanup timeout instead of dropping at default 30 seconds")
                    _ = try await js("window.setTimeout=window.originalTimeout;null")
                }
                try await navigate()
                try await until("fresh config", "!!document.getElementById('config-port')")
            }
            try await navigate("/api-models")
            let beforeRotate = events("config:finished")
            _ = try await js("button('轮换密钥').click();null")
            try await wait("rotation finishing") { events("config:finished") > beforeRotate }
            _ = try await js("button('取消轮换').click();null")
            try await until("truthful API page", "document.body.textContent.includes('配置已生效') && document.body.textContent.includes('请重新读取')")
            try check(!services.isBusy, "API page consumes applied cancellation ACK without claiming rollback")
            try await navigate()
            try fixture(["prepare_delay":0.3])
            let prepares = events("config:prepare"), commits = events("config:commit")
            _ = try await js("input('config-port','\(targetPorts[4])');null")
            try await until("save before SPA unmount", "!button('保存并应用').disabled")
            _ = try await js("button('保存并应用').click();null")
            try await wait("prepare before unmount") { events("config:prepare") > prepares }
            _ = try await js("window.unmountRoot();null")
            try await wait("SPA cancellation cleanup") { !services.isBusy }
            let remainingChildren = try await js("document.getElementById('root').children.length") as? Int
            try check(events("config:commit") == commits && remainingChildren == 0, "SPA unmount cancels exact pending transaction with no late commit or UI write")
            PanelWebView.dismantleNSView(web, coordinator: coordinator)
            await services.shutdown(); window.orderOut(nil)
            try check(services.didFinishShutdown, "real Coordinator test stops every owned helper")
        } catch {
            PanelWebView.dismantleNSView(web, coordinator: coordinator)
            await services.shutdown(); window.orderOut(nil)
            throw error
        }
    }
}
