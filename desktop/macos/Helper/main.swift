// strataos-fileprovider: the agent's hands on the File Provider domains
// (the agent is Go: NSFileProviderManager is easier here). It sits in
// Contents/MacOS of the app, so the system finds the extension of the app.
//
//   strataos-fileprovider add <domain> <display name>   adds (or keeps) the domain, prints its folder
//   strataos-fileprovider remove <domain>               removes it (its downloaded files go)
//   strataos-fileprovider signal <domain>               the agent saw changes on the NAS
//   strataos-fileprovider publish                       writes agent.json (stdin) in the app group folder
//   strataos-fileprovider group-dir                     prints the app group folder

import FileProvider
import Foundation

func fail(_ msg: String) -> Never {
    FileHandle.standardError.write(Data((msg + "\n").utf8))
    exit(1)
}

/// Waits for a completion handler (the command has nothing else to do).
func wait<T>(_ f: (@escaping (T) -> Void) -> Void) -> T {
    let sem = DispatchSemaphore(value: 0)
    var out: T?
    f { v in
        out = v
        sem.signal()
    }
    if sem.wait(timeout: .now() + 60) == .timedOut { fail("timeout") }
    return out!
}

/// Runs a call answering an optional error, and stops on an error.
func check(_ what: String, _ f: (@escaping (Error?) -> Void) -> Void) {
    let e: Error? = wait(f)
    if let e = e { fail(what + ": " + e.localizedDescription) }
}

func domain(_ id: String, _ name: String = "StrataOS") -> NSFileProviderDomain {
    NSFileProviderDomain(identifier: NSFileProviderDomainIdentifier(rawValue: id), displayName: name)
}

func groupDir() -> URL {
    guard let u = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: BuildConfig.appGroup) else {
        fail("no app group folder (" + BuildConfig.appGroup + "): the app is not signed with its entitlements")
    }
    return u
}

let args = CommandLine.arguments
guard args.count >= 2 else { fail("usage: strataos-fileprovider add|remove|signal|publish|group-dir …") }
switch args[1] {
case "add" where args.count == 4:
    let d = domain(args[2], args[3])
    check("add") { done in NSFileProviderManager.add(d) { done($0) } }
    guard let m = NSFileProviderManager(for: d) else { fail("no manager for the domain") }
    let (url, err): (URL?, Error?) = wait { done in m.getUserVisibleURL(for: .rootContainer) { done(($0, $1)) } }
    guard let u = url else { fail("folder: \(err?.localizedDescription ?? "unknown")") }
    print(u.path)
case "remove" where args.count == 3:
    check("remove") { done in NSFileProviderManager.remove(domain(args[2])) { done($0) } }
case "signal" where args.count == 3:
    guard let m = NSFileProviderManager(for: domain(args[2])) else { fail("no such domain") }
    check("signal") { done in m.signalEnumerator(for: .workingSet) { done($0) } }
case "publish":
    let data = FileHandle.standardInput.readDataToEndOfFile()
    guard AgentConfig.load(from: data) != nil else { fail("invalid agent.json") }
    let f = groupDir().appendingPathComponent("agent.json")
    try? FileManager.default.createDirectory(at: groupDir(), withIntermediateDirectories: true)
    let tmp = groupDir().appendingPathComponent(".agent.json.tmp")
    FileManager.default.createFile(atPath: tmp.path, contents: data, attributes: [.posixPermissions: 0o600])
    _ = try? FileManager.default.removeItem(at: f)
    do { try FileManager.default.moveItem(at: tmp, to: f) } catch { fail("publish: \(error.localizedDescription)") }
case "group-dir":
    print(groupDir().path)
default:
    fail("usage: strataos-fileprovider add|remove|signal|publish|group-dir …")
}
