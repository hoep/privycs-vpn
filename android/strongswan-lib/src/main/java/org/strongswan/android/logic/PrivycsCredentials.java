/*
 * Privycs-owned. NOT upstream strongSwan code, despite the package.
 *
 * This file lives in strongswan-lib/src/main/java/, not in the pinned
 * vendor/strongswan submodule. It will not be found there, and it must never
 * be committed there. build.gradle.kts Syncs the submodule's Java into a
 * generated dir (excluding only org/strongswan/android/ui/**) and lists
 * src/main/java alongside it, so a class placed here compiles into the same
 * package as the submodule's own sources. The two stubs under
 * org/strongswan/android/ui/ use the identical trick.
 */
package org.strongswan.android.logic;

import java.security.PrivateKey;
import java.util.Map;
import java.util.Objects;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;

/**
 * In-memory client credentials for an IPSec profile, handed to charon at
 * connect time in place of an Android KeyChain entry.
 *
 * <h3>Why this class is in the strongSwan package</h3>
 * charonservice.c binds {@code getUserKey()}/{@code getUserCertificate()}
 * natively via a hardcoded {@code FindClass} on the concrete class
 * {@code org.strongswan.android.logic.CharonVpnService}, and both methods are
 * private on it. There is no interface, no injection point, and no
 * subclassing path: the only way to change what those methods return is to
 * patch that class. The patch (vendor/strongswan-patches/) is deliberately
 * kept to a few lines by having it call into this holder, which lives outside
 * the submodule and can be edited freely. Package-level co-location is what
 * makes that call reachable without widening the patch.
 *
 * <h3>Why in-memory only</h3>
 * strongSwan's native layer treats the private key as an opaque signing
 * oracle: it only ever calls {@code Signature.initSign(key)}, never
 * {@code getEncoded()} ({@code get_encoding} is hard-wired to fail). So any
 * {@code PrivateKey} that {@code Signature.initSign} accepts satisfies the
 * contract, including one parsed in-process from the profile's PKCS#12 — no
 * KeyChain install required. That removes a credential the app could never
 * clean up afterwards (Android exposes no API to delete an installed KeyChain
 * entry), which is the point of the change.
 *
 * <p>The security argument for parsing the key ourselves rests entirely on it
 * staying here: entries are process-lifetime only and MUST NOT be persisted,
 * mirrored, or logged. strongswan.db and the privycs_ipsec prefs are both
 * plaintext stores and must never see this material. The encrypted
 * {@code configContent} remains the only at-rest copy.
 *
 * <p>Thread-safe: charon calls in from its own native threads, concurrently
 * with the Android main thread that populates the map.
 */
public final class PrivycsCredentials {

    /**
     * The client key and its leaf certificate for one profile.
     *
     * <p>{@code leafDer} is stored and returned by reference; callers must not
     * mutate it.
     */
    public static final class Entry {
        public final PrivateKey key;
        public final byte[] leafDer;

        Entry(PrivateKey key, byte[] leafDer) {
            this.key = key;
            this.leafDer = leafDer;
        }

        /**
         * Redacted on purpose. A provider's own {@code PrivateKey.toString()}
         * is free to render key material, and this object is one accidental
         * string concatenation away from a log line.
         */
        @Override
        public String toString() {
            return "PrivycsCredentials.Entry{redacted}";
        }
    }

    private static final Map<UUID, Entry> ENTRIES = new ConcurrentHashMap<>();

    private PrivycsCredentials() {
    }

    public static void put(UUID profileUuid, PrivateKey key, byte[] leafDer) {
        Objects.requireNonNull(profileUuid, "profileUuid");
        Objects.requireNonNull(key, "key");
        Objects.requireNonNull(leafDer, "leafDer");
        ENTRIES.put(profileUuid, new Entry(key, leafDer));
    }

    /**
     * @return the entry, or null if this profile has none — the patched
     *         CharonVpnService falls back to its upstream KeyChain path on
     *         null, so imported-elsewhere profiles keep working.
     */
    public static Entry get(UUID profileUuid) {
        return profileUuid == null ? null : ENTRIES.get(profileUuid);
    }

    /** Call on teardown of a profile's connection. */
    public static void clear(UUID profileUuid) {
        if (profileUuid != null) {
            ENTRIES.remove(profileUuid);
        }
    }

    public static void clearAll() {
        ENTRIES.clear();
    }
}
