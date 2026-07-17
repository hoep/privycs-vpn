package com.privycs.vpn.data

import android.content.Context
import android.database.sqlite.SQLiteDatabase
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.strongswan.android.data.DatabaseHelper

/**
 * Pins the strongSwan profile database against the version-counter squat.
 *
 * Patch 0001 originally set DATABASE_VERSION to 20 while upstream was at 19, to
 * carry the Privycs PPK columns. That collides: strongSwan gates column
 * migrations on `column.Since > oldVersion` (DatabaseHelper.getAlterTables), so
 * a user whose database already reached OUR 20 would never receive upstream's
 * own future `Since = 20` columns — `20 > 20` is false — and the app would die
 * with "no such column". No choice of number fixes that; any value we occupy
 * swallows the identically-numbered upstream migration.
 *
 * The fix these tests pin: column presence is decided by what the table actually
 * has, not by the counter, and the counter is handed back to upstream.
 */
@RunWith(RobolectricTestRunner::class)
// The manifest's application is PrivycsApp, which inherits StrongSwanApplication's
// static System.loadLibrary("androidbridge") — the native side is not built on a
// plain JVM and this test does not need it: DatabaseHelper is Java plus SQLite.
@Config(application = android.app.Application::class)
class StrongswanDbMigrationTest {

    private lateinit var context: Context

    /** Mirrors DatabaseHelper.DATABASE_NAME (private there). */
    private val dbName = "strongswan.db"

    @Before
    fun setUp() {
        context = ApplicationProvider.getApplicationContext()
        context.deleteDatabase(dbName)
    }

    private fun columnsOf(db: SQLiteDatabase, table: String): Set<String> =
        db.rawQuery("PRAGMA table_info($table)", null).use { c ->
            buildSet { while (c.moveToNext()) add(c.getString(c.getColumnIndexOrThrow("name"))) }
        }

    private fun openViaHelper(): SQLiteDatabase = DatabaseHelper(context).writableDatabase

    /**
     * Seed a database the way a build BEFORE the PPK patch left it: schema at
     * upstream's version, no PPK columns.
     */
    private fun seedLegacyDb(version: Int, withPpk: Boolean = false) {
        val db = SQLiteDatabase.openOrCreateDatabase(context.getDatabasePath(dbName), null)
        db.execSQL(
            "CREATE TABLE vpnprofile (" +
                "_id INTEGER PRIMARY KEY AUTOINCREMENT, _uuid TEXT UNIQUE, name TEXT NOT NULL, " +
                "gateway TEXT NOT NULL, vpn_type TEXT NOT NULL DEFAULT ''" +
                (if (withPpk) ", ppk_id TEXT, ppk_psk TEXT" else "") + ")"
        )
        db.execSQL("INSERT INTO vpnprofile (_uuid, name, gateway) VALUES ('u-1', 'existing', 'gw.example')")
        db.version = version
        db.close()
    }

    /**
     * THE REGRESSION. A database sitting at the squatted version 20 that lacks a
     * declared column must still get it. Under the old version-gated rule this
     * was unreachable: onUpgrade does not even fire when oldVersion equals
     * DATABASE_VERSION, and once upstream ships its own Since = 20 columns the
     * `20 > 20` gate blocks them for good.
     */
    @Test
    fun `a database at the squatted version still receives missing columns`() {
        seedLegacyDb(version = 20, withPpk = false)

        val cols = openViaHelper().use { columnsOf(it, "vpnprofile") }

        assertTrue("ppk_id missing — the version counter is still gating column presence", "ppk_id" in cols)
        assertTrue("ppk_psk missing — the version counter is still gating column presence", "ppk_psk" in cols)
    }

    /**
     * An upgrader from a pre-PPK build: schema at upstream's 19, no PPK columns,
     * and — once DATABASE_VERSION is handed back to 19 — no upgrade edge at all
     * to hang the migration on. Presence must be healed regardless.
     */
    @Test
    fun `a pre-PPK database at upstream's version is healed`() {
        seedLegacyDb(version = 19, withPpk = false)

        val cols = openViaHelper().use { columnsOf(it, "vpnprofile") }

        assertTrue("ppk_id missing", "ppk_id" in cols)
        assertTrue("ppk_psk missing", "ppk_psk" in cols)
    }

    /**
     * Existing users carry the squatted 20 on disk. Handing the counter back to
     * 19 makes SQLiteOpenHelper take its DOWNGRADE path, whose default
     * implementation THROWS — that would brick every one of them on first open.
     */
    @Test
    fun `opening a squatted database does not throw and keeps the data`() {
        seedLegacyDb(version = 20, withPpk = true)

        val db = openViaHelper()
        val names = db.rawQuery("SELECT name FROM vpnprofile", null).use { c ->
            buildList { while (c.moveToNext()) add(c.getString(0)) }
        }
        val version = db.version
        db.close()

        assertEquals("the existing profile must survive", listOf("existing"), names)
        // The point of the exercise: the squatted stamp is handed back, so
        // upstream's own future migrations are no longer skipped.
        assertEquals("the database must be restamped to upstream's version", 19, version)
    }

    /** A fresh install must come up with the full schema and no healing needed. */
    @Test
    fun `a fresh database has the full schema`() {
        val cols = openViaHelper().use { columnsOf(it, "vpnprofile") }

        assertTrue("ppk_id missing on a fresh database", "ppk_id" in cols)
        assertTrue("ppk_psk missing on a fresh database", "ppk_psk" in cols)
        assertTrue("_uuid missing on a fresh database", "_uuid" in cols)
    }

    /** Healing runs on every open; it must not fight itself. */
    @Test
    fun `repeated opens are idempotent`() {
        seedLegacyDb(version = 20, withPpk = false)

        openViaHelper().close()
        val cols = openViaHelper().use { columnsOf(it, "vpnprofile") }

        assertEquals("ppk_id added more than once", 1, cols.count { it == "ppk_id" })
        assertTrue("ppk_psk missing after a second open", "ppk_psk" in cols)
    }
}
