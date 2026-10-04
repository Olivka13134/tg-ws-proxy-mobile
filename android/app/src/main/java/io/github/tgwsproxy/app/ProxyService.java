package io.github.tgwsproxy.app;

import android.app.Notification;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.os.Build;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;
import android.widget.Toast;

import io.github.tgwsproxy.tgws.Tgws;

/**
 * Keeps the proxy alive while Telegram is in front: a foreground service
 * with a small status notification.
 */
public class ProxyService extends Service {
    static final String ACTION_START = "io.github.tgwsproxy.START";
    static final String ACTION_STOP = "io.github.tgwsproxy.STOP";
    private static final int NOTIF_ID = 1;

    private final Handler handler = new Handler(Looper.getMainLooper());
    private boolean foreground;

    private final Runnable tick = new Runnable() {
        @Override
        public void run() {
            if (!foreground) return;
            if (!Tgws.isRunning()) {
                // proxy was stopped from elsewhere (e.g. port taken after a settings change)
                stopEverything();
                return;
            }
            notifyStatus();
            handler.postDelayed(this, 5000);
        }
    };

    static void start(Context c) {
        Intent i = new Intent(c, ProxyService.class).setAction(ACTION_START);
        if (Build.VERSION.SDK_INT >= 26) c.startForegroundService(i);
        else c.startService(i);
    }

    static void stop(Context c) {
        c.startService(new Intent(c, ProxyService.class).setAction(ACTION_STOP));
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        String action = intent != null ? intent.getAction() : ACTION_START; // null = restarted by the system
        if (ACTION_STOP.equals(action)) {
            stopEverything();
            return START_NOT_STICKY;
        }
        // Must call startForeground quickly after startForegroundService.
        goForeground();
        String err = App.initCore(this);
        if (err == null) {
            try {
                Tgws.startProxy();
            } catch (Exception e) {
                err = e.getMessage();
            }
        }
        if (err != null) {
            final String msg = err;
            handler.post(() -> Toast.makeText(getApplicationContext(), msg, Toast.LENGTH_LONG).show());
            stopEverything();
            return START_NOT_STICKY;
        }
        handler.removeCallbacks(tick);
        handler.post(tick);
        return START_STICKY;
    }

    private void goForeground() {
        Notification n = buildNotification(getString(R.string.starting));
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(NOTIF_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);
        } else {
            startForeground(NOTIF_ID, n);
        }
        foreground = true;
    }

    private void notifyStatus() {
        Notification n = buildNotification(Tgws.statusLine());
        getSystemService(android.app.NotificationManager.class).notify(NOTIF_ID, n);
    }

    private Notification buildNotification(String text) {
        int piFlags = PendingIntent.FLAG_UPDATE_CURRENT | (Build.VERSION.SDK_INT >= 23 ? PendingIntent.FLAG_IMMUTABLE : 0);
        PendingIntent open = PendingIntent.getActivity(this, 0,
                new Intent(this, MainActivity.class), piFlags);
        PendingIntent stop = PendingIntent.getService(this, 1,
                new Intent(this, ProxyService.class).setAction(ACTION_STOP), piFlags);
        Notification.Builder b = Build.VERSION.SDK_INT >= 26
                ? new Notification.Builder(this, App.CHANNEL)
                : new Notification.Builder(this);
        b.setSmallIcon(R.drawable.ic_notification)
                .setContentTitle(getString(R.string.notif_title))
                .setContentText(text)
                .setContentIntent(open)
                .setOngoing(true)
                .setShowWhen(false)
                .addAction(new Notification.Action.Builder(null, getString(R.string.turn_off), stop).build());
        if (Build.VERSION.SDK_INT >= 31) {
            b.setForegroundServiceBehavior(Notification.FOREGROUND_SERVICE_IMMEDIATE);
        }
        return b.build();
    }

    private void stopEverything() {
        handler.removeCallbacks(tick);
        try {
            Tgws.stopProxy();
        } catch (Throwable ignored) {
        }
        if (foreground) {
            if (Build.VERSION.SDK_INT >= 24) stopForeground(STOP_FOREGROUND_REMOVE);
            else stopForeground(true);
            foreground = false;
        }
        stopSelf();
    }

    @Override
    public void onDestroy() {
        handler.removeCallbacks(tick);
        try {
            Tgws.stopProxy();
        } catch (Throwable ignored) {
        }
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }
}
