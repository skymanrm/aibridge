#import <Cocoa/Cocoa.h>
#include "tray_darwin.h"
#include "_cgo_export.h"

@interface TrayTarget : NSObject
@end

@implementation TrayTarget
- (void)show:(id)sender { trayAction(0); }
- (void)toggle:(id)sender { trayAction(1); }
- (void)quit:(id)sender { trayAction(2); }
@end

static NSStatusItem *item;
static NSMenuItem *statusLine, *toggleItem;
static TrayTarget *target;

// SF Symbol "sparkles"; falls back to text before macOS 11.
static void setIcon(NSStatusBarButton *button) {
	if (@available(macOS 11.0, *)) {
		NSImage *img = [NSImage imageWithSystemSymbolName:@"sparkles" accessibilityDescription:@"AI Bridge"];
		img = [img imageWithSymbolConfiguration:[NSImageSymbolConfiguration configurationWithPointSize:15 weight:NSFontWeightRegular]];
		img.template = YES;
		button.image = img;
		return;
	}
	button.title = @"AI";
}

static NSMenuItem *addItem(NSMenu *menu, NSString *title, SEL action, NSString *key) {
	NSMenuItem *mi = [menu addItemWithTitle:title action:action keyEquivalent:key];
	mi.target = target;
	return mi;
}

void trayInstall(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		// Wails forces a regular (Dock) app on launch; switch to a menu bar-only app.
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
		if (item) return;
		target = [TrayTarget new];
		item = [[[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength] retain];
		setIcon(item.button);
		item.button.imagePosition = NSImageLeft;
		item.button.toolTip = @"AI Bridge";

		NSMenu *menu = [NSMenu new];
		menu.autoenablesItems = NO;
		statusLine = [menu addItemWithTitle:@"Starting…" action:nil keyEquivalent:@""];
		statusLine.enabled = NO;
		[menu addItem:[NSMenuItem separatorItem]];
		addItem(menu, @"Open AI Bridge", @selector(show:), @"");
		toggleItem = addItem(menu, @"Stop bridge", @selector(toggle:), @"");
		[menu addItem:[NSMenuItem separatorItem]];
		addItem(menu, @"Quit AI Bridge", @selector(quit:), @"q");
		item.menu = menu;
	});
}

void trayUpdate(int running, int active, const char *status) {
	NSString *text = [NSString stringWithUTF8String:status];
	dispatch_async(dispatch_get_main_queue(), ^{
		if (!item) return;
		item.button.appearsDisabled = !running;
		NSString *count = active > 0 ? [NSString stringWithFormat:@"%d", active] : @"";
		item.button.title = item.button.image ? count : [@"AI " stringByAppendingString:count];
		statusLine.title = text;
		toggleItem.title = running ? @"Stop bridge" : @"Start bridge";
	});
}
