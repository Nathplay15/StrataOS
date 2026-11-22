// The agent's File Provider API (desktop/internal/agent/ondemand.go) as the
// extension calls it: agent.json in the app group folder gives the port
// and the token; the domain identifier is the account.

import FileProvider
import Foundation

final class AgentClient {
    let account: String
    private let session: URLSession

    init(account: String) {
        self.account = account
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 120
        cfg.timeoutIntervalForResource = 24 * 3600 // large files
        cfg.connectionProxyDictionary = [:] // 127.0.0.1 only, never a proxy
        session = URLSession(configuration: cfg)
    }

    /// Read at each call: the agent writes a new port and token at each launch.
    private func config() -> AgentConfig? {
        guard let dir = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: BuildConfig.appGroup),
              let data = try? Data(contentsOf: dir.appendingPathComponent("agent.json")) else { return nil }
        return AgentConfig.load(from: data)
    }

    private func request(_ method: String, _ route: String, _ query: [String: String]) throws -> URLRequest {
        guard let c = config(), let url = c.url(account: account, route: route, query: query) else {
            throw NSFileProviderError(.serverUnreachable) // the app is not running
        }
        var r = URLRequest(url: url)
        r.httpMethod = method
        r.setValue("Bearer " + c.token, forHTTPHeaderField: "Authorization")
        return r
    }

    static func error(status: Int, body: Data?) -> Error {
        let msg = body.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }?["error"] as? String ?? "HTTP \(status)"
        switch AgentFailure.of(status: status) {
        case .noSuchItem: return NSFileProviderError(.noSuchItem)
        case .readOnly: return CocoaError(.fileWriteNoPermission, userInfo: [NSLocalizedDescriptionKey: msg])
        case .offline: return NSFileProviderError(.serverUnreachable)
        case .conflict, .other:
            return NSError(domain: NSCocoaErrorDomain, code: CocoaError.fileWriteUnknown.rawValue, userInfo: [NSLocalizedDescriptionKey: msg])
        }
    }

    private static func transport(_ e: Error) -> Error {
        (e as? URLError) != nil ? NSFileProviderError(.serverUnreachable) : e
    }

    /// A JSON call.
    func call<T: Decodable>(_ method: String, _ route: String, _ query: [String: String] = [:], as: T.Type,
                            done: @escaping (Result<T, Error>) -> Void) -> URLSessionTask? {
        let req: URLRequest
        do { req = try request(method, route, query) } catch { done(.failure(error)); return nil }
        let t = session.dataTask(with: req) { data, resp, err in
            if let err = err { return done(.failure(Self.transport(err))) }
            let status = (resp as? HTTPURLResponse)?.statusCode ?? 0
            guard status == 200, let data = data else { return done(.failure(Self.error(status: status, body: data))) }
            do { done(.success(try JSONDecoder().decode(T.self, from: data))) } catch { done(.failure(error)) }
        }
        t.resume()
        return t
    }

    /// Downloads a file into a temporary file of the extension.
    func download(path: String, version: String?, done: @escaping (Result<URL, Error>) -> Void) -> URLSessionTask? {
        var q = ["path": path]
        if let v = version, !v.isEmpty { q["version"] = v }
        let req: URLRequest
        do { req = try request("GET", "content", q) } catch { done(.failure(error)); return nil }
        let t = session.downloadTask(with: req) { url, resp, err in
            if let err = err { return done(.failure(Self.transport(err))) }
            let status = (resp as? HTTPURLResponse)?.statusCode ?? 0
            guard status == 200, let url = url else { return done(.failure(Self.error(status: status, body: nil))) }
            // The system wants a file it may move: out of URLSession's folder.
            let dst = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
            do {
                try FileManager.default.moveItem(at: url, to: dst)
                done(.success(dst))
            } catch { done(.failure(error)) }
        }
        t.resume()
        return t
    }

    /// Sends a file (ifMatch: the NAS version replaced, "" a new file).
    func upload(path: String, file: URL, ifMatch: String, mtime: Date?, done: @escaping (Result<RemoteItem, Error>) -> Void) -> URLSessionTask? {
        var q = ["path": path, "ifMatch": ifMatch]
        if let m = mtime { q["mtime"] = String(Int64(m.timeIntervalSince1970 * 1000)) }
        let req: URLRequest
        do { req = try request("PUT", "content", q) } catch { done(.failure(error)); return nil }
        let t = session.uploadTask(with: req, fromFile: file) { data, resp, err in
            if let err = err { return done(.failure(Self.transport(err))) }
            let status = (resp as? HTTPURLResponse)?.statusCode ?? 0
            guard status == 200, let data = data else { return done(.failure(Self.error(status: status, body: data))) }
            do { done(.success(try JSONDecoder().decode(RemoteItem.self, from: data))) } catch { done(.failure(error)) }
        }
        t.resume()
        return t
    }
}
