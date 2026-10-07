import SwiftUI
import AppKit

/// 主窗口内容：原生服务条 + 内置面板。
///
/// 为什么保留一条原生服务条（而不是把控制全交给面板）：
///   面板是 Web 内容，它挂掉的时候「重试 / 启动 / 停止」必须仍然可用 ——
///   这与「原生故障页不能用 HTML 实现」是同一条原则（开发计划 §6.1）。
///
/// 服务条只放窗口级动作，不承载任何页面内容，
/// 因此不构成对侧栏导航的重复（设计规范 §2.2）。
struct MainWindowView: View {

    @EnvironmentObject private var services: ServiceManager
    @Environment(\.openWindow) private var openWindow
    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        VStack(spacing: 0) {
            NativeServiceBar()
            Divider()
            PanelContainerView(services: services)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .frame(minWidth: 960, minHeight: 640)
        .background(DesktopPalette.workspace(colorScheme))
        .background(WindowIdentifierAccessor(identifier: AppDelegate.mainWindowID))
        .onAppear { AppDelegate.reopenMainWindow = { openWindow(id: AppDelegate.mainWindowID) } }
    }
}

// MARK: - 原生配色

/// 与管理面板共用的暖白 / 铜棕视觉基调；状态色仍由 GatewayState 决定。
enum DesktopPalette {
    static let warningStatus = Color(red: 0.85, green: 0.66, blue: 0.06)

    static func workspace(_ scheme: ColorScheme) -> Color {
        scheme == .dark
            ? Color(red: 28.0 / 255, green: 27.0 / 255, blue: 25.0 / 255)
            : Color(red: 247.0 / 255, green: 246.0 / 255, blue: 242.0 / 255)
    }

    static func surface(_ scheme: ColorScheme) -> Color {
        scheme == .dark
            ? Color(red: 36.0 / 255, green: 35.0 / 255, blue: 32.0 / 255)
            : .white
    }

    static func text(_ scheme: ColorScheme) -> Color {
        scheme == .dark
            ? Color(red: 238.0 / 255, green: 236.0 / 255, blue: 230.0 / 255)
            : Color(red: 41.0 / 255, green: 39.0 / 255, blue: 34.0 / 255)
    }

    static func secondaryText(_ scheme: ColorScheme) -> Color {
        scheme == .dark
            ? Color(red: 187.0 / 255, green: 181.0 / 255, blue: 170.0 / 255)
            : Color(red: 104.0 / 255, green: 100.0 / 255, blue: 91.0 / 255)
    }

    static func accent(_ scheme: ColorScheme) -> Color {
        scheme == .dark
            ? Color(red: 225.0 / 255, green: 179.0 / 255, blue: 143.0 / 255)
            : Color(red: 135.0 / 255, green: 83.0 / 255, blue: 51.0 / 255)
    }

    static func buttonFill(_ scheme: ColorScheme) -> Color {
        scheme == .dark
            ? Color(red: 149.0 / 255, green: 98.0 / 255, blue: 64.0 / 255)
            : accent(scheme)
    }

    static func status(_ state: GatewayState) -> Color {
        switch state {
        case .runningReady: return .green
        case .starting, .runningUnavailable, .maintenance, .stopping: return warningStatus
        case .stopped, .failed: return .red
        }
    }
}

// MARK: - 原生服务条

/// 网关状态 + 启动 / 停止 / 重启。
///
/// 状态文案直接取自七态状态机（`GatewayState`），因此
/// 「运行中 · 暂不可调用」（进程活着但没可用账号）与「启动失败」在原生层也是分开表达的。
struct NativeServiceBar: View {

    @EnvironmentObject private var services: ServiceManager
    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        HStack(spacing: 16) {
            HStack(spacing: 10) {
                Circle()
                    .fill(statusTint)
                    .frame(width: 8, height: 8)
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: 3) {
                    Text("WBPMM网关")
                        .font(.system(size: 12, weight: .medium))
                        .foregroundStyle(DesktopPalette.secondaryText(colorScheme))

                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(services.gatewayState.label)
                            .font(.system(size: 13, weight: .semibold))
                            .foregroundStyle(DesktopPalette.text(colorScheme))

                        Text(services.gatewayState.detail)
                            .font(.system(size: 12))
                            .foregroundStyle(DesktopPalette.secondaryText(colorScheme))
                            .lineLimit(1)
                            .truncationMode(.tail)
                    }
                }
            }
            .accessibilityElement(children: .combine)
            .help(services.gatewayState.detail)

            Spacer(minLength: 16)

            if services.isBusy {
                ProgressView()
                    .controlSize(.small)
                    .accessibilityLabel("操作进行中")
            }

            HStack(spacing: 6) {
                Button("启动") { Task { await services.startGateway() } }
                    .disabled(!services.gatewayState.allowsStart || services.isBusy)

                Button("停止") { Task { await services.stopGateway() } }
                    .disabled(!services.gatewayState.allowsStop || (services.isBusy && services.gatewayState != .starting))

                Button("重启") { Task { await services.restartGateway() } }
                    .disabled(!services.gatewayState.allowsRestart || services.isBusy)
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
            .tint(DesktopPalette.accent(colorScheme))
        }
        .padding(.horizontal, 16)
        .frame(height: 48)
        .background(DesktopPalette.surface(colorScheme))
    }

    private var statusTint: Color {
        DesktopPalette.status(services.gatewayState)
    }
}

// MARK: - 窗口标识

/// 给 SwiftUI 创建的窗口打上标识。
///
/// 用途：菜单栏「打开主窗口」与 Dock 点击都要按标识找回窗口。
/// SwiftUI 的 `Window` 场景不保证窗口 identifier 等于场景 id，因此显式设置。
///
/// 顺带记录一次窗口几何。面板加载失败时，「窗口多大、屏幕多大」是排查布局问题的
/// 必要信息，而这两个值只有原生层知道 —— 面板起不来时它自己也报不出来。
struct WindowIdentifierAccessor: NSViewRepresentable {

    let identifier: String

    func makeNSView(context: Context) -> NSView {
        let view = NSView(frame: .zero)
        // makeNSView 阶段 view.window 还是 nil，等这一轮布局结束再设置。
        DispatchQueue.main.async {
            guard let window = view.window else { return }
            window.identifier = NSUserInterfaceItemIdentifier(identifier)
            if AppDelegate.isLoginBackgroundLaunch && NSApp.activationPolicy() == .accessory { window.orderOut(nil) }

            let frame = window.frame
            let screen = window.screen ?? NSScreen.main
            let screenFrame = screen?.frame ?? .zero
            AppLog.shared.info(
                "主窗口就绪：\(Int(frame.width))×\(Int(frame.height)) "
                + "@ (\(Int(frame.origin.x)),\(Int(frame.origin.y)))；"
                + "屏幕 \(Int(screenFrame.width))×\(Int(screenFrame.height))")
        }
        return view
    }

    func updateNSView(_ nsView: NSView, context: Context) {}
}
