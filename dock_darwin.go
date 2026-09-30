package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#import <Cocoa/Cocoa.h>

static void setDockBadge(const char *label) {
	NSString *text = label ? [NSString stringWithUTF8String:label] : nil;
	dispatch_async(dispatch_get_main_queue(), ^{
		[[NSApp dockTile] setBadgeLabel:text];
	});
}
*/
import "C"

import (
	"strconv"
	"unsafe"
)

// SetDockBadge shows the number of running requests on the Dock icon (hidden at 0).
func SetDockBadge(n int) {
	if n <= 0 {
		C.setDockBadge(nil)
		return
	}
	label := C.CString(strconv.Itoa(n))
	defer C.free(unsafe.Pointer(label))
	C.setDockBadge(label)
}
