import SwiftUI

@main
struct TgWsProxyApp: App {
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var proxy = ProxyController.shared

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(proxy)
        }
        .onChange(of: scenePhase) { phase in
            if phase == .active {
                proxy.appBecameActive()
            }
        }
    }
}
