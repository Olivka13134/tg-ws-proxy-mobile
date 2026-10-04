import Foundation
import Tgws

/// Owns the Go proxy core (Tgws.xcframework) and the background keep-alive.
final class ProxyController: ObservableObject {
    static let shared = ProxyController()

    @Published private(set) var settingsURL: URL?
    @Published private(set) var initError: String?

    private let keepAlive = BackgroundKeepAlive()
    private let wantRunningKey = "wantRunning"

    private init() {
        let dir = Self.dataDirectory()
        let lang = (Locale.preferredLanguages.first ?? "en").hasPrefix("ru") ? "ru" : "en"
        var err: NSError?
        _ = TgwsInit(dir, lang, &err)
        if let err = err {
            initError = err.localizedDescription
            return
        }
        var uerr: NSError?
        let url = TgwsSettingsURL(&uerr)
        if let uerr = uerr {
            initError = uerr.localizedDescription
            return
        }
        settingsURL = URL(string: url)
        keepAlive.onResume = { [weak self] in self?.ensureRunningIfWanted() }

        // The user had it on last time: turn it back on.
        if UserDefaults.standard.bool(forKey: wantRunningKey) {
            start()
        }
    }

    var isRunning: Bool { TgwsIsRunning() }
    var link: String { TgwsLink() }

    @discardableResult
    func start() -> String? {
        UserDefaults.standard.set(true, forKey: wantRunningKey)
        var err: NSError?
        _ = TgwsStartProxy(&err)
        if let err = err {
            return err.localizedDescription
        }
        keepAlive.start()
        return nil
    }

    func stop() {
        UserDefaults.standard.set(false, forKey: wantRunningKey)
        TgwsStopProxy()
        keepAlive.stop()
    }

    /// Back in the foreground: if iOS suspended us anyway (call, another
    /// app took audio), bring the proxy and the keep-alive back.
    func appBecameActive() {
        ensureRunningIfWanted()
    }

    private func ensureRunningIfWanted() {
        guard UserDefaults.standard.bool(forKey: wantRunningKey) else { return }
        var err: NSError?
        _ = TgwsStartProxy(&err) // no-op if already running
        keepAlive.start()
    }

    private static func dataDirectory() -> String {
        let fm = FileManager.default
        let base = (try? fm.url(for: .applicationSupportDirectory, in: .userDomainMask,
                                appropriateFor: nil, create: true))
            ?? fm.temporaryDirectory
        let dir = base.appendingPathComponent("TgWsProxyGo", isDirectory: true)
        try? fm.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.path
    }
}
