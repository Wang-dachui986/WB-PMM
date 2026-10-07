// Compile with all macOS sources, -DDEBUG -DWBPMM_LIFECYCLE_CHECK -parse-as-library.
// Run with WB_TEST_ROOT (private temp directory), WB_TEST_PORT, WB_HELPERS_DIR.
import AppKit
import CoreServices
import Foundation

@main struct MenuLifecycleCheck {
    @MainActor static func main() {
        guard ProcessInfo.processInfo.environment["WB_TEST_ROOT"]?.hasPrefix("/private/tmp/") == true else { fatalError("isolated root required") }
        let app = NSApplication.shared
        let delegate = AppDelegate()
        app.delegate = delegate
        app.setActivationPolicy(.regular)
        let window = NSWindow(contentRect: NSRect(x: 80, y: 80, width: 300, height: 180), styleMask: [.titled, .closable], backing: .buffered, defer: false)
        window.identifier = NSUserInterfaceItemIdentifier(AppDelegate.mainWindowID)
        window.isReleasedWhenClosed = false
        AppDelegate.reopenMainWindow = { window.makeKeyAndOrderFront(nil) }
        let token = NotificationCenter.default.addObserver(forName: NSApplication.willTerminateNotification, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated {
                precondition(delegate.services.didFinishShutdown)
                print("PASS menu explicit quit completed service shutdown")
                fflush(stdout)
            }
        }
        Task { @MainActor in
            do {
                for _ in 0..<150 {
                    if case .ready = delegate.services.panelState { break }
                    try await Task.sleep(for: .milliseconds(100))
                }
                guard case .ready = delegate.services.panelState else { fatalError("panel did not start") }
                let started = await delegate.services.startGateway()
                precondition(started)
                let originalPanel = delegate.services.panelState
                let menuBar = delegate.menuBar!
                precondition(menuBar.statusItem.isVisible && menuBar.statusItem.button?.image != nil)
                precondition(!menuBar.statusItem.behavior.contains(.removalAllowed))
                print("PASS persistent status item has visible image and cannot be removed")
                window.makeKeyAndOrderFront(nil)
                window.performClose(nil)
                try await Task.sleep(for: .milliseconds(100))
                precondition(app.activationPolicy() == .accessory)
                precondition(!delegate.applicationShouldTerminateAfterLastWindowClosed(app))
                precondition(delegate.services.panelState == originalPanel && !delegate.services.isShuttingDown)
                print("PASS close window preserves background services")
                let menu = menuBar.statusItem.menu!
                menuBar.menuNeedsUpdate(menu)
                menu.performActionForItem(at: menu.items.firstIndex { $0.title == "打开主窗口" }!)
                precondition(app.activationPolicy() == .regular && window.isVisible)
                app.terminate(nil) // Same NSApplication exit flow as Dock Quit and Cmd-Q.
                precondition(app.activationPolicy() == .accessory && !delegate.services.isShuttingDown)
                let configuration = URLSessionConfiguration.ephemeral
                configuration.connectionProxyDictionary = [:]
                let session = URLSession(configuration: configuration)
                let (data, response) = try await session.data(from: URL(string: "http://127.0.0.1:\(delegate.services.apiPort!)/healthz")!)
                // Empty isolated account pool intentionally reports unavailable, but the API is alive.
                precondition((response as? HTTPURLResponse)?.statusCode == 503)
                let health = try JSONSerialization.jsonObject(with: data) as! [String: Any]
                precondition(health["service"] as? String == "workbuddy2api" && health["total"] as? Int == 0)
                session.invalidateAndCancel()
                print("PASS Dock-style quit hides app; API still responds and panel session is unchanged")
                precondition(delegate.services.panelState == originalPanel)
                precondition(!AppDelegate.isSystemTermination(nil))
                for reason in [kAEQuitAll, kAEShutDown, kAERestart, kAEReallyLogOut] {
                    let event = NSAppleEventDescriptor(eventClass: kCoreEventClass, eventID: kAEQuitApplication, targetDescriptor: nil, returnID: AEReturnID(kAutoGenerateReturnID), transactionID: AETransactionID(kAnyTransactionID))
                    event.setParam(NSAppleEventDescriptor(enumCode: reason), forKeyword: kAEQuitReason)
                    precondition(AppDelegate.isSystemTermination(event))
                }
                print("PASS shutdown/restart/logout exit reasons allowed")
                fflush(stdout)
                // Deliver like a native menu click, outside this MainActor Task: AppKit's
                // terminateLater enters a nested event loop until shutdown replies.
                DispatchQueue.main.async {
                    menu.performActionForItem(at: menu.items.firstIndex { $0.title == "完全退出" }!)
                }
            } catch {
                print("FAIL \(error)"); fflush(stdout)
                await delegate.services.shutdown()
                exit(1)
            }
        }
        withExtendedLifetime((delegate, token, window)) { app.run() }
    }
}
