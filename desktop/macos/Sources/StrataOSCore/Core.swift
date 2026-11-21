// Logic shared by the File Provider extension, the Finder Sync extension
// and the helper, without Apple-only frameworks so that `swift test` runs
// it anywhere. The extensions are compiled together with these files (one
// module each, see build.sh); the Swift package only exists for the tests.

import Foundation

/// agent.json, written by the agent (through the helper) in the app group
/// folder: where the agent listens and the token of the extensions.
struct AgentConfig: Codable, Equatable {
    var port: Int
    var token: String
    var folders: [String]?

    static func load(from data: Data) -> AgentConfig? {
        guard let c = try? JSONDecoder().decode(AgentConfig.self, from: data),
              c.port > 0, c.port < 65536, !c.token.isEmpty else { return nil }
        return c
    }

    /// The address of a route of the File Provider API of the agent.
    func url(account: String, route: String, query: [String: String] = [:]) -> URL? {
        var c = URLComponents()
        c.scheme = "http"
        c.host = "127.0.0.1"
        c.port = port
        c.path = "/v1/fp/" + account + "/" + route
        if !query.isEmpty {
            c.queryItems = query.keys.sorted().map { URLQueryItem(name: $0, value: query[$0]) }
            // "+" stays literal in queries: the agent would read a space.
            c.percentEncodedQuery = c.percentEncodedQuery?.replacingOccurrences(of: "+", with: "%2B")
        }
        return c.url
    }
}

/// An item as the agent answers it (`fpItem` in internal/agent/ondemand.go).
struct RemoteItem: Codable, Equatable {
    var path: String
    var name: String
    var dir: Bool?
    var size: Int64
    var mtime: Int64 // ms
    var version: String?
    var writable: Bool
    var parent: String?

    var isDir: Bool { dir ?? false }
    var parentPath: String { parent ?? ItemPath.parent(of: path) }
    var modified: Date { Date(timeIntervalSince1970: Double(mtime) / 1000) }

    /// Version for the system: contents change with the NAS version, the
    /// metadata with the date too.
    var contentVersion: Data { Data((isDir ? "dir" : (version ?? "")).utf8) }
    var metadataVersion: Data { Data(((version ?? "") + ":\(mtime):\(writable)").utf8) }
}

struct ListAnswer: Codable {
    var items: [RemoteItem]
    var anchor: String
}

struct ChangeAnswer: Codable {
    struct Change: Codable, Equatable {
        var path: String
        var removed: Bool?
        var item: RemoteItem?
    }
    var anchor: String
    var expired: Bool?
    var changes: [Change]?
}

/// Item identifiers are the paths "Share/folder/file"; "" is the root.
enum ItemPath {
    static func parent(of p: String) -> String {
        guard let i = p.lastIndex(of: "/") else { return "" }
        return String(p[..<i])
    }

    static func join(_ parent: String, _ name: String) -> String? {
        guard valid(name) else { return nil }
        return parent.isEmpty ? name : parent + "/" + name
    }

    static func valid(_ name: String) -> Bool {
        !name.isEmpty && name != "." && name != ".." && !name.contains("/") && !name.contains("\0")
    }

    static func isUnder(_ p: String, _ dir: String) -> Bool {
        dir.isEmpty || p == dir || p.hasPrefix(dir + "/")
    }
}

/// What a change enumerator reports for a container ("" root; nil: the
/// working set, everything).
enum ChangeFilter {
    static func split(_ changes: [ChangeAnswer.Change], container: String?) -> (updated: [RemoteItem], deleted: [String]) {
        var up: [RemoteItem] = []
        var del: [String] = []
        for c in changes {
            if let container = container, ItemPath.parent(of: c.path) != container {
                continue
            }
            if c.removed == true || c.item == nil {
                del.append(c.path)
            } else if let it = c.item {
                up.append(it)
            }
        }
        return (up, del)
    }
}

/// The links the Finder menu opens: the app does the work (share links
/// need the window; the extension has no token for the NAS).
enum DeepLink {
    static func url(_ action: String, path: String) -> URL? {
        var c = URLComponents()
        c.scheme = "strataos"
        c.host = action
        c.queryItems = [URLQueryItem(name: "path", value: path)]
        c.percentEncodedQuery = c.percentEncodedQuery?.replacingOccurrences(of: "+", with: "%2B")
        return c.url
    }

    /// Whether a file is inside one of the folders (synchronized or on demand).
    static func inside(_ path: String, folders: [String]) -> Bool {
        folders.contains { f in
            let d = f.hasSuffix("/") ? String(f.dropLast()) : f
            return !d.isEmpty && (path == d || path.hasPrefix(d + "/"))
        }
    }
}

/// HTTP status of the agent → what the extension reports.
enum AgentFailure: Equatable {
    case noSuchItem, readOnly, conflict, offline, other

    static func of(status: Int) -> AgentFailure {
        switch status {
        case 404: return .noSuchItem
        case 403: return .readOnly
        case 409: return .conflict
        case 502, 503: return .offline
        default: return .other
        }
    }
}
