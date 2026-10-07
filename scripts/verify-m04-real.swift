import Foundation
import Darwin

/// Empty auth only: real bundled Go control/drain wiring, never login/poll/import.
@main struct RealM04Check {
    @MainActor static func main() async throws {
        if ProcessInfo.processInfo.environment["WB_CENTRAL_ONLY"] == "1" { try await configurationChecks(); return }
        let defaults=UserDefaults(suiteName:"m04-real-empty")!
        defaults.setVolatileDomain(["apiPort":Int(ProcessInfo.processInfo.environment["WB_TEST_PORT"]!)!],forName:UserDefaults.argumentDomain)
        let services=ServiceManager(settings:AppSettings(defaults:defaults),maintenanceLifetime:20,maintenanceConfirmation:{ _ in .wait })
        let owner=NativeBridge.DocumentOwner()
        func check(_ value:Bool,_ label:String) { guard value else { print("FAIL M04 REAL \(label): \(services.lastError ?? "")"); exit(1) }; print("PASS M04 REAL \(label)") }
        check(services.prepareForLaunch(),"isolated empty-auth initial configuration")
        check(try FileManager.default.contentsOfDirectory(atPath:AppPaths.authDirectory.path).isEmpty,"empty auth before any helper starts")
        check(await services.startPanelService(),"real panel private handshake and Cookie")
        let originalInstance=services.instanceID
        check(await services.startGateway(),"real gateway empty pool starts")
        check(services.gatewayState == .runningUnavailable,"empty pool is running/unavailable")
        let first=try await services.performMaintenance(.begin(action:"login",uids:[],requestID:"real-first"),owner:owner)
        check(services.gatewayState == .maintenance && services.isBusy,"real atomic drain then own gateway stopped before permit")
        let instance=first["instance_id"] as! String, generation=UInt64(first["generation"] as! String)!
        check(instance==originalInstance,"permit bound to real panel instance")
        _ = try await services.performMaintenance(.end(instanceID:instance,generation:generation),owner:owner)
        check(!services.isBusy && services.gatewayState == .runningUnavailable,"real revoke drained and prior gateway restored")
        _ = try await services.performMaintenance(.begin(action:"login",uids:[],requestID:"real-cancel"),owner:owner)
        _ = try await services.performMaintenance(.cancelPending(requestID:"real-cancel",importing:false),owner:owner)
        check(!services.isBusy && services.gatewayState == .runningUnavailable,"precise request cancel revokes real permit and restores")
        check(await services.stopGateway(),"ordinary stop terminates the owned gateway")
        let stopped=try await services.performMaintenance(.begin(action:"login",uids:[],requestID:"real-stopped"),owner:owner)
        _ = try await services.performMaintenance(.end(instanceID:stopped["instance_id"] as! String,generation:UInt64(stopped["generation"] as! String)!),owner:owner)
        check(services.gatewayState == .stopped,"previously stopped gateway remains stopped")
        check(try FileManager.default.contentsOfDirectory(atPath:AppPaths.authDirectory.path).isEmpty,"auth remains empty and no OAuth/import call performed")
        await services.shutdown()
        check(services.didFinishShutdown,"owned real Go helpers stopped")
        print("M04_REAL_EMPTY_AUTH_PASS")
    }
    @MainActor static func configurationChecks() async throws {
        enum Failure: Error { case check(String) }
        let defaults=UserDefaults(suiteName:"native-real-config")!
        defaults.setVolatileDomain(["apiPort":Int(ProcessInfo.processInfo.environment["WB_TEST_PORT"]!)!],forName:UserDefaults.argumentDomain)
        let services=ServiceManager(settings:AppSettings(defaults:defaults),maintenanceConfirmation:{_ in .wait})
        let owner=NativeBridge.DocumentOwner()
        func check(_ value:Bool,_ label:String) throws {guard value else {throw Failure.check(label+" / "+(services.lastError ?? ""))};print("PASS CENTRAL REAL \(label)")}
        func stats(port:Int,key:String) async throws -> Int {
            let config=URLSessionConfiguration.ephemeral;config.connectionProxyDictionary=[:];config.httpCookieStorage=nil
            let session=URLSession(configuration:config,delegate:NoRedirect(),delegateQueue:nil);defer{session.invalidateAndCancel()}
            var request=URLRequest(url:URL(string:"http://127.0.0.1:\(port)/v1/stats")!);request.setValue("Bearer "+key,forHTTPHeaderField:"Authorization")
            let (_,response)=try await session.data(for:request);return (response as! HTTPURLResponse).statusCode
        }
        do {
            try check(services.prepareForLaunch(),"isolated initial snapshot")
            try check(await services.startPanelService(),"real private panel ready")
            let original=services.instanceID, nextPort=Int(ProcessInfo.processInfo.environment["WB_TEST_NEXT_PORT"]!)!
            let applied=try await services.performConfiguration(.apply(revision:GatewayConfigFile.readSnapshot().revision,patch:["port":nextPort],requestID:"real-apply"),owner:owner)
            try check(services.gatewayState == .stopped && services.apiPort==nextPort,"port commit keeps initially stopped gateway stopped")
            try check((applied["config"] as? [String:Any])?["revision"] as? String == GatewayConfigFile.readSnapshot().revision,"commit returns exact safe revision")
            try check(services.instanceID==original,"Go updates panel connection without restarting panel")
            try check(await services.startGateway() && services.gatewayState == .runningUnavailable,"committed dynamic port starts with empty-auth 503")
            let old=try GatewayConfigFile.readSnapshot().apiKey
            try check(try await stats(port:nextPort,key:old)==200,"committed inference key reaches local-only stats")
            let rotated=try await services.performConfiguration(.rotate(revision:GatewayConfigFile.readSnapshot().revision,requestID:"real-rotate"),owner:owner)
            let new=try GatewayConfigFile.readSnapshot().apiKey
            try check(new != old && Data(base64Encoded: new.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/") + "=")?.count == 32,"real controlled key rotation uses fresh random material")
            try check(try await stats(port:nextPort,key:old)==401,"old inference key rejected after rotation")
            try check(try await stats(port:nextPort,key:new)==200,"new inference key accepted by restored owned gateway")
            let text=String(data:try JSONSerialization.data(withJSONObject:rotated),encoding:.utf8)!
            try check(!text.contains(new) && !text.contains(old),"bridge transaction result contains no key")
            try check(services.instanceID==original && services.gatewayState == .runningUnavailable,"panel instance retained and empty gateway restored")
            try check(await services.stopGateway() && services.gatewayState == .stopped,"ordinary stop uses real atomic drain")
            try check(try FileManager.default.contentsOfDirectory(atPath:AppPaths.authDirectory.path).isEmpty,"auth remains empty throughout configuration checks")
            await services.shutdown();try check(services.didFinishShutdown,"all real owned helpers stopped")
        } catch {await services.shutdown();throw error}
        print("CENTRAL_REAL_EMPTY_AUTH_PASS")
    }

}
