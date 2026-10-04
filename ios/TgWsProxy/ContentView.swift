import SwiftUI
import UIKit
import WebKit

/// The UI is the same settings page as on Windows/Android, served by the Go
/// core on 127.0.0.1 and shown in a web view with a small native bridge.
struct ContentView: View {
    @EnvironmentObject var proxy: ProxyController

    var body: some View {
        Group {
            if let url = proxy.settingsURL {
                SettingsWebView(url: url, proxy: proxy)
            } else {
                VStack(spacing: 12) {
                    Text("TG WS Proxy").font(.title2).bold()
                    Text(proxy.initError ?? "…")
                        .font(.footnote)
                        .foregroundColor(.secondary)
                        .multilineTextAlignment(.center)
                }
                .padding()
            }
        }
        .background(Color(UIColor.systemGroupedBackground).ignoresSafeArea())
    }
}

struct SettingsWebView: UIViewRepresentable {
    let url: URL
    let proxy: ProxyController

    func makeCoordinator() -> Coordinator { Coordinator(proxy: proxy, home: url) }

    func makeUIView(context: Context) -> WKWebView {
        let ucc = WKUserContentController()
        ucc.add(context.coordinator, name: "tgws")
        let cfg = WKWebViewConfiguration()
        cfg.userContentController = ucc
        let web = WKWebView(frame: .zero, configuration: cfg)
        web.isOpaque = false
        web.backgroundColor = .clear
        web.scrollView.backgroundColor = .clear
        web.navigationDelegate = context.coordinator
        context.coordinator.web = web
        web.load(URLRequest(url: url))
        return web
    }

    func updateUIView(_ web: WKWebView, context: Context) {}

    static func dismantleUIView(_ web: WKWebView, coordinator: Coordinator) {
        web.configuration.userContentController.removeScriptMessageHandler(forName: "tgws")
    }

    final class Coordinator: NSObject, WKScriptMessageHandler, WKNavigationDelegate {
        let proxy: ProxyController
        let home: URL
        weak var web: WKWebView?

        init(proxy: ProxyController, home: URL) {
            self.proxy = proxy
            self.home = home
        }

        // window.webkit.messageHandlers.tgws.postMessage({cmd, arg})
        func userContentController(_ ucc: WKUserContentController, didReceive message: WKScriptMessage) {
            guard let body = message.body as? [String: Any],
                  let cmd = body["cmd"] as? String else { return }
            let arg = body["arg"] as? String ?? ""
            switch cmd {
            case "open":
                if let u = URL(string: arg) { UIApplication.shared.open(u) }
            case "copy":
                UIPasteboard.general.string = arg
            case "start":
                if let err = proxy.start() { showError(err) }
            case "stop":
                proxy.stop()
            default:
                break
            }
            web?.evaluateJavaScript("typeof poll==='function'&&poll()")
        }

        // Links that are not the settings page (tg://, https://) open outside.
        func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
                     decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
            if let u = action.request.url, !u.absoluteString.hasPrefix(home.absoluteString) {
                UIApplication.shared.open(u)
                decisionHandler(.cancel)
                return
            }
            decisionHandler(.allow)
        }

        private func showError(_ text: String) {
            let js = "(function(){var m=document.getElementById('err');if(m)m.textContent=\(Self.jsString(text));})()"
            web?.evaluateJavaScript(js)
        }

        private static func jsString(_ s: String) -> String {
            let data = (try? JSONSerialization.data(withJSONObject: [s])) ?? Data("[\"\"]".utf8)
            let arr = String(data: data, encoding: .utf8) ?? "[\"\"]"
            return String(arr.dropFirst().dropLast())
        }
    }
}
