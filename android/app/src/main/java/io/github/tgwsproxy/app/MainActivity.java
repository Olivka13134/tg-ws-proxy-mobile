package io.github.tgwsproxy.app;

import android.Manifest;
import android.annotation.SuppressLint;
import android.app.Activity;
import android.content.ActivityNotFoundException;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.PowerManager;
import android.provider.Settings;
import android.view.View;
import android.view.ViewGroup;
import android.view.WindowInsets;
import android.webkit.JavascriptInterface;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.widget.TextView;
import android.widget.Toast;

import io.github.tgwsproxy.tgws.Tgws;

/**
 * The whole UI is the settings page served by the Go core on 127.0.0.1
 * (same page as on Windows); this activity hosts it in a WebView and gives
 * it a small bridge for things only the phone can do.
 */
public class MainActivity extends Activity {
    private WebView web;
    private FrameLayout root;

    @SuppressLint({"SetJavaScriptEnabled", "AddJavascriptInterface"})
    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        root = new FrameLayout(this);
        setContentView(root);
        applyInsets(root);

        String err = App.initCore(this);
        String url = null;
        if (err == null) {
            try {
                url = Tgws.settingsURL();
            } catch (Exception e) {
                err = e.getMessage();
            }
        }
        if (url == null) {
            TextView tv = new TextView(this);
            tv.setPadding(48, 48, 48, 48);
            tv.setText(getString(R.string.init_error) + "\n\n" + err);
            root.addView(tv);
            return;
        }

        web = new WebView(this);
        web.setBackgroundColor(Color.TRANSPARENT);
        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setAllowFileAccess(false);
        s.setAllowContentAccess(false);
        s.setSupportZoom(false);
        web.addJavascriptInterface(new Bridge(), "TgWsNative");
        final String home = url;
        web.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest req) {
                Uri u = req.getUrl();
                if (u.toString().startsWith(home)) return false;
                openExternal(u);
                return true;
            }
        });
        root.addView(web, new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
        web.loadUrl(url);

        if (Build.VERSION.SDK_INT >= 33
                && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, 1);
        }
    }

    private void applyInsets(View v) {
        v.setOnApplyWindowInsetsListener((view, insets) -> {
            int l, t, r, b;
            if (Build.VERSION.SDK_INT >= 30) {
                android.graphics.Insets i = insets.getInsets(
                        WindowInsets.Type.systemBars() | WindowInsets.Type.ime() | WindowInsets.Type.displayCutout());
                l = i.left; t = i.top; r = i.right; b = i.bottom;
            } else {
                l = insets.getSystemWindowInsetLeft();
                t = insets.getSystemWindowInsetTop();
                r = insets.getSystemWindowInsetRight();
                b = insets.getSystemWindowInsetBottom();
            }
            view.setPadding(l, t, r, b);
            return insets;
        });
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (web != null) web.evaluateJavascript("typeof poll==='function'&&poll()", null);
    }

    @Override
    protected void onDestroy() {
        if (web != null) {
            web.destroy();
            web = null;
        }
        super.onDestroy();
    }

    private void openExternal(Uri u) {
        try {
            startActivity(new Intent(Intent.ACTION_VIEW, u));
        } catch (ActivityNotFoundException e) {
            Toast.makeText(this, R.string.no_telegram, Toast.LENGTH_LONG).show();
        }
    }

    private boolean batteryRestricted() {
        if (Build.VERSION.SDK_INT < 23) return false;
        PowerManager pm = getSystemService(PowerManager.class);
        return pm != null && !pm.isIgnoringBatteryOptimizations(getPackageName());
    }

    /** Called from the settings page (window.TgWsNative). Runs on a binder thread. */
    private class Bridge {
        @JavascriptInterface
        public void open(String link) {
            runOnUiThread(() -> openExternal(Uri.parse(link)));
        }

        @JavascriptInterface
        public void copy(String text) {
            runOnUiThread(() -> {
                ClipboardManager cm = getSystemService(ClipboardManager.class);
                if (cm != null) cm.setPrimaryClip(ClipData.newPlainText("tg proxy", text));
            });
        }

        @JavascriptInterface
        public void setRunning(boolean on) {
            runOnUiThread(() -> {
                if (on) ProxyService.start(MainActivity.this);
                else ProxyService.stop(MainActivity.this);
            });
        }

        @JavascriptInterface
        public boolean batteryRestricted() {
            return MainActivity.this.batteryRestricted();
        }

        @JavascriptInterface
        @SuppressLint("BatteryLife")
        public void fixBattery() {
            runOnUiThread(() -> {
                try {
                    startActivity(new Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                            Uri.parse("package:" + getPackageName())));
                } catch (Exception e) {
                    try {
                        startActivity(new Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS));
                    } catch (Exception ignored) {
                    }
                }
            });
        }
    }
}
