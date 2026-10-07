import Foundation
import AppKit
import Darwin

@main struct AccountCheck {
    @MainActor static func main() async throws {
        final class MemoryDefaults: UserDefaults, @unchecked Sendable {
            override func set(_ value: Any?, forKey key: String) {
                var values = volatileDomain(forName: UserDefaults.argumentDomain)
                values[key] = value; setVolatileDomain(values, forName: UserDefaults.argumentDomain)
            }
        }
        let defaults = MemoryDefaults(suiteName: "workbuddy-m04-accounts")!
        defaults.setVolatileDomain(["apiPort": Int(ProcessInfo.processInfo.environment["WB_TEST_PORT"]!)!], forName: UserDefaults.argumentDomain)
        let settings = AppSettings(defaults: defaults)
        var confirmation: CheckedContinuation<Void, Never>?
        var holdConfirmation = false
        let services = ServiceManager(settings: settings, maintenanceLifetime: 30, accountInteractionLifetime: 0.7,
            maintenanceConfirmation: { _ in
                if holdConfirmation { await withCheckedContinuation { confirmation = $0 } }
                return .abort
            })
        let owner = NativeBridge.DocumentOwner()
        func check(_ ok: Bool, _ label: String) {
            guard ok else { print("FAIL M04 ACCOUNT \(label)"); exit(1) }
            print("PASS M04 ACCOUNT \(label)")
        }
        func fixture(_ value: [String: Any] = [:]) throws {
            try JSONSerialization.data(withJSONObject: value).write(to: AppPaths.dataDirectory.appendingPathComponent("fixture.json"), options: .atomic)
        }
        func events(_ name: String) -> Int {
            let raw = (try? String(contentsOf: AppPaths.dataDirectory.appendingPathComponent("events.jsonl"), encoding: .utf8)) ?? ""
            return raw.split(separator: "\n").filter { (try? JSONSerialization.jsonObject(with: Data($0.utf8)) as? [String: Any])?["event"] as? String == name }.count
        }
        func rejected(_ label: String, _ work: () async throws -> Void) async {
            do { try await work(); check(false, label) } catch { check(true, label) }
        }
        func wait(_ predicate: () -> Bool) async throws {
            for _ in 0..<200 { if predicate() { return }; try await Task.sleep(for: .milliseconds(10)) }
            check(false, "wait timed out")
        }
        func begin(_ action: String, id: String = "begin", uids: [String] = ["fake001"]) async throws -> [String: Any] {
            try await services.performMaintenance(.begin(action: action, uids: uids, requestID: id), owner: owner)
        }
        func end(_ value: [String: Any]) async throws {
            _ = try await services.performMaintenance(.end(instanceID: value["instance_id"] as! String, generation: UInt64(value["generation"] as! String)!), owner: owner)
        }
        check(services.prepareForLaunch(), "isolated paths")
        try fixture()
        check(await services.startPanelService(), "fake panel with private transport")
        let file = AppPaths.dataDirectory.appendingPathComponent("selected.json")
        let raw: [String: Any] = ["account": ["uid": "fake001", "nickname": "Synthetic"], "auth": ["accessToken": "SYNTHETIC_ONLY", "refreshToken": "SYNTHETIC_REFRESH", "domain": "copilot.tencent.com", "expiresAt": 1999999999]]
        try JSONSerialization.data(withJSONObject: raw).write(to: file)
        services.importFileChooser = { file }
        check(try NativeAuthImport.read(file).uid == "fake001", "nested schema parses in native only")
        let flat = AppPaths.dataDirectory.appendingPathComponent("flat.json")
        try JSONSerialization.data(withJSONObject: ["uid":"fake001","accessToken":"SYNTHETIC_ONLY","domain":"www.workbuddy.ai"]).write(to: flat)
        check(try NativeAuthImport.read(flat).region == "global", "flat schema supported")
        let oversized = AppPaths.dataDirectory.appendingPathComponent("large.json")
        try Data(repeating: 32, count: (1 << 20) + 1).write(to: oversized)
        await rejected("over 1 MiB rejected") { _ = try NativeAuthImport.read(oversized) }
        let link = AppPaths.dataDirectory.appendingPathComponent("link.json")
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: file)
        await rejected("symlink rejected") { _ = try NativeAuthImport.read(link) }
        let fifo = AppPaths.dataDirectory.appendingPathComponent("fifo.json")
        mkfifo(fifo.path, 0o600)
        await rejected("FIFO rejected without blocking") { _ = try NativeAuthImport.read(fifo) }
        for invalid: [String: Any] in [["uid":"../fake","accessToken":"x"], ["uid":"fake001","accessToken":"x","expiresAt":true], ["uid":"fake001","accessToken":"x","domain":"evil.example"]] {
            try JSONSerialization.data(withJSONObject: invalid).write(to: flat)
            await rejected("invalid credential metadata rejected") { _ = try NativeAuthImport.read(flat) }
        }
        var preview = try await services.performMaintenance(.importPreview(requestID:"preview-1"), owner: owner)
        check(Set(preview.keys) == ["preview_id","uid","region","expires_at","overwrite_required"] && !String(describing:preview).contains("SYNTHETIC"), "preview projection has no raw credential")
        _ = try await services.performMaintenance(.importCancel(previewID:preview["preview_id"] as! String), owner:owner)
        await rejected("cancelled preview is unusable") { _ = try await services.performMaintenance(.importConfirm(previewID:preview["preview_id"] as! String,overwrite:false,requestID:"confirm"),owner:owner) }
        preview = try await services.performMaintenance(.importPreview(requestID:"late-preview"), owner:owner)
        _ = try await services.performMaintenance(.cancelPending(requestID:"late-preview",importing:true),owner:owner)
        await rejected("cancel by request ID clears an already-created late preview") { _ = try await services.performMaintenance(.importConfirm(previewID:preview["preview_id"] as! String,overwrite:false,requestID:"confirm"),owner:owner) }
        preview = try await services.performMaintenance(.importPreview(requestID:"preview-expiry"), owner:owner)
        try await Task.sleep(for:.milliseconds(800))
        await rejected("expired preview is unusable") { _ = try await services.performMaintenance(.importConfirm(previewID:preview["preview_id"] as! String,overwrite:false,requestID:"confirm"),owner:owner) }
        try fixture(["existing_domain":"www.workbuddy.ai"])
        await rejected("cross-region preview refused") { _ = try await services.performMaintenance(.importPreview(requestID:"cross"),owner:owner) }
        try fixture(["existing_domain":"copilot.tencent.com"])
        preview = try await services.performMaintenance(.importPreview(requestID:"overwrite"),owner:owner)
        check(preview["overwrite_required"] as? Bool == true, "same-region overwrite disclosed")
        await rejected("overwrite requires explicit confirmation") { _ = try await services.performMaintenance(.importConfirm(previewID:preview["preview_id"] as! String,overwrite:false,requestID:"confirm"),owner:owner) }
        _ = try await services.performMaintenance(.importConfirm(previewID:preview["preview_id"] as! String,overwrite:true,requestID:"confirm"),owner:owner)
        check(events("import") == 1 && !services.isBusy, "confirmed import fixed transport and cleanup")
        await rejected("import preview is single-use") { _ = try await services.performMaintenance(.importConfirm(previewID:preview["preview_id"] as! String,overwrite:true,requestID:"confirm"),owner:owner) }
        try fixture()
        var chooser: CheckedContinuation<URL?, Never>?
        services.importFileChooser = { await withCheckedContinuation { chooser = $0 } }
        let choosing = Task { _ = try await services.performMaintenance(.importPreview(requestID:"picker-pending"),owner:owner) }
        try await wait { chooser != nil }
        var cancelDone = false
        let cancel = Task { _ = try await services.performMaintenance(.cancelPending(requestID:"picker-pending",importing:true),owner:owner); cancelDone=true }
        try await Task.sleep(for:.milliseconds(50))
        check(!cancelDone && services.isBusy, "picker cancel waits for in-flight chooser")
        chooser?.resume(returning:file); chooser=nil
        _ = try await cancel.value
        await rejected("late picker result cannot leave a preview") { _ = try await choosing.value }
        check(!services.isBusy, "picker cleanup releases operation lock")
        services.importFileChooser = { file }
        holdConfirmation=true
        let confirming = Task { _ = try await begin("refresh",id:"confirm-pending") }
        try await wait { confirmation != nil }
        cancelDone=false
        let cancelConfirm=Task { _ = try await services.performMaintenance(.cancelPending(requestID:"confirm-pending",importing:false),owner:owner); cancelDone=true }
        try await Task.sleep(for:.milliseconds(50))
        check(!cancelDone, "confirmation cancel waits for actual cleanup")
        confirmation?.resume(); confirmation=nil; holdConfirmation=false
        _ = try await cancelConfirm.value
        await rejected("cancelled confirmation cannot grant") { _ = try await confirming.value }
        let lease=try await begin("login",uids:[])
        var opened: [URL]=[]
        services.authorizationOpener={ opened.append($0); return true }
        let sessionID=String(repeating:"a",count:32)
        let browser=try await services.performMaintenance(.openBrowser(sessionID:sessionID),owner:owner)
        check(Set(browser.keys)==["opened"] && opened.count==1, "private session lookup opens allowlisted URL without JS disclosure")
        for url in ["http://copilot.tencent.com/a","https://copilot.tencent.com.evil.example/a","https://www.workbuddy.ai@evil.example/a","https://www.workbuddy.ai:8443/a","https://workbuddy.ai/a"] {
            try fixture(["auth_url":url])
            await rejected("non-allowlisted authorization URL rejected") { _ = try await services.performMaintenance(.openBrowser(sessionID:sessionID),owner:owner) }
        }
        try fixture(["auth_url":"https://www.workbuddy.ai/a"])
        _ = try await services.performMaintenance(.openBrowser(sessionID:sessionID),owner:owner)
        check(opened.count==2, "global exact host allowed")
        try await end(lease)
        await rejected("browser lookup requires active login permit") { _ = try await services.performMaintenance(.openBrowser(sessionID:sessionID),owner:owner) }
        try fixture()
        let deletion=try await begin("delete")
        settings.setAccountNote(uid:"fake001",value:"local note")
        try fixture(["delete_fail":true])
        await rejected("delete failure propagates") { _ = try await services.performMaintenance(.delete(uid:"fake001"),owner:owner) }
        check(settings.accountNotes["fake001"]=="local note", "delete failure keeps note")
        try fixture()
        _ = try await services.performMaintenance(.delete(uid:"fake001"),owner:owner)
        check(settings.accountNotes["fake001"]==nil, "successful fixed delete clears only selected note")
        try await end(deletion)
        check(await services.startGateway(), "owned fake gateway starts")
        _ = try await services.performMaintenance(.setEnabled(uid:"fake001",enabled:false),owner:owner)
        _ = try await services.performMaintenance(.setEnabled(uid:"fake001",enabled:true),owner:owner)
        check(events("manual_disable")==1 && events("manual_enable")==1 && services.gatewayState == .runningUnavailable, "manual enable uses fixed owned gateway management transport")
        for (action,payload) in [("auth.openBrowser",["session_id":sessionID,"url":"https://evil.example"] as [String:Any]),("files.importAuth",["operation":"preview","path":"/private"]),("account.setEnabled",["uid":"fake001","enabled":1]),("account.maintenance",["operation":"cancel_pending","request_id":"x","extra":true])] {
            check(NativeBridge.accountRequest(.init(id:"x",action:action,payload:payload))==nil,"strict native account payload")
        }
        await services.shutdown()
        check(services.didFinishShutdown,"owned fake helpers stopped")
        print("M04_ACCOUNTS_PASS")
    }
}
