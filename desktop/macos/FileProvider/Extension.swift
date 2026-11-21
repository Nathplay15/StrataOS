// File Provider extension: the shared folders of a NAS in Finder (macOS 11+,
// replicated model: the system keeps the downloaded files and offers
// "Download Now" / "Remove Download"). The data comes from the agent of the
// desktop app; this extension holds no token of the NAS.
//
// Identifiers are the paths ("Share/folder/file"), the root container is
// "". A rename or move therefore gives the item a new identifier: it shows
// as removed and added (the change log of the agent says so).

import FileProvider
import Foundation
import UniformTypeIdentifiers

final class Item: NSObject, NSFileProviderItem {
    let remote: RemoteItem

    init(_ r: RemoteItem) { remote = r }

    static func identifier(_ path: String) -> NSFileProviderItemIdentifier {
        path.isEmpty ? .rootContainer : NSFileProviderItemIdentifier(path)
    }

    static func path(_ id: NSFileProviderItemIdentifier) -> String? {
        if id == .rootContainer { return "" }
        if id == .workingSet || id == .trashContainer { return nil }
        return id.rawValue
    }

    var itemIdentifier: NSFileProviderItemIdentifier { Item.identifier(remote.path) }
    var parentItemIdentifier: NSFileProviderItemIdentifier { Item.identifier(remote.parentPath) }
    var filename: String { remote.path.isEmpty ? "StrataOS" : remote.name }
    var contentType: UTType {
        remote.isDir ? .folder : (UTType(filenameExtension: (remote.name as NSString).pathExtension) ?? .data)
    }
    var documentSize: NSNumber? { remote.isDir ? nil : NSNumber(value: remote.size) }
    var contentModificationDate: Date? { remote.modified }
    var creationDate: Date? { remote.modified }
    var itemVersion: NSFileProviderItemVersion {
        NSFileProviderItemVersion(contentVersion: remote.contentVersion, metadataVersion: remote.metadataVersion)
    }
    var capabilities: NSFileProviderItemCapabilities {
        var c: NSFileProviderItemCapabilities = [.allowsReading]
        if remote.isDir { c.insert(.allowsContentEnumerating) }
        // The top folders are the shared folders: not renamed nor deleted here.
        if remote.writable && remote.path.contains("/") {
            c.formUnion([.allowsWriting, .allowsRenaming, .allowsReparenting, .allowsDeleting])
        }
        if remote.writable && remote.isDir { c.insert(.allowsAddingSubItems) }
        return c
    }
}

@objc(StrataOSFileProviderExtension)
final class FileProviderExtension: NSObject, NSFileProviderReplicatedExtension {
    let domain: NSFileProviderDomain
    let agent: AgentClient

    required init(domain: NSFileProviderDomain) {
        self.domain = domain
        agent = AgentClient(account: domain.identifier.rawValue)
        super.init()
    }

    func invalidate() {}

    private func progress(_ t: URLSessionTask?) -> Progress {
        let p = Progress(totalUnitCount: 1)
        if let t = t {
            p.addChild(t.progress, withPendingUnitCount: 1)
            p.cancellationHandler = { t.cancel() }
        }
        return p
    }

    func item(for identifier: NSFileProviderItemIdentifier, request: NSFileProviderRequest,
              completionHandler: @escaping (NSFileProviderItem?, Error?) -> Void) -> Progress {
        guard let p = Item.path(identifier) else {
            completionHandler(nil, NSFileProviderError(.noSuchItem))
            return Progress()
        }
        if p.isEmpty {
            completionHandler(Item(RemoteItem(path: "", name: "", dir: true, size: 0, mtime: 0, version: nil, writable: false, parent: nil)), nil)
            return Progress()
        }
        return progress(agent.call("GET", "item", ["path": p], as: RemoteItem.self) { r in
            switch r {
            case .success(let it): completionHandler(Item(it), nil)
            case .failure(let e): completionHandler(nil, e)
            }
        })
    }

    func fetchContents(for itemIdentifier: NSFileProviderItemIdentifier, version requestedVersion: NSFileProviderItemVersion?,
                       request: NSFileProviderRequest, completionHandler: @escaping (URL?, NSFileProviderItem?, Error?) -> Void) -> Progress {
        guard let p = Item.path(itemIdentifier), !p.isEmpty else {
            completionHandler(nil, nil, NSFileProviderError(.noSuchItem))
            return Progress()
        }
        let v = requestedVersion.flatMap { String(data: $0.contentVersion, encoding: .utf8) }
        let agent = self.agent
        return progress(agent.download(path: p, version: v) { r in
            switch r {
            case .failure(let e): completionHandler(nil, nil, e)
            case .success(let url):
                _ = agent.call("GET", "item", ["path": p], as: RemoteItem.self) { ir in
                    switch ir {
                    case .success(let it): completionHandler(url, Item(it), nil)
                    case .failure(let e): completionHandler(nil, nil, e)
                    }
                }
            }
        })
    }

    func createItem(basedOn itemTemplate: NSFileProviderItem, fields: NSFileProviderItemFields, contents url: URL?,
                    options: NSFileProviderCreateItemOptions = [], request: NSFileProviderRequest,
                    completionHandler: @escaping (NSFileProviderItem?, NSFileProviderItemFields, Bool, Error?) -> Void) -> Progress {
        guard let parent = Item.path(itemTemplate.parentItemIdentifier),
              let p = ItemPath.join(parent, itemTemplate.filename) else {
            completionHandler(nil, [], false, CocoaError(.fileWriteInvalidFileName))
            return Progress()
        }
        let done: (Result<RemoteItem, Error>) -> Void = { r in
            switch r {
            case .success(let it): completionHandler(Item(it), [], false, nil)
            case .failure(let e): completionHandler(nil, [], false, e)
            }
        }
        if itemTemplate.contentType == .folder {
            return progress(agent.call("POST", "mkdir", ["path": p], as: RemoteItem.self, done: done))
        }
        if itemTemplate.contentType == .symbolicLink {
            completionHandler(nil, [], false, CocoaError(.featureUnsupported))
            return Progress()
        }
        guard let url = url else {
            completionHandler(nil, [], false, CocoaError(.fileReadNoSuchFile))
            return Progress()
        }
        let mtime = itemTemplate.contentModificationDate ?? nil
        return progress(agent.upload(path: p, file: url, ifMatch: "", mtime: mtime, done: done))
    }

    func modifyItem(_ item: NSFileProviderItem, baseVersion version: NSFileProviderItemVersion, changedFields: NSFileProviderItemFields,
                    contents newContents: URL?, options: NSFileProviderModifyItemOptions = [], request: NSFileProviderRequest,
                    completionHandler: @escaping (NSFileProviderItem?, NSFileProviderItemFields, Bool, Error?) -> Void) -> Progress {
        guard let old = Item.path(item.itemIdentifier), !old.isEmpty else {
            completionHandler(nil, [], false, NSFileProviderError(.noSuchItem))
            return Progress()
        }
        let agent = self.agent
        let p = Progress(totalUnitCount: 2)
        let fail: (Error) -> Void = { completionHandler(nil, [], false, $0) }
        // Move or rename first, then the contents at the new place.
        let moved: (String) -> Void = { path in
            guard changedFields.contains(.contents), let url = newContents else {
                _ = agent.call("GET", "item", ["path": path], as: RemoteItem.self) { r in
                    switch r {
                    case .success(let it): completionHandler(Item(it), [], false, nil)
                    case .failure(let e): fail(e)
                    }
                }
                return
            }
            let ifMatch = String(data: version.contentVersion, encoding: .utf8) ?? ""
            let mtime = item.contentModificationDate ?? nil
            if let t = agent.upload(path: path, file: url, ifMatch: ifMatch, mtime: mtime, done: { r in
                switch r {
                case .success(let it): completionHandler(Item(it), [], false, nil)
                case .failure(let e): fail(e)
                }
            }) {
                p.addChild(t.progress, withPendingUnitCount: 1)
            }
        }
        if changedFields.contains(.filename) || changedFields.contains(.parentItemIdentifier) {
            guard let parent = Item.path(item.parentItemIdentifier), let to = ItemPath.join(parent, item.filename) else {
                fail(CocoaError(.fileWriteInvalidFileName))
                return p
            }
            if to != old {
                _ = agent.call("POST", "move", ["path": old, "to": to], as: RemoteItem.self) { r in
                    switch r {
                    case .success(let it): moved(it.path)
                    case .failure(let e): fail(e)
                    }
                }
                return p
            }
        }
        moved(old)
        return p
    }

    func deleteItem(identifier: NSFileProviderItemIdentifier, baseVersion version: NSFileProviderItemVersion,
                    options: NSFileProviderDeleteItemOptions = [], request: NSFileProviderRequest,
                    completionHandler: @escaping (Error?) -> Void) -> Progress {
        guard let p = Item.path(identifier), !p.isEmpty else {
            completionHandler(NSFileProviderError(.noSuchItem))
            return Progress()
        }
        var q = ["path": p]
        if options.contains(.recursive) { q["recursive"] = "1" }
        struct OK: Decodable {}
        return progress(agent.call("POST", "delete", q, as: OK.self) { r in
            if case .failure(let e) = r { completionHandler(e) } else { completionHandler(nil) }
        })
    }

    func enumerator(for containerItemIdentifier: NSFileProviderItemIdentifier, request: NSFileProviderRequest) throws -> NSFileProviderEnumerator {
        if containerItemIdentifier == .workingSet {
            return Enumerator(agent: agent, container: nil)
        }
        guard let p = Item.path(containerItemIdentifier) else { throw NSFileProviderError(.noSuchItem) }
        return Enumerator(agent: agent, container: p)
    }
}

/// Lists a folder (one page: the agent answers whole folders) and reports
/// the changes after an anchor of the agent's change log. The working set
/// (container nil) lists nothing at first and reports every change.
final class Enumerator: NSObject, NSFileProviderEnumerator {
    let agent: AgentClient
    let container: String?

    init(agent: AgentClient, container: String?) {
        self.agent = agent
        self.container = container
    }

    func invalidate() {}

    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        guard let c = container else {
            observer.finishEnumerating(upTo: nil)
            return
        }
        _ = agent.call("GET", "list", ["path": c], as: ListAnswer.self) { r in
            switch r {
            case .success(let l):
                observer.didEnumerate(l.items.map { Item($0) })
                observer.finishEnumerating(upTo: nil)
            case .failure(let e):
                observer.finishEnumeratingWithError(e)
            }
        }
    }

    func enumerateChanges(for observer: NSFileProviderChangeObserver, from anchor: NSFileProviderSyncAnchor) {
        let a = String(data: anchor.rawValue, encoding: .utf8) ?? ""
        _ = agent.call("GET", "changes", ["anchor": a], as: ChangeAnswer.self) { [container] r in
            switch r {
            case .failure(let e):
                observer.finishEnumeratingWithError(e)
            case .success(let ch):
                if ch.expired == true {
                    observer.finishEnumeratingWithError(NSFileProviderError(.syncAnchorExpired))
                    return
                }
                let s = ChangeFilter.split(ch.changes ?? [], container: container)
                if !s.updated.isEmpty { observer.didUpdate(s.updated.map { Item($0) }) }
                if !s.deleted.isEmpty { observer.didDeleteItems(withIdentifiers: s.deleted.map { Item.identifier($0) }) }
                observer.finishEnumeratingChanges(upTo: NSFileProviderSyncAnchor(Data(ch.anchor.utf8)), moreComing: false)
            }
        }
    }

    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        struct A: Decodable { var anchor: String }
        _ = agent.call("GET", "anchor", as: A.self) { r in
            if case .success(let a) = r {
                completionHandler(NSFileProviderSyncAnchor(Data(a.anchor.utf8)))
            } else {
                completionHandler(nil)
            }
        }
    }
}
