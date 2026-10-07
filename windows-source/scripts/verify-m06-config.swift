import Foundation
import Security
import Darwin

@main struct ConfigChecks {
    static func main() throws {
        let root = AppPaths.dataDirectory
        precondition(root.path.hasPrefix("/private/tmp/"))
        let fm = FileManager.default
        try fm.createDirectory(at: root, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let url = AppPaths.gatewayConfigFile
        func check(_ value: Bool, _ message: String) { precondition(value, message); print("PASS \(message)") }
        func write(_ data: Data) throws { try data.write(to: url); try fm.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path) }
        func reject(_ name: String) throws {
            let before = try? Data(contentsOf: url)
            do { _ = try GatewayConfigFile.ensureInitial(port: 19082); preconditionFailure(name) }
            catch { check(error is GatewayConfigFile.Failure, name) }
            check((try? Data(contentsOf: url)) == before, "\(name) preserves bytes")
        }
        let initial = try GatewayConfigFile.ensureInitial(port: 19081)
        check(initial.port == 19081 && initial.host == "127.0.0.1" && !initial.allowLAN && initial.apiKey.count == 71,
              "initial loopback config and random 32-byte key")
        let initialDocument = try JSONSerialization.jsonObject(with: Data(contentsOf: url)) as! [String: Any]
        let initialSchedule = initialDocument["schedule"] as! [String: Any]
        check(initialSchedule["checkin_enabled"] as? Bool == true && (initialSchedule["checkin_hours"] as? [Int]) == [9, 21], "new desktop config enables scheduled check-in at default hours")
        for name in ["travel_enabled", "activity_enabled", "keepalive_enabled", "school_enabled", "cat_enabled"] {
            check(initialSchedule[name] as? Bool == false, "other schedule remains disabled: \(name)")
        }
        check(!fm.fileExists(atPath: root.appendingPathComponent("state.json").path), "first run allows absent state file")
        check((try fm.attributesOfItem(atPath: url.path)[.posixPermissions] as? NSNumber)?.intValue == 0o600, "initial file permission 0600")
        let base = try Data(contentsOf: url)
        let old = String(decoding: base, as: UTF8.self)
        let preserved = Data(old.replacingOccurrences(of: "{", with: "{\n\"huge_unknown\": 18446744073709551617,\n", range: old.startIndex..<old.index(after: old.startIndex)).utf8)
        try write(preserved)
        let snapshot = try GatewayConfigFile.write(port: 19555, apiKey: "ignored-stale-key")
        check(snapshot.port == 19081 && snapshot.apiKey == initial.apiKey, "startup uses committed port and key")
        check(try Data(contentsOf: url) == preserved, "unknown and large integer exact bytes survive startup")
        let large = Data("{\"large_unknown\":\"".utf8) + Data(repeating: 0x78, count: 4 * 1024 * 1024) + Data("\",".utf8) + preserved.dropFirst()
        check(large.count > 4 * 1024 * 1024 && large.count <= 10 * 1024 * 1024, "large valid fixture exceeds old 4 MiB limit")
        try write(large)
        let largeSnapshot = try GatewayConfigFile.write(port: 19555, apiKey: "ignored-stale-key")
        check(largeSnapshot.port == initial.port && largeSnapshot.apiKey == initial.apiKey, "large unknown-field config loads without replacing committed values")
        check(try Data(contentsOf: url) == large, "over 4 MiB configuration preserves every byte")
        try write(preserved + Data(repeating: 32, count: 10 * 1024 * 1024 + 1 - preserved.count))
        try reject("over 10 MiB configuration")
        try write(preserved)
        let rev = snapshot.revision
        check(try GatewayConfigFile.readSnapshot().revision == rev && rev.count == 64, "revision hashes exact committed file")
        do { _ = try GatewayConfigFile.randomAPIKey { _ in errSecNotAvailable }; preconditionFailure("random failure") }
        catch { check((error as? GatewayConfigFile.Failure) == .randomUnavailable, "random failure never returns zero key") }
        check(try Data(contentsOf: url) == preserved, "random failure leaves config unchanged")
        let document = try JSONSerialization.jsonObject(with: base) as! [String: Any]
        var lan = document; lan["listen"] = "0.0.0.0:19081"
        let lanData = try JSONSerialization.data(withJSONObject: lan)
        try write(lanData)
        let lanSnapshot = try GatewayConfigFile.readSnapshot()
        check(lanSnapshot.port == 19081 && lanSnapshot.host == "0.0.0.0" && lanSnapshot.allowLAN,
              "committed wildcard listen enables LAN without a second stored flag")
        check(try Data(contentsOf: url) == lanData, "LAN snapshot preserves committed bytes")
        try write(preserved)
        let changes: [(String, Any)] = [("listen", ":19081"), ("listen", "127.0.0.1:019081"), ("listen", "127.0.0.1:1023"),
            ("listen", "0.0.0.0:019081"), ("listen", "192.168.1.5:19081"), ("listen", "[::]:19081"),
            ("api_key", NSNull()), ("auth_dir", "auths"), ("state_file", "/private/tmp/escape.json"),
            ("schedule", ["checkin_enabled": false]), ("admin", ["enabled": true]), ("admin", ["enabled": 0]), ("LISTEN", "0.0.0.0:19081")]
        for (field, value) in changes {
            var bad = document; bad[field] = value
            try write(JSONSerialization.data(withJSONObject: bad)); try reject("invalid \(field)")
        }
        var blankKey = document; blankKey["api_key"] = " \n"
        let blankKeyData = try JSONSerialization.data(withJSONObject: blankKey)
        try write(blankKeyData)
        check(try GatewayConfigFile.readSnapshot().apiKey == " \n", "blank key remains readable for controlled rotation")
        check(try Data(contentsOf: url) == blankKeyData, "blank key read preserves exact file")
        for hours: [Any] in [[-1], [24], [9, 9], [true], [9.5]] {
            var bad = document; var schedule = initialSchedule; schedule["checkin_hours"] = hours; bad["schedule"] = schedule
            try write(JSONSerialization.data(withJSONObject: bad)); try reject("invalid check-in hours")
        }
        var legacy = document; var disabledSchedule = initialSchedule
        disabledSchedule["checkin_enabled"] = false; disabledSchedule.removeValue(forKey: "checkin_hours")
        legacy["schedule"] = disabledSchedule
        try write(JSONSerialization.data(withJSONObject: legacy))
        check(try GatewayConfigFile.readSnapshot().port == initial.port, "existing disabled check-in without hours remains readable")
        try write(preserved)
        for raw in ["invalid json", old.replacingOccurrences(of: "{", with: "{\"api_key\":\"synthetic\",", range: old.startIndex..<old.index(after: old.startIndex)),
                    old.replacingOccurrences(of: "{", with: "{\"nested\":{\"duplicate\":1,\"duplicate\":2},", range: old.startIndex..<old.index(after: old.startIndex))] {
            try write(Data(raw.utf8)); try reject("malformed or duplicate JSON")
        }
        try write(Data(old.utf16.map { UInt8(truncatingIfNeeded: $0) }.flatMap { [$0, 0] })); try reject("non-UTF8 JSON")
        try write(Data([0xEF, 0xBB, 0xBF]) + base); try reject("UTF8 BOM JSON")
        try write(preserved)
        try fm.setAttributes([.posixPermissions: 0o644], ofItemAtPath: url.path); try reject("unsafe config permission")
        try fm.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
        for name in ["auths", "state.json"] {
            let target = root.appendingPathComponent(name)
            try fm.createSymbolicLink(at: target, withDestinationURL: url); try reject("symlink \(name)"); try fm.removeItem(at: target)
        }
        let saved = root.appendingPathComponent("saved.json")
        try fm.moveItem(at: url, to: saved); try fm.createSymbolicLink(at: url, withDestinationURL: saved)
        try reject("symlink config"); try fm.removeItem(at: url); try fm.moveItem(at: saved, to: url)
        try fm.setAttributes([.posixPermissions: 0o755], ofItemAtPath: root.path); try reject("unsafe root permission")
        try fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: root.path)
        check(try Data(contentsOf: url) == preserved, "all failures preserve exact old file")
        check(try fm.contentsOfDirectory(atPath: root.path) == ["gateway-config.json"], "no backup or temporary residue")
        print("M06_CONFIG_CORE_PASS")
    }
}
