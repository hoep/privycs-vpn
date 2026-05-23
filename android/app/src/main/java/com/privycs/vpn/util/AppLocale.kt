package com.privycs.vpn.util

import android.app.Activity
import android.app.LocaleManager
import android.content.Context
import android.content.res.Configuration
import android.os.Build
import android.os.LocaleList
import java.util.Locale

/**
 * In-app language selection.
 *
 * Android 13+ has a system per-app language picker (the app ships
 * res/xml/locales_config.xml for it). This helper adds an *in-app*
 * picker as well, so Android 8–12 — which have no system picker — can
 * also override the app language.
 *
 * - **API 33+**: delegates to the framework [LocaleManager], so the
 *   in-app choice and the system per-app picker stay in sync. The
 *   framework recreates the activity itself.
 * - **API < 33**: persists the tag here and applies it by wrapping the
 *   base context (see [wrap], called from `attachBaseContext` in both
 *   MainActivity and PrivycsApp), then recreates the activity.
 *
 * A language tag of `""` means "follow the system language".
 */
object AppLocale {
    private const val PREFS = "app_locale"
    private const val KEY = "language_tag"

    /** BCP-47 tags the app is translated into; "" = system default. */
    val SUPPORTED = listOf("", "en", "de", "es", "fr", "it", "pt")

    private fun prefs(context: Context) =
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** The currently selected language tag — `""` = system default. */
    fun currentTag(context: Context): String {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            val locales = context.getSystemService(LocaleManager::class.java)
                ?.applicationLocales
            return if (locales == null || locales.isEmpty) "" else locales[0].toLanguageTag()
        }
        return prefs(context).getString(KEY, "").orEmpty()
    }

    /**
     * Apply [tag] as the app language and recreate [activity] so the new
     * resources take effect. `""` resets to the system language.
     */
    fun setLanguage(activity: Activity, tag: String) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            val list =
                if (tag.isEmpty()) LocaleList.getEmptyLocaleList()
                else LocaleList.forLanguageTags(tag)
            activity.getSystemService(LocaleManager::class.java)?.applicationLocales = list
            // LocaleManager recreates the activity itself.
        } else {
            prefs(activity).edit().putString(KEY, tag).apply()
            activity.recreate()
        }
    }

    /**
     * Wrap a base context with the persisted locale — call from
     * `Activity`/`Application.attachBaseContext`. No-op on API 33+ (the
     * framework already applies the LocaleManager choice) and when no
     * in-app language is set.
     */
    fun wrap(base: Context): Context {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) return base
        val tag = prefs(base).getString(KEY, "").orEmpty()
        if (tag.isEmpty()) return base
        val locale = Locale.forLanguageTag(tag)
        Locale.setDefault(locale)
        val config = Configuration(base.resources.configuration)
        config.setLocale(locale)
        return base.createConfigurationContext(config)
    }
}
