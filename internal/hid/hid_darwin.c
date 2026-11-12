// IOKit side of the macOS backend; see hid_darwin.go.
//
// Property reads, non-exclusive open, the input report callback and
// IOHIDDeviceSetReport follow mxswitch's macos/mxswitch.c
// (https://github.com/marcocosta97/mxswitch, MIT, Copyright (c) Marco Costa).

#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOKitLib.h>
#include <IOKit/hid/IOHIDManager.h>
#include <stdlib.h>
#include <string.h>

#include "_cgo_export.h"

#define LOGITECH_VID 0x046D

struct esw_session {
	IOHIDManagerRef mgr;
	CFRunLoopRef loop;
	CFRunLoopSourceRef stop_source;
	int stopped; // run-loop thread only
	int count;   // devices opened and scheduled
	IOHIDDeviceRef *devs;
	uint8_t **bufs;
	CFIndex *buf_lens;
	int *gone; // set by the removal callback
	uintptr_t ctx; // cgo.Handle of the Go device
};

static int32_t prop_int(IOHIDDeviceRef dev, CFStringRef key) {
	CFTypeRef v = IOHIDDeviceGetProperty(dev, key);
	int32_t out = 0;
	if (v && CFGetTypeID(v) == CFNumberGetTypeID())
		CFNumberGetValue((CFNumberRef)v, kCFNumberSInt32Type, &out);
	return out;
}

static uint64_t registry_id(IOHIDDeviceRef dev) {
	uint64_t id = 0;
	io_service_t service = IOHIDDeviceGetService(dev);
	if (service)
		IORegistryEntryGetRegistryEntryID(service, &id);
	return id;
}

// A manager matching Logitech's vendor ID, or NULL. It is never opened:
// only the devices chosen by Open are, one by one.
static IOHIDManagerRef logitech_manager(void) {
	IOHIDManagerRef mgr = IOHIDManagerCreate(kCFAllocatorDefault, kIOHIDOptionsTypeNone);
	if (!mgr)
		return NULL;
	int32_t vid = LOGITECH_VID;
	CFNumberRef num = CFNumberCreate(kCFAllocatorDefault, kCFNumberSInt32Type, &vid);
	CFStringRef key = CFSTR(kIOHIDVendorIDKey);
	CFDictionaryRef match = NULL;
	if (num) {
		match = CFDictionaryCreate(kCFAllocatorDefault, (const void **)&key, (const void **)&num, 1,
		                           &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
		CFRelease(num);
	}
	if (!match) {
		CFRelease(mgr);
		return NULL;
	}
	IOHIDManagerSetDeviceMatching(mgr, match);
	CFRelease(match);
	return mgr;
}

// The manager's devices, owned by *set (NULL when there are none). The
// caller frees the array and releases *set.
static IOHIDDeviceRef *copy_devices(IOHIDManagerRef mgr, CFSetRef *set, CFIndex *n) {
	*n = 0;
	*set = IOHIDManagerCopyDevices(mgr);
	CFIndex count = *set ? CFSetGetCount(*set) : 0;
	IOHIDDeviceRef *devs = calloc((size_t)count + 1, sizeof(*devs));
	if (!devs)
		return NULL;
	if (count)
		CFSetGetValues(*set, (const void **)devs);
	*n = count;
	return devs;
}

int32_t esw_enumerate(esw_info **out, int *count) {
	*out = NULL;
	*count = 0;
	IOHIDManagerRef mgr = logitech_manager();
	if (!mgr)
		return kIOReturnNoMemory;
	CFSetRef set;
	CFIndex n;
	IOHIDDeviceRef *devs = copy_devices(mgr, &set, &n);
	esw_info *infos = calloc((size_t)n + 1, sizeof(*infos));
	if (!devs || !infos) {
		free(devs);
		free(infos);
		if (set)
			CFRelease(set);
		CFRelease(mgr);
		return kIOReturnNoMemory;
	}
	for (CFIndex i = 0; i < n; i++) {
		IOHIDDeviceRef d = devs[i];
		esw_info *in = &infos[i];
		in->id = registry_id(d);
		in->vendor = prop_int(d, CFSTR(kIOHIDVendorIDKey));
		in->product = prop_int(d, CFSTR(kIOHIDProductIDKey));
		in->usage_page = prop_int(d, CFSTR(kIOHIDPrimaryUsagePageKey));
		in->usage = prop_int(d, CFSTR(kIOHIDPrimaryUsageKey));
		in->max_in = prop_int(d, CFSTR(kIOHIDMaxInputReportSizeKey));
		in->max_out = prop_int(d, CFSTR(kIOHIDMaxOutputReportSizeKey));
		in->location = prop_int(d, CFSTR(kIOHIDLocationIDKey));
		CFTypeRef name = IOHIDDeviceGetProperty(d, CFSTR(kIOHIDProductKey));
		if (!name || CFGetTypeID(name) != CFStringGetTypeID() ||
		    !CFStringGetCString((CFStringRef)name, in->name, sizeof(in->name), kCFStringEncodingUTF8))
			in->name[0] = '\0';
	}
	free(devs);
	if (set)
		CFRelease(set);
	CFRelease(mgr);
	*out = infos;
	*count = (int)n;
	return kIOReturnSuccess;
}

static void input_cb(void *context, IOReturn result, void *sender, IOHIDReportType type,
                     uint32_t report_id, uint8_t *report, CFIndex len) {
	(void)sender;
	(void)type;
	esw_session *s = context;
	if (result != kIOReturnSuccess || len <= 0)
		return;
	eswInputReport(s->ctx, report_id, report, (long)len);
}

static void removal_cb(void *context, IOReturn result, void *sender) {
	(void)result;
	esw_session *s = context;
	for (int i = 0; i < s->count; i++)
		if ((void *)s->devs[i] == sender)
			s->gone[i] = 1;
	eswRemoved(s->ctx);
}

static void stop_perform(void *info) {
	esw_session *s = info;
	s->stopped = 1;
	CFRunLoopStop(CFRunLoopGetCurrent());
}

// Closes everything; runs on the run-loop thread once its loop has ended,
// so no callback can run during or after it.
static void session_free(esw_session *s) {
	for (int i = 0; i < s->count; i++) {
		IOHIDDeviceRef d = s->devs[i];
		if (!s->gone[i]) {
			IOHIDDeviceRegisterInputReportCallback(d, s->bufs[i], s->buf_lens[i], NULL, NULL);
			IOHIDDeviceRegisterRemovalCallback(d, NULL, NULL);
			IOHIDDeviceUnscheduleFromRunLoop(d, s->loop, kCFRunLoopDefaultMode);
			IOHIDDeviceClose(d, kIOHIDOptionsTypeNone);
		}
		CFRelease(d);
	}
	if (s->stop_source) {
		CFRunLoopRemoveSource(s->loop, s->stop_source, kCFRunLoopDefaultMode);
		CFRelease(s->stop_source);
	}
	if (s->mgr)
		CFRelease(s->mgr);
	if (s->loop)
		CFRelease(s->loop);
	for (int i = 0; i < s->count; i++)
		free(s->bufs[i]);
	free(s->devs);
	free(s->bufs);
	free(s->buf_lens);
	free(s->gone);
	free(s);
}

int32_t esw_open(const uint64_t *ids, int n, uintptr_t ctx, esw_session **out) {
	*out = NULL;
	esw_session *s = calloc(1, sizeof(*s));
	if (!s)
		return kIOReturnNoMemory;
	s->ctx = ctx;
	s->devs = calloc((size_t)n + 1, sizeof(*s->devs));
	s->bufs = calloc((size_t)n + 1, sizeof(*s->bufs));
	s->buf_lens = calloc((size_t)n + 1, sizeof(*s->buf_lens));
	s->gone = calloc((size_t)n + 1, sizeof(*s->gone));
	s->loop = CFRunLoopGetCurrent();
	CFRetain(s->loop);
	CFRunLoopSourceContext sc;
	memset(&sc, 0, sizeof(sc));
	sc.info = s;
	sc.perform = stop_perform;
	s->stop_source = CFRunLoopSourceCreate(kCFAllocatorDefault, 0, &sc);
	s->mgr = logitech_manager();
	if (!s->devs || !s->bufs || !s->buf_lens || !s->gone || !s->stop_source || !s->mgr) {
		session_free(s);
		return kIOReturnNoMemory;
	}
	CFRunLoopAddSource(s->loop, s->stop_source, kCFRunLoopDefaultMode);

	CFSetRef set;
	CFIndex count;
	IOHIDDeviceRef *all = copy_devices(s->mgr, &set, &count);
	if (!all) {
		if (set)
			CFRelease(set);
		session_free(s);
		return kIOReturnNoMemory;
	}
	IOReturn ret = kIOReturnSuccess;
	for (int i = 0; i < n && ret == kIOReturnSuccess; i++) {
		IOHIDDeviceRef d = NULL;
		for (CFIndex k = 0; k < count; k++)
			if (registry_id(all[k]) == ids[i])
				d = all[k];
		if (!d) {
			ret = kIOReturnNoDevice;
			break;
		}
		ret = IOHIDDeviceOpen(d, kIOHIDOptionsTypeNone); // never seize
		if (ret != kIOReturnSuccess)
			break;
		int32_t max_in = prop_int(d, CFSTR(kIOHIDMaxInputReportSizeKey));
		CFIndex len = max_in > 64 ? max_in : 64;
		uint8_t *buf = calloc((size_t)len, 1);
		if (!buf) {
			IOHIDDeviceClose(d, kIOHIDOptionsTypeNone);
			ret = kIOReturnNoMemory;
			break;
		}
		CFRetain(d);
		s->devs[s->count] = d;
		s->bufs[s->count] = buf;
		s->buf_lens[s->count] = len;
		s->count++;
		IOHIDDeviceRegisterInputReportCallback(d, buf, len, input_cb, s);
		IOHIDDeviceRegisterRemovalCallback(d, removal_cb, s);
		IOHIDDeviceScheduleWithRunLoop(d, s->loop, kCFRunLoopDefaultMode);
	}
	free(all);
	if (set)
		CFRelease(set);
	if (ret != kIOReturnSuccess) {
		session_free(s);
		return ret;
	}
	*out = s;
	return kIOReturnSuccess;
}

void esw_run(esw_session *s) {
	// The stop source keeps the loop from finishing early, so only
	// stop_perform ends it; s stays valid until esw_stop has been called.
	while (!s->stopped)
		CFRunLoopRunInMode(kCFRunLoopDefaultMode, 1.0e10, false);
	session_free(s);
}

void esw_stop(esw_session *s) {
	CFRunLoopSourceSignal(s->stop_source);
	CFRunLoopWakeUp(s->loop);
}

int32_t esw_set_report(esw_session *s, int index, uint8_t report_id, const uint8_t *data, long len) {
	if (index < 0 || index >= s->count)
		return kIOReturnBadArgument;
	// The report ID goes both in the buffer and as its own argument, as
	// mxswitch and hidapi do.
	return IOHIDDeviceSetReport(s->devs[index], kIOHIDReportTypeOutput, report_id, data, (CFIndex)len);
}

int esw_access_denied(int32_t ret) {
	return ret == kIOReturnNotPermitted || ret == kIOReturnNotPrivileged;
}
