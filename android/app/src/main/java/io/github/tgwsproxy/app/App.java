package io.github.tgwsproxy.app;

import android.app.Application;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.content.Context;
import android.os.Build;

import java.util.Locale;

import io.github.tgwsproxy.tgws.Tgws;

public class App extends Application {
    static final String CHANNEL = "proxy";

    @Override
    public void onCreate() {
        super.onCreate();
        if (Build.VERSION.SDK_INT >= 26) {
            NotificationChannel ch = new NotificationChannel(CHANNEL,
                    getString(R.string.channel_name), NotificationManager.IMPORTANCE_LOW);
            ch.setShowBadge(false);
            getSystemService(NotificationManager.class).createNotificationChannel(ch);
        }
    }

    /** Loads the Go core once per process. Returns an error text or null. */
    static synchronized String initCore(Context c) {
        try {
            String lang = Locale.getDefault().getLanguage().equals("ru") ? "ru" : "en";
            Tgws.init(dataDir(c), lang);
            return null;
        } catch (Exception e) {
            return e.getMessage();
        }
    }

    static String dataDir(Context c) {
        return c.getFilesDir().getAbsolutePath();
    }
}
