//go:build darwin

package main

// macOS IPSec/IKEv2 via Apple's built-in NetworkExtension stack
// (NEVPNManager + NEVPNProtocolIKEv2). Replaces the Homebrew-strongSwan
// /swanctl path: the app creates the VPN configuration in-process, so
// it OWNS the config and can drive connect/disconnect programmatically
// — the security boundary that forbids controlling .mobileconfig-
// installed profiles does not apply to configs the calling app created
// itself (same model as iOS WireGuard/OpenVPN, NordVPN-Mac, Mullvad-Mac).
//
// Requires the com.apple.developer.networking.vpn.api (Personal VPN)
// entitlement — see build/darwin/entitlements.plist. The .sswan field
// mapping mirrors the iOS SswanProfile→NEVPNProtocolIKEv2 path proven
// in the iOS port (cert auth via inline PKCS#12 identityData).
//
// Obj-C lives directly in the cgo preamble (same style as
// power_macos.go); ARC + -x objective-c are package-wide cgo CFLAGS.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework NetworkExtension
#import <Foundation/Foundation.h>
#import <NetworkExtension/NetworkExtension.h>
#include <stdlib.h>
#include <string.h>

// Returns a malloc'd copy of err.localizedDescription (or fallback), or
// NULL when there is no error. Go frees the returned pointer.
static char* privycs_err_dup(NSError *err, NSString *fallback) {
    NSString *msg = err ? [err localizedDescription] : fallback;
    if (!msg) return NULL;
    const char *c = [msg UTF8String];
    return c ? strdup(c) : NULL;
}

// Synchronously load the shared NEVPNManager preferences. Blocks the
// calling (background) goroutine's thread on a semaphore while the
// completion handler fires on the main queue — safe because the app's
// AppKit run loop services the main queue and we are never on it.
static NSError* privycs_load_sync(NEVPNManager *mgr) {
    __block NSError *outErr = nil;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [mgr loadFromPreferencesWithCompletionHandler:^(NSError *e) {
        outErr = e;
        dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
    return outErr;
}

// Build + save an IKEv2 cert-auth config. NULL = success.
static char* privycs_nevpn_configure(const char* name, const char* server,
        const char* remoteID, const char* localID,
        const void* p12, int p12len, const char* p12pass) {
    @autoreleasepool {
        NEVPNManager *mgr = [NEVPNManager sharedManager];
        NSError *loadErr = privycs_load_sync(mgr);
        if (loadErr) return privycs_err_dup(loadErr, nil);

        NSString *wantServer = [NSString stringWithUTF8String:server];
        NSString *wantName = [NSString stringWithUTF8String:name];

        // IDEMPOTENCY — avoid the per-connect auth prompt.
        // configure() runs on EVERY connect. saveToPreferences re-writes the
        // system VPN config and re-imports the PKCS#12 identity into the
        // keychain, which makes macOS prompt the user (admin/keychain) on
        // EVERY connect. Skip the save entirely when the already-saved config
        // is the one we'd write (same server + name + IKEv2 cert-auth +
        // enabled): reconnecting to the same endpoint then needs no save → no
        // prompt. A genuinely different endpoint still saves once (unavoidable:
        // macOS gates VPN-config changes for Developer-ID apps). Mirrors the
        // iOS pattern of configuring a slot once and reusing it.
        NEVPNProtocol *cur = mgr.protocolConfiguration;
        if (mgr.isEnabled && [cur isKindOfClass:[NEVPNProtocolIKEv2 class]]) {
            NEVPNProtocolIKEv2 *ck = (NEVPNProtocolIKEv2 *)cur;
            if ([ck.serverAddress isEqualToString:wantServer]
                && [mgr.localizedDescription isEqualToString:wantName]
                && ck.authenticationMethod == NEVPNIKEAuthenticationMethodCertificate) {
                return NULL; // identical config already saved — no save, no prompt
            }
        }

        NEVPNProtocolIKEv2 *p = [[NEVPNProtocolIKEv2 alloc] init];
        p.serverAddress = [NSString stringWithUTF8String:server];
        p.remoteIdentifier = [NSString stringWithUTF8String:remoteID];
        if (localID && strlen(localID) > 0) {
            p.localIdentifier = [NSString stringWithUTF8String:localID];
        }
        p.authenticationMethod = NEVPNIKEAuthenticationMethodCertificate;
        if (p12 && p12len > 0) {
            p.identityData = [NSData dataWithBytes:p12 length:(NSUInteger)p12len];
        }
        if (p12pass && strlen(p12pass) > 0) {
            p.identityDataPassword = [NSString stringWithUTF8String:p12pass];
        }
        p.useExtendedAuthentication = NO;
        // CRITICAL: mirror iOS (VPNTunnelManager.makeIKEv2Proto, =false). Left
        // unset this defaults to YES on macOS → the tunnel only routes the
        // IKEv2 INTERNAL_IP4_SUBNET config-mode attribute the gateway pushes
        // (often a /32 or nothing) instead of full-tunnel → NEVPNManager
        // reports "connected" but NO traffic flows → 20s blackhole → failover.
        // NO = route all traffic through the tunnel (full tunnel), like iOS.
        p.useConfigurationAttributeInternalIPSubnet = NO;
        p.disableMOBIKE = NO;
        p.disableRedirect = NO;
        p.enablePFS = YES;
        p.deadPeerDetectionRate = NEVPNIKEv2DeadPeerDetectionRateMedium;

        // Match the GATEWAY's documented IKE/ESP proposal exactly:
        // aes256-sha256-modp2048 (AES-256-CBC + SHA256 + DH group 14). Forcing
        // AES256-GCM + group19 (the prior values) offered something the gateway
        // does not accept → no common proposal → IKE_SA_INIT timed out. Mirrors
        // the iOS fix (VPNTunnelManager.connectViaIKEv2). CBC needs an explicit
        // integrity algorithm (GCM is AEAD).
        p.IKESecurityAssociationParameters.encryptionAlgorithm = NEVPNIKEv2EncryptionAlgorithmAES256;
        p.IKESecurityAssociationParameters.integrityAlgorithm = NEVPNIKEv2IntegrityAlgorithmSHA256;
        p.IKESecurityAssociationParameters.diffieHellmanGroup = NEVPNIKEv2DiffieHellmanGroup14;
        p.childSecurityAssociationParameters.encryptionAlgorithm = NEVPNIKEv2EncryptionAlgorithmAES256;
        p.childSecurityAssociationParameters.integrityAlgorithm = NEVPNIKEv2IntegrityAlgorithmSHA256;
        p.childSecurityAssociationParameters.diffieHellmanGroup = NEVPNIKEv2DiffieHellmanGroup14;

        mgr.protocolConfiguration = p;
        mgr.localizedDescription = [NSString stringWithUTF8String:name];
        [mgr setEnabled:YES];

        __block NSError *saveErr = nil;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        [mgr saveToPreferencesWithCompletionHandler:^(NSError *e) {
            saveErr = e;
            dispatch_semaphore_signal(sem);
        }];
        dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
        if (saveErr) return privycs_err_dup(saveErr, nil);

        // Reload so mgr.connection reflects the freshly-saved config.
        NSError *reErr = privycs_load_sync(mgr);
        return reErr ? privycs_err_dup(reErr, nil) : NULL;
    }
}

// Start the tunnel. NULL = success.
//
// The FIRST startVPNTunnel() right after a fresh saveToPreferences()
// frequently throws NEVPNErrorConfigurationStale / ...Invalid because the
// just-saved config hasn't propagated to the NE daemon yet. iOS handles this
// with a reload+retry loop (VPNTunnelManager.startTunnelRetrying, up to 8×);
// the previous macOS bridge tried exactly ONCE and therefore failed to connect.
// Retry up to 8× on stale/invalid, reloading prefs between attempts.
static char* privycs_nevpn_start(void) {
    @autoreleasepool {
        NEVPNManager *mgr = [NEVPNManager sharedManager];
        NSError *loadErr = privycs_load_sync(mgr);
        if (loadErr) return privycs_err_dup(loadErr, nil);
        NSError *startErr = nil;
        for (int attempt = 0; attempt < 8; attempt++) {
            startErr = nil;
            if ([[mgr connection] startVPNTunnelAndReturnError:&startErr]) {
                return NULL; // success
            }
            BOOL retriable = startErr
                && [startErr.domain isEqualToString:NEVPNErrorDomain]
                && (startErr.code == NEVPNErrorConfigurationStale
                    || startErr.code == NEVPNErrorConfigurationInvalid);
            if (!retriable) break;
            [NSThread sleepForTimeInterval:0.3];
            privycs_load_sync(mgr); // reload before the next attempt
        }
        return privycs_err_dup(startErr, @"startVPNTunnel failed");
    }
}

// Stop the tunnel. NULL = success.
static char* privycs_nevpn_stop(void) {
    @autoreleasepool {
        NEVPNManager *mgr = [NEVPNManager sharedManager];
        NSError *loadErr = privycs_load_sync(mgr);
        if (loadErr) return privycs_err_dup(loadErr, nil);
        [[mgr connection] stopVPNTunnel];
        return NULL;
    }
}

// Raw NEVPNStatus: 0=invalid 1=disconnected 2=connecting 3=connected
// 4=reasserting 5=disconnecting.
static int privycs_nevpn_status(void) {
    @autoreleasepool {
        NEVPNManager *mgr = [NEVPNManager sharedManager];
        (void)privycs_load_sync(mgr);
        return (int)[[mgr connection] status];
    }
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

// nevpnConfigure builds + saves a Personal-VPN IKEv2 cert-auth config.
func nevpnConfigure(name, server, remoteID, localID string, p12 []byte, p12pass string) error {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	cServer := C.CString(server)
	defer C.free(unsafe.Pointer(cServer))
	cRemote := C.CString(remoteID)
	defer C.free(unsafe.Pointer(cRemote))
	cLocal := C.CString(localID)
	defer C.free(unsafe.Pointer(cLocal))
	cPass := C.CString(p12pass)
	defer C.free(unsafe.Pointer(cPass))

	var p12ptr unsafe.Pointer
	if len(p12) > 0 {
		p12ptr = unsafe.Pointer(&p12[0])
	}
	cerr := C.privycs_nevpn_configure(cName, cServer, cRemote, cLocal,
		p12ptr, C.int(len(p12)), cPass)
	if cerr != nil {
		defer C.free(unsafe.Pointer(cerr))
		return errors.New(C.GoString(cerr))
	}
	return nil
}

func nevpnStart() error {
	cerr := C.privycs_nevpn_start()
	if cerr != nil {
		defer C.free(unsafe.Pointer(cerr))
		return errors.New(C.GoString(cerr))
	}
	return nil
}

func nevpnStop() error {
	cerr := C.privycs_nevpn_stop()
	if cerr != nil {
		defer C.free(unsafe.Pointer(cerr))
		return errors.New(C.GoString(cerr))
	}
	return nil
}

// nevpnStatusRaw returns the NEVPNStatus enum value (3 == connected).
func nevpnStatusRaw() int {
	return int(C.privycs_nevpn_status())
}
