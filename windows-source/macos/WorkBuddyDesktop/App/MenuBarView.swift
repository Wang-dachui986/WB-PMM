import SwiftUI
import AppKit

/// 菜单栏品牌图标；服务状态保留在菜单正文和辅助说明中。
struct MenuBarLabel: View {

    @ObservedObject var services: ServiceManager
    @Environment(\.openWindow) private var openWindow

    private static let icon: NSImage? = {
        guard let image = NSImage(named: "WBPMMMenuTemplate") else { return nil }
        image.size = NSSize(width: 28, height: 16)
        image.isTemplate = true
        return image
    }()

    var body: some View {
        Group {
            if let icon = Self.icon {
                Image(nsImage: icon)
            } else {
                Image(systemName: "bolt.horizontal.circle")
                    .frame(width: 28, height: 16)
            }
        }
        .accessibilityLabel("WB-PMM控制台：\(services.gatewayState.label)")
        .help(services.gatewayState.detail)
        .onAppear { AppDelegate.reopenMainWindow = { openWindow(id: AppDelegate.mainWindowID) } }
    }
}

// MARK: - 菜单栏下拉

/// 菜单栏下拉内容。
///
/// 只做窗口级动作（设计规范 §2.2）：打开主窗口 / API 启停 / 复制 API 地址 / 完全退出。
/// **它不是第二套导航** —— 侧栏能到的页面一个都不在这里出现。
struct MenuBarView: View {

    @EnvironmentObject private var services: ServiceManager
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        // 不可点击的状态行
        Text("WBPMM网关 · \(services.gatewayState.label)")

        if let health = services.gatewayHealth {
            Text("可用账号 \(health.healthy) / \(health.total)")
        }

        Divider()

        Button {
            AppDelegate.activateForMainWindow()
            openWindow(id: AppDelegate.mainWindowID)
        } label: {
            Label("打开主窗口", systemImage: "macwindow")
        }

        Divider()

        // 只呈现当前状态允许的动作，避免出现「按钮在但点了报错」。
        if services.gatewayState.allowsStart {
            Button { Task { await services.startGateway() } } label: {
                Label("启动 API 服务", systemImage: "play.fill")
            }
        }
        if services.gatewayState.allowsStop {
            Button { Task { await services.stopGateway() } } label: {
                Label("停止 API 服务", systemImage: "stop.fill")
            }
        }
        if services.gatewayState.allowsRestart {
            Button { Task { await services.restartGateway() } } label: {
                Label("重启 API 服务", systemImage: "arrow.clockwise")
            }
        }

        Divider()

        Button(action: copyAPIBaseURL) {
            Label("复制 API 地址", systemImage: "doc.on.doc")
        }
            .disabled(services.apiBaseURL == nil)

        Button {
            NSWorkspace.shared.open(AppPaths.logDirectory)
        } label: {
            Label("打开日志目录", systemImage: "folder")
        }

        Divider()

        Button { NSApp.terminate(nil) } label: {
            Label("完全退出", systemImage: "power")
        }
    }

    private func copyAPIBaseURL() {
        guard let base = services.apiBaseURL else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(base, forType: .string)
        AppLog.shared.info("已复制 API 地址到剪贴板")
    }
}
