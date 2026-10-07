import AppKit
import Foundation
import WebKit

private final class ThemeDefaults: UserDefaults {
    private var values: [String: Any] = [:]
    init() { super.init(suiteName: "workbuddy-m07-theme-injected-only")! }
    override func set(_ value: Any?, forKey key: String) { values[key] = value }
    override func integer(forKey key: String) -> Int { values[key] as? Int ?? 0 }
    override func bool(forKey key: String) -> Bool { values[key] as? Bool ?? false }
    override func string(forKey key: String) -> String? { values[key] as? String }
    override func dictionary(forKey key: String) -> [String: Any]? { values[key] as? [String: Any] }
}
@main struct WebCheck {
    @MainActor static func main() {
        let app = NSApplication.shared
        app.setActivationPolicy(.prohibited)
        Task { @MainActor in
            do { try await run(); print("M07_WEBKIT_SELF_CHECK_PASS"); exit(0) }
            catch { print("M07_WEBKIT_FAIL \(error)"); exit(1) }
        }
        app.run()
    }
    @MainActor static func run() async throws {
        enum Failure: Error { case check(String) }
        func check(_ condition: Bool, _ name: String) throws {
            guard condition else { throw Failure.check(name) }
            print("PASS WK \(name)")
        }
        let root = URL(fileURLWithPath: ProcessInfo.processInfo.environment["WB_TEST_ROOT"]!)
        precondition(root.path.hasPrefix("/private/tmp/"))
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        let view = WKWebView(frame: NSRect(x: 0, y: 0, width: 900, height: 700), configuration: config)
        let window = NSWindow(contentRect: view.frame, styleMask: [.titled], backing: .buffered, defer: false)
        window.contentView = view
        view.loadFileURL(root.appendingPathComponent("browser.html"), allowingReadAccessTo: root)
        var done = false
        for _ in 0..<750 {
            if (try? await view.evaluateJavaScript("window.done === true")) as? Bool == true { done = true; break }
            try await Task.sleep(for: .milliseconds(20))
        }
        let failure = try await view.evaluateJavaScript("window.failure || ''") as? String ?? ""
        if !done || !failure.isEmpty { print("PAGE_DEBUG", try await view.evaluateJavaScript("JSON.stringify({checks:window.checks,body:document.body.innerText,commands:window.requests,network:window.network})") as Any) }
        try check(done && failure.isEmpty, "real React browser flow completed: \(failure)")
        let checks = try await view.evaluateJavaScript("window.checks || []") as? [String] ?? []
        if ProcessInfo.processInfo.environment["WB_FINISH_PAGES_ONLY"] == "1" {
            try check(checks.contains("desktop system excludes forbidden web controls and API") && checks.contains("only final completion writes completed flag"), "finish-page required flows executed")
            for result in checks { print("PASS WK \(result)") }
            window.contentView = nil
            return
        }
        try check(checks.count >= 9, "React functional checks executed")
        for result in checks { print("PASS WK \(result)") }
        let settings = AppSettings(defaults: ThemeDefaults())
        let utilities = DesktopUtilities(settings: settings,
            log: AppLog(directory: root.appendingPathComponent("theme-logs"), emitSystemLog: false),
            snapshot: { .init(gateway: .stopped, panel: .notStarted, apiPort: nil) })
        let owner = NativeBridge.DocumentOwner()
        for (theme, dark) in [("light", false), ("dark", true)] {
            _ = try await utilities.dispatch(action: "preferences.update", payload: ["theme": theme], owner: owner)
            var matched = false
            for _ in 0..<100 {
                if (try await view.evaluateJavaScript("matchMedia('(prefers-color-scheme: dark)').matches")) as? Bool == dark { matched = true; break }
                try await Task.sleep(for: .milliseconds(20))
            }
            try check(matched, "native \(theme) preference updates WebKit color-scheme media")
        }
        _ = try await utilities.dispatch(action: "preferences.update", payload: ["theme": "system"], owner: owner)
        try check(NSApp.appearance == nil, "system preference clears app appearance override")
        window.contentView = nil
    }
}
