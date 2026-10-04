package io.github.tgwsproxy.app;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;

import io.github.tgwsproxy.tgws.Tgws;

/** Starts the proxy after a reboot or app update if "start with the phone" is on. */
public class BootReceiver extends BroadcastReceiver {
    @Override
    public void onReceive(Context context, Intent intent) {
        String a = intent.getAction();
        if (!Intent.ACTION_BOOT_COMPLETED.equals(a) && !Intent.ACTION_MY_PACKAGE_REPLACED.equals(a)) {
            return;
        }
        try {
            if (Tgws.autostartEnabled(App.dataDir(context))) {
                ProxyService.start(context);
            }
        } catch (Throwable ignored) {
            // never crash on boot
        }
    }
}
