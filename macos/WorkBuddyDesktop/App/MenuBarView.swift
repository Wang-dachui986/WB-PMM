import AppKit

/// 由应用委托持有固定菜单栏入口，不依赖 SwiftUI 窗口或 Scene 是否仍在显示。
@MainActor
final class MenuBarController: NSObject, NSMenuDelegate {
    let statusItem: NSStatusItem
    private let services: ServiceManager
    private let onQuit: () -> Void

    init(services: ServiceManager, onQuit: @escaping () -> Void) {
        self.services = services
        self.onQuit = onQuit
        statusItem = NSStatusBar.system.statusItem(withLength: 32)
        super.init()
        statusItem.behavior = []
        if let button = statusItem.button {
            if let image = NSImage(named: "WBPMMMenuTemplate")
                ?? NSImage(systemSymbolName: "bolt.horizontal.circle", accessibilityDescription: "WB-PMM") {
                image.size = NSSize(width: 24, height: 16)
                image.isTemplate = true
                button.image = image
            } else {
                button.title = "WB"
            }
            button.setAccessibilityLabel("WB-PMM控制台")
            button.toolTip = "WB-PMM控制台 · 点击或右键打开菜单"
        }
        let menu = NSMenu()
        menu.delegate = self
        statusItem.menu = menu
        ensureVisible()
    }

    func ensureVisible() { statusItem.isVisible = true }

    func menuNeedsUpdate(_ menu: NSMenu) {
        menu.removeAllItems()
        menu.addItem(withTitle: "WBPMM网关 · \(services.gatewayState.label)", action: nil, keyEquivalent: "")
        if let health = services.gatewayHealth {
            menu.addItem(withTitle: "可用账号 \(health.healthy) / \(health.total)", action: nil, keyEquivalent: "")
        }
        menu.addItem(.separator())
        add("打开主窗口", #selector(openWindow), to: menu)
        menu.addItem(.separator())
        if services.gatewayState.allowsStart { add("启动 API 服务", #selector(startGateway), to: menu) }
        if services.gatewayState.allowsStop { add("停止 API 服务", #selector(stopGateway), to: menu) }
        if services.gatewayState.allowsRestart { add("重启 API 服务", #selector(restartGateway), to: menu) }
        menu.addItem(.separator())
        if services.apiBaseURL != nil { add("复制 API 地址", #selector(copyAPIBaseURL), to: menu) }
        add("打开日志目录", #selector(openLogs), to: menu)
        menu.addItem(.separator())
        add("完全退出", #selector(quit), to: menu)
    }

    private func add(_ title: String, _ action: Selector, to menu: NSMenu) {
        let item = menu.addItem(withTitle: title, action: action, keyEquivalent: "")
        item.target = self
    }

    @objc private func openWindow() { AppDelegate.showMainWindow() }
    @objc private func startGateway() { Task { await services.startGateway() } }
    @objc private func stopGateway() { Task { await services.stopGateway() } }
    @objc private func restartGateway() { Task { await services.restartGateway() } }
    @objc private func copyAPIBaseURL() {
        guard let base = services.apiBaseURL else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(base, forType: .string)
    }
    @objc private func openLogs() { NSWorkspace.shared.open(AppPaths.logDirectory) }
    @objc private func quit() { onQuit() }
}
