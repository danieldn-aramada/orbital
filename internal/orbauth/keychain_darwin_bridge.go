//go:build darwin

package orbauth

/*
#cgo LDFLAGS: -framework CoreFoundation -framework Security

#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>

// orbital_add_item adds a generic password item protected by
// kSecAttrAccessibleWhenUnlockedThisDeviceOnly — item is locked when the
// device is locked, never synced to iCloud, and cannot be migrated to
// another device. No biometric prompt is used; Touch ID ACLs require the
// binary to be code-signed with Apple entitlements (errSecMissingEntitlement)
// which is not practical for an unsigned developer CLI.
static OSStatus orbital_add_item(
    const char *service,
    const char *account,
    const char *label,
    const uint8_t *data, CFIndex dataLen)
{
    CFStringRef cfService = CFStringCreateWithCString(kCFAllocatorDefault, service, kCFStringEncodingUTF8);
    CFStringRef cfAccount = CFStringCreateWithCString(kCFAllocatorDefault, account, kCFStringEncodingUTF8);
    CFStringRef cfLabel   = CFStringCreateWithCString(kCFAllocatorDefault, label,   kCFStringEncodingUTF8);
    CFDataRef   cfData    = CFDataCreate(kCFAllocatorDefault, data, dataLen);

    const void *keys[] = {
        kSecClass,
        kSecAttrService,
        kSecAttrAccount,
        kSecAttrLabel,
        kSecValueData,
        kSecAttrAccessible,
    };
    const void *values[] = {
        kSecClassGenericPassword,
        cfService,
        cfAccount,
        cfLabel,
        cfData,
        kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
    };
    CFDictionaryRef query = CFDictionaryCreate(
        kCFAllocatorDefault, keys, values, 6,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks);

    OSStatus status = SecItemAdd(query, NULL);

    CFRelease(query);
    CFRelease(cfData);
    CFRelease(cfLabel);
    CFRelease(cfAccount);
    CFRelease(cfService);
    return status;
}

// orbital_delete_item removes a generic password item. Ignores errSecItemNotFound.
static OSStatus orbital_delete_item(const char *service, const char *account) {
    CFStringRef cfService = CFStringCreateWithCString(kCFAllocatorDefault, service, kCFStringEncodingUTF8);
    CFStringRef cfAccount = CFStringCreateWithCString(kCFAllocatorDefault, account, kCFStringEncodingUTF8);

    const void *keys[]   = { kSecClass, kSecAttrService, kSecAttrAccount };
    const void *values[] = { kSecClassGenericPassword, cfService, cfAccount };
    CFDictionaryRef query = CFDictionaryCreate(
        kCFAllocatorDefault, keys, values, 3,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks);

    OSStatus status = SecItemDelete(query);

    CFRelease(query);
    CFRelease(cfAccount);
    CFRelease(cfService);
    return status;
}

// orbital_load_item copies the data for a generic password item.
// On success, *outData is set to a CFDataRef that the caller must CFRelease.
// Returns the OSStatus.
static OSStatus orbital_load_item(
    const char *service,
    const char *account,
    CFDataRef  *outData)
{
    CFStringRef cfService = CFStringCreateWithCString(kCFAllocatorDefault, service, kCFStringEncodingUTF8);
    CFStringRef cfAccount = CFStringCreateWithCString(kCFAllocatorDefault, account, kCFStringEncodingUTF8);

    const void *keys[] = {
        kSecClass,
        kSecAttrService,
        kSecAttrAccount,
        kSecMatchLimit,
        kSecReturnData,
    };
    const void *values[] = {
        kSecClassGenericPassword,
        cfService,
        cfAccount,
        kSecMatchLimitOne,
        kCFBooleanTrue,
    };
    CFDictionaryRef query = CFDictionaryCreate(
        kCFAllocatorDefault, keys, values, 5,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks);

    CFTypeRef result = NULL;
    OSStatus status = SecItemCopyMatching(query, &result);
    if (status == errSecSuccess && result != NULL) {
        *outData = (CFDataRef)result;
    }

    CFRelease(query);
    CFRelease(cfAccount);
    CFRelease(cfService);
    return status;
}
*/
import "C"
import "unsafe"

// addKeychainItem writes a generic password to the macOS keychain.
// Returns the raw OSStatus.
func addKeychainItem(service, account, label string, data []byte) C.OSStatus {
	cs := C.CString(service)
	defer C.free(unsafe.Pointer(cs))
	ca := C.CString(account)
	defer C.free(unsafe.Pointer(ca))
	cl := C.CString(label)
	defer C.free(unsafe.Pointer(cl))

	var dataPtr *C.uint8_t
	if len(data) > 0 {
		dataPtr = (*C.uint8_t)(&data[0])
	}
	return C.orbital_add_item(cs, ca, cl, dataPtr, C.CFIndex(len(data)))
}

// deleteKeychainItem removes a generic password item. Returns the raw OSStatus.
func deleteKeychainItem(service, account string) C.OSStatus {
	cs := C.CString(service)
	defer C.free(unsafe.Pointer(cs))
	ca := C.CString(account)
	defer C.free(unsafe.Pointer(ca))
	return C.orbital_delete_item(cs, ca)
}

// loadKeychainItem retrieves the data for a generic password item.
// Returns the raw bytes and OSStatus.
func loadKeychainItem(service, account string) ([]byte, C.OSStatus) {
	cs := C.CString(service)
	defer C.free(unsafe.Pointer(cs))
	ca := C.CString(account)
	defer C.free(unsafe.Pointer(ca))

	var cfData C.CFDataRef
	status := C.orbital_load_item(cs, ca, &cfData)
	if status != C.errSecSuccess || cfData == 0 {
		return nil, status
	}
	defer C.CFRelease(C.CFTypeRef(cfData))

	length := C.CFDataGetLength(cfData)
	if length == 0 {
		return []byte{}, status
	}
	ptr := C.CFDataGetBytePtr(cfData)
	return C.GoBytes(unsafe.Pointer(ptr), C.int(length)), status
}

// osStatusIsNotFound reports whether an OSStatus is errSecItemNotFound.
func osStatusIsNotFound(s C.OSStatus) bool { return s == C.errSecItemNotFound }
