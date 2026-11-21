// Finder Sync extension: "Copy a share link" and "Open on the NAS" in the
// menu of files inside the synchronized folders and the on-demand folders.
// It opens strataos:// links: the app (single instance) does the work, as
// for the Explorer and Nautilus menus.

import Cocoa
import FinderSync

@objc(StrataOSFinderSync)
final class FinderSync: FIFinderSync {
    private var folders: [String] = []

    override init() {
        super.init()
        reload()
        // The agent rewrites agent.json at each launch and when folders change.
        Timer.scheduledTimer(withTimeInterval: 30, repeats: true) { [weak self] _ in self?.reload() }
    }

    private func reload() {
        guard let dir = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: BuildConfig.appGroup),
              let data = try? Data(contentsOf: dir.appendingPathComponent("agent.json")),
              let c = AgentConfig.load(from: data) else { return }
        let l = (c.folders ?? []).filter { !$0.isEmpty }
        if l != folders {
            folders = l
            FIFinderSyncController.default().directoryURLs = Set(l.map { URL(fileURLWithPath: $0) })
        }
    }

    override func menu(for menuKind: FIMenuKind) -> NSMenu {
        let m = NSMenu(title: "")
        guard menuKind == .contextualMenuForItems || menuKind == .toolbarItemMenu else { return m }
        let b = Bundle(for: FinderSync.self)
        m.addItem(withTitle: NSLocalizedString("Copy a share link", bundle: b, comment: ""), action: #selector(shareLink(_:)), keyEquivalent: "")
        m.addItem(withTitle: NSLocalizedString("Open on the NAS", bundle: b, comment: ""), action: #selector(openOnNAS(_:)), keyEquivalent: "")
        return m
    }

    private func selected() -> String? {
        let c = FIFinderSyncController.default()
        let u = c.selectedItemURLs()?.first ?? c.targetedURL()
        guard let p = u?.path, DeepLink.inside(p, folders: folders) else { return nil }
        return p
    }

    @objc func shareLink(_ sender: AnyObject?) { open("share-link") }
    @objc func openOnNAS(_ sender: AnyObject?) { open("open-on-nas") }

    private func open(_ action: String) {
        guard let p = selected(), let url = DeepLink.url(action, path: p) else { return }
        NSWorkspace.shared.open(url)
    }
}
