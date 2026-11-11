import Foundation
import XCTest
@testable import StrataOSCore

final class CoreTests: XCTestCase {
    func testAgentConfig() throws {
        let c = try XCTUnwrap(AgentConfig.load(from: Data(#"{"port":4321,"token":"abc","folders":["/Users/a/Sync"]}"#.utf8)))
        XCTAssertEqual(c.folders, ["/Users/a/Sync"])
        XCTAssertNil(AgentConfig.load(from: Data(#"{"port":0,"token":"abc"}"#.utf8)))
        XCTAssertNil(AgentConfig.load(from: Data(#"{"port":80,"token":""}"#.utf8)))
        let u = try XCTUnwrap(c.url(account: "main", route: "list", query: ["path": "Docs/a b+c&d.txt"]))
        XCTAssertEqual(u.absoluteString, "http://127.0.0.1:4321/v1/fp/main/list?path=Docs/a%20b%2Bc%26d.txt")
    }

    func testRemoteItem() throws {
        let json = #"{"path":"Docs/a.txt","name":"a.txt","size":5,"mtime":1700000000000,"version":"v1","writable":true,"parent":"Docs"}"#
        let it = try JSONDecoder().decode(RemoteItem.self, from: Data(json.utf8))
        XCTAssertFalse(it.isDir)
        XCTAssertEqual(it.parentPath, "Docs")
        XCTAssertEqual(it.contentVersion, Data("v1".utf8))
        XCTAssertEqual(it.modified.timeIntervalSince1970, 1_700_000_000)
        var d = it
        d.dir = true
        XCTAssertEqual(d.contentVersion, Data("dir".utf8))
        XCTAssertNotEqual(it.metadataVersion, { var x = it; x.mtime += 1; return x.metadataVersion }())
    }

    func testPaths() {
        XCTAssertEqual(ItemPath.parent(of: "Docs/a/b.txt"), "Docs/a")
        XCTAssertEqual(ItemPath.parent(of: "Docs"), "")
        XCTAssertEqual(ItemPath.join("", "Docs"), "Docs")
        XCTAssertEqual(ItemPath.join("Docs", "x.txt"), "Docs/x.txt")
        XCTAssertNil(ItemPath.join("Docs", "a/b"))
        XCTAssertNil(ItemPath.join("Docs", ".."))
        XCTAssertTrue(ItemPath.isUnder("Docs/a", "Docs"))
        XCTAssertFalse(ItemPath.isUnder("Docs2/a", "Docs"))
    }

    func testChangeFilter() {
        let item = RemoteItem(path: "Docs/a", name: "a", dir: nil, size: 1, mtime: 0, version: "v", writable: true, parent: "Docs")
        let ch = [
            ChangeAnswer.Change(path: "Docs/a", removed: nil, item: item),
            ChangeAnswer.Change(path: "Docs/sub/b", removed: true, item: nil),
            ChangeAnswer.Change(path: "Docs/c", removed: true, item: nil),
        ]
        let all = ChangeFilter.split(ch, container: nil)
        XCTAssertEqual(all.updated.count, 1)
        XCTAssertEqual(all.deleted, ["Docs/sub/b", "Docs/c"])
        let docs = ChangeFilter.split(ch, container: "Docs")
        XCTAssertEqual(docs.deleted, ["Docs/c"])
        XCTAssertTrue(ChangeFilter.split(ch, container: "").updated.isEmpty)
    }

    func testDeepLinks() throws {
        let u = try XCTUnwrap(DeepLink.url("share-link", path: "/Users/a/Sync/r&d+1.pdf"))
        XCTAssertEqual(u.absoluteString, "strataos://share-link?path=/Users/a/Sync/r%26d%2B1.pdf")
        XCTAssertTrue(DeepLink.inside("/Users/a/Sync/x", folders: ["/Users/a/Sync/"]))
        XCTAssertFalse(DeepLink.inside("/Users/a/Sync2/x", folders: ["/Users/a/Sync"]))
        XCTAssertFalse(DeepLink.inside("/x", folders: [""]))
    }

    func testFailures() {
        XCTAssertEqual(AgentFailure.of(status: 404), .noSuchItem)
        XCTAssertEqual(AgentFailure.of(status: 403), .readOnly)
        XCTAssertEqual(AgentFailure.of(status: 503), .offline)
    }
}
