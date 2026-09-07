package com.xdreemb52.azhalha

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Bitmap
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import android.webkit.CookieManager
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import androidx.webkit.JavaScriptReplyProxy
import androidx.webkit.WebMessageCompat
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature

class MainActivity : Activity() {
    companion object {
        private const val REQUEST_NOTIFICATIONS = 5207
        private const val SITE_HOST = "xdreemb52.vercel.app"
        private const val SITE_ORIGIN = "https://xdreemb52.vercel.app"
        private const val BRIDGE_NAME = "AzhalhaAndroidBridge"
        private const val PREFS = "azhalha_native_permissions"
        private const val ASKED = "post_notifications_asked"
        private const val APP_PREFS = "azhalha_app_state"
        private const val LAST_VERSION = "last_version_code"
    }

    private lateinit var webView: WebView
    private var initialLoadStarted = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        webView = WebView(this)
        setContentView(webView)

        webView.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            databaseEnabled = true
            cacheMode = WebSettings.LOAD_DEFAULT
            mediaPlaybackRequiresUserGesture = false
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            userAgentString = "$userAgentString AzhalhaAndroid/${BuildConfig.VERSION_NAME}"
        }

        CookieManager.getInstance().apply {
            setAcceptCookie(true)
            setAcceptThirdPartyCookies(webView, true)
        }

        clearHttpCacheAfterUpgrade()
        installNativeNotificationBridge()

        webView.webChromeClient = WebChromeClient()
        webView.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView?, request: WebResourceRequest?): Boolean {
                val uri = request?.url ?: return false
                return if (uri.scheme == "http" || uri.scheme == "https") {
                    false
                } else {
                    runCatching { startActivity(Intent(Intent.ACTION_VIEW, uri)) }
                    true
                }
            }

            override fun onPageStarted(view: WebView?, url: String?, favicon: Bitmap?) {
                super.onPageStarted(view, url, favicon)
                publishNativePermission(view, url)
            }

            override fun onPageFinished(view: WebView?, url: String?) {
                super.onPageFinished(view, url)
                publishNativePermission(view, url)
            }
        }

        webView.setDownloadListener { url, _, _, _, _ ->
            runCatching { startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url))) }
        }

        if (savedInstanceState != null) {
            webView.restoreState(savedInstanceState)
            initialLoadStarted = true
            requestNotificationPermissionIfNeeded()
        } else {
            requestNotificationPermissionIfNeeded()
        }
    }

    private fun clearHttpCacheAfterUpgrade() {
        val prefs = getSharedPreferences(APP_PREFS, MODE_PRIVATE)
        val previous = prefs.getInt(LAST_VERSION, -1)
        if (previous != BuildConfig.VERSION_CODE) {
            webView.clearCache(true)
            prefs.edit().putInt(LAST_VERSION, BuildConfig.VERSION_CODE).apply()
        }
    }

    private fun installNativeNotificationBridge() {
        if (!WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)) return

        WebViewCompat.addWebMessageListener(
            webView,
            BRIDGE_NAME,
            setOf(SITE_ORIGIN),
            object : WebViewCompat.WebMessageListener {
                override fun onPostMessage(
                    view: WebView,
                    message: WebMessageCompat,
                    sourceOrigin: Uri,
                    isMainFrame: Boolean,
                    replyProxy: JavaScriptReplyProxy
                ) {
                    val trusted = isMainFrame &&
                        sourceOrigin.scheme == "https" &&
                        sourceOrigin.host.equals(SITE_HOST, ignoreCase = true) &&
                        (sourceOrigin.port == -1 || sourceOrigin.port == 443)
                    if (!trusted) return

                    when (message.data) {
                        "get-notification-permission" ->
                            replyProxy.postMessage(notificationPermissionState())

                        "request-notification-permission" -> {
                            replyProxy.postMessage("accepted")
                            requestNotificationPermissionFromSite()
                        }

                        "open-notification-settings" -> {
                            replyProxy.postMessage("accepted")
                            openNotificationSettings()
                        }
                    }
                }
            }
        )
    }

    private fun permissionWasAsked(): Boolean =
        getSharedPreferences(PREFS, MODE_PRIVATE).getBoolean(ASKED, false)

    private fun markPermissionAsked() {
        getSharedPreferences(PREFS, MODE_PRIVATE).edit().putBoolean(ASKED, true).apply()
    }

    private fun requestNotificationPermissionIfNeeded() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            notificationPermissionState() == "default") {
            markPermissionAsked()
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQUEST_NOTIFICATIONS)
        } else {
            loadSiteIfNeeded()
        }
    }

    private fun requestNotificationPermissionFromSite() {
        when (notificationPermissionState()) {
            "granted" -> publishNativePermission(webView, webView.url)
            "default" -> {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                    markPermissionAsked()
                    requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQUEST_NOTIFICATIONS)
                } else {
                    openNotificationSettings()
                }
            }
            else -> openNotificationSettings()
        }
    }

    private fun notificationPermissionState(): String {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            val runtimeGranted = ContextCompat.checkSelfPermission(
                this,
                Manifest.permission.POST_NOTIFICATIONS
            ) == PackageManager.PERMISSION_GRANTED

            if (!runtimeGranted) {
                return if (permissionWasAsked()) "denied" else "default"
            }
        }

        return if (NotificationManagerCompat.from(this).areNotificationsEnabled()) {
            "granted"
        } else {
            "denied"
        }
    }

    private fun loadSiteIfNeeded() {
        if (initialLoadStarted) return
        initialLoadStarted = true
        webView.loadUrl(BuildConfig.SITE_URL)
    }

    private fun isTrustedUrl(rawUrl: String?): Boolean {
        if (rawUrl.isNullOrBlank()) return false
        return runCatching {
            val uri = Uri.parse(rawUrl)
            uri.scheme == "https" &&
                uri.host.equals(SITE_HOST, ignoreCase = true) &&
                (uri.port == -1 || uri.port == 443)
        }.getOrDefault(false)
    }

    private fun publishNativePermission(view: WebView?, url: String?) {
        val target = view ?: return
        if (!isTrustedUrl(url)) return
        val state = notificationPermissionState()
        val js = """
            (() => {
              const state = '$state';
              window.__AZHALHA_ANDROID__ = true;
              window.__AZHALHA_ANDROID_NOTIFICATION_PERMISSION__ = state;
              window.__AZHALHA_GET_NOTIFICATION_PERMISSION__ = () =>
                window.__AZHALHA_ANDROID_NOTIFICATION_PERMISSION__ || 'default';
              window.__AZHALHA_REQUEST_NOTIFICATION_PERMISSION__ = () => {
                try {
                  const bridge = window.$BRIDGE_NAME;
                  if (bridge && typeof bridge.postMessage === 'function') {
                    bridge.postMessage('request-notification-permission');
                  }
                } catch (_) {}
                return Promise.resolve(window.__AZHALHA_GET_NOTIFICATION_PERMISSION__());
              };
              window.__AZHALHA_OPEN_NOTIFICATION_SETTINGS__ = () => {
                try {
                  const bridge = window.$BRIDGE_NAME;
                  if (bridge && typeof bridge.postMessage === 'function') {
                    bridge.postMessage('open-notification-settings');
                    return true;
                  }
                } catch (_) {}
                return false;
              };
              try {
                const bridge = window.$BRIDGE_NAME;
                if (bridge && typeof bridge.postMessage === 'function') {
                  bridge.onmessage = (event) => {
                    const next = event && event.data;
                    if (next === 'granted' || next === 'denied' || next === 'default') {
                      window.__AZHALHA_ANDROID_NOTIFICATION_PERMISSION__ = next;
                      window.dispatchEvent(new CustomEvent('azhalha-native-notification-permission', { detail: { state: next } }));
                    }
                  };
                  bridge.postMessage('get-notification-permission');
                }
              } catch (_) {}
              try {
                window.dispatchEvent(new CustomEvent('azhalha-native-notification-permission', { detail: { state } }));
              } catch (_) {}
            })();
        """.trimIndent()
        target.evaluateJavascript(js, null)
    }

    private fun openNotificationSettings() {
        runCatching {
            startActivity(Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).apply {
                putExtra(Settings.EXTRA_APP_PACKAGE, packageName)
            })
        }
    }

    override fun onRequestPermissionsResult(
        requestCode: Int,
        permissions: Array<out String>,
        grantResults: IntArray
    ) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == REQUEST_NOTIFICATIONS) {
            loadSiteIfNeeded()
            publishNativePermission(webView, webView.url)
        }
    }

    override fun onResume() {
        super.onResume()
        if (::webView.isInitialized && initialLoadStarted) {
            publishNativePermission(webView, webView.url)
        }
    }

    override fun onSaveInstanceState(outState: Bundle) {
        webView.saveState(outState)
        super.onSaveInstanceState(outState)
    }

    @Deprecated("Deprecated in Java")
    override fun onBackPressed() {
        if (::webView.isInitialized && webView.canGoBack()) webView.goBack() else super.onBackPressed()
    }

    override fun onDestroy() {
        if (::webView.isInitialized) {
            webView.stopLoading()
            webView.destroy()
        }
        super.onDestroy()
    }
}
