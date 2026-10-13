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
static NSMutableArray<NSMenuItem *> *usageItems;
static TrayTarget *target;

#define MAX_BARS 8
#define LABEL_LEN 8
// Copied by value into drawing blocks, which can't capture plain C arrays.
typedef struct {
	int n, other;
	BOOL vertical, icon, dots, mono, tools, reset;
	double pct[MAX_BARS];
	int active[MAX_BARS];
	int glyph[MAX_BARS]; // 0 dot, 1 Claude, 2 Codex, 3 Gemini
	char label[MAX_BARS][LABEL_LEN];
} Meter;
static Meter meter = {.icon = YES};
static int lastActive;
static BOOL blinkOn = YES;
static NSTimer *blinkTimer;

API_AVAILABLE(macos(11.0)) static NSImage *sparkles(void) {
	NSImage *img = [NSImage imageWithSystemSymbolName:@"sparkles" accessibilityDescription:@"AI Bridge"];
	img = [img imageWithSymbolConfiguration:[NSImageSymbolConfiguration configurationWithPointSize:15 weight:NSFontWeightRegular]];
	img.template = YES;
	return img;
}

// Monochrome images are templates, where only alpha counts.
static NSColor *ink(BOOL mono) { return mono ? [NSColor blackColor] : [NSColor labelColor]; }

// Same thresholds as the window: orange from 75%, red from 90%.
static NSColor *barColor(double pct, BOOL mono) {
	if (mono) return ink(YES);
	if (pct >= 90) return [NSColor systemRedColor];
	if (pct >= 75) return [NSColor systemOrangeColor];
	return [NSColor labelColor];
}

// The fill is clipped to the track's rounded shape, so low values read as a sliver of the pill, not a thin line.
static void drawBar(NSRect track, double pct, BOOL vertical, BOOL mono, CGFloat alpha) {
	CGFloat r = (vertical ? track.size.width : track.size.height) / 2;
	NSBezierPath *pill = [NSBezierPath bezierPathWithRoundedRect:track xRadius:r yRadius:r];
	[[ink(mono) colorWithAlphaComponent:0.25] set];
	[pill fill];
	NSRect fill = track;
	if (vertical) fill.size.height = track.size.height * pct / 100;
	else fill.size.width = track.size.width * pct / 100;
	if (fill.size.width <= 0 || fill.size.height <= 0) return;
	[NSGraphicsContext saveGraphicsState];
	[pill addClip];
	[[barColor(pct, mono) colorWithAlphaComponent:alpha] set];
	NSRectFillUsingOperation(fill, NSCompositingOperationSourceOver);
	[NSGraphicsContext restoreGraphicsState];
}

// Light brand tints in color mode, so the marks tell the tools apart at a glance.
static NSColor *glyphColor(int glyph, BOOL mono) {
	if (mono) return ink(YES);
	switch (glyph) {
	case 1: return [NSColor colorWithSRGBRed:0.85 green:0.47 blue:0.34 alpha:1];
	case 3: return [NSColor colorWithSRGBRed:0.36 green:0.55 blue:0.98 alpha:1];
	}
	return [NSColor labelColor];
}

// Ultra small tool marks: Claude a starburst, Codex a hexagon ring, Gemini a four-point star, others a dot.
static void drawGlyph(int glyph, NSRect r, NSColor *color) {
	CGFloat cx = NSMidX(r), cy = NSMidY(r), s = r.size.width / 2;
	NSBezierPath *p = [NSBezierPath bezierPath];
	[color set];
	switch (glyph) {
	case 1:
		for (int i = 0; i < 4; i++) {
			CGFloat a = i * M_PI / 4, dx = cos(a) * s * 0.88, dy = sin(a) * s * 0.88;
			[p moveToPoint:NSMakePoint(cx - dx, cy - dy)];
			[p lineToPoint:NSMakePoint(cx + dx, cy + dy)];
		}
		p.lineWidth = MAX(0.9, s * 0.36);
		p.lineCapStyle = NSLineCapStyleRound;
		[p stroke];
		break;
	case 2:
		for (int i = 0; i < 6; i++) {
			CGFloat a = M_PI / 6 + i * M_PI / 3;
			NSPoint pt = NSMakePoint(cx + cos(a) * s * 0.78, cy + sin(a) * s * 0.78);
			if (i == 0) [p moveToPoint:pt];
			else [p lineToPoint:pt];
		}
		[p closePath];
		p.lineWidth = MAX(0.9, s * 0.34);
		p.lineJoinStyle = NSLineJoinStyleRound;
		[p stroke];
		break;
	case 3: {
		CGFloat k = s * 0.14;
		[p moveToPoint:NSMakePoint(cx, cy + s)];
		[p curveToPoint:NSMakePoint(cx + s, cy) controlPoint1:NSMakePoint(cx + k, cy + k) controlPoint2:NSMakePoint(cx + k, cy + k)];
		[p curveToPoint:NSMakePoint(cx, cy - s) controlPoint1:NSMakePoint(cx + k, cy - k) controlPoint2:NSMakePoint(cx + k, cy - k)];
		[p curveToPoint:NSMakePoint(cx - s, cy) controlPoint1:NSMakePoint(cx - k, cy - k) controlPoint2:NSMakePoint(cx - k, cy - k)];
		[p curveToPoint:NSMakePoint(cx, cy + s) controlPoint1:NSMakePoint(cx - k, cy + k) controlPoint2:NSMakePoint(cx - k, cy + k)];
		[p fill];
		break;
	}
	default:
		[[NSBezierPath bezierPathWithOvalInRect:NSInsetRect(r, s * 0.3, s * 0.3)] fill];
	}
}

// Optional symbol, then one bar per AI tool with its mark before it (above, when vertical) and the time to reset after it.
// While a tool runs a request its mark pulses, or its bar does when marks are off. Drawn lazily so colors follow the menu bar.
API_AVAILABLE(macos(11.0)) static NSImage *meterIcon(NSImage *symbol, Meter m, BOOL bright) {
	int n = m.n;
	CGFloat h = 20, area = 16, y0 = (h - area) / 2, gap = 2;
	NSSize sym = symbol ? symbol.size : NSZeroSize;
	CGFloat x0 = symbol ? sym.width + 4 : 0;
	NSFont *font = [NSFont monospacedDigitSystemFontOfSize:(n == 1 ? 10 : n == 2 || m.vertical ? 8 : 6.5) weight:NSFontWeightSemibold];
	NSMutableArray<NSString *> *labels = [NSMutableArray array];
	CGFloat labelW = 0;
	for (int i = 0; i < n; i++) {
		NSString *l = m.reset ? [NSString stringWithUTF8String:m.label[i]] : @"";
		[labels addObject:l];
		labelW = MAX(labelW, [l sizeWithAttributes:@{NSFontAttributeName: font}].width);
	}
	CGFloat labelGap = labelW > 0 ? 2.5 : 0;

	CGFloat width, thick, g, barW = 0, col = 0, group = 0, groupGap = 0;
	if (m.vertical) {
		thick = n == 1 ? 6 : n == 2 ? 5 : 4;
		g = m.tools ? (n == 1 ? 8 : 6) : 0;
		col = MAX(thick, g);
		group = col + labelGap + labelW;
		groupGap = labelW > 0 ? 4 : gap;
		width = x0 + n * group + (n - 1) * groupGap;
	} else {
		thick = n == 1 ? 8 : n == 2 ? 6 : 4;
		g = m.tools ? (n == 1 ? 10 : n == 2 ? 7.5 : 5.5) : 0;
		barW = n == 1 ? 26 : 22;
		width = x0 + (g > 0 ? g + 2 : 0) + barW + labelGap + labelW;
	}
	NSImage *img = [NSImage imageWithSize:NSMakeSize(ceil(width), h) flipped:NO drawingHandler:^BOOL(NSRect rect) {
		if (symbol) {
			NSRect symRect = NSMakeRect(0, (h - sym.height) / 2, sym.width, sym.height);
			[symbol drawInRect:symRect];
			[ink(m.mono) set];
			NSRectFillUsingOperation(symRect, NSCompositingOperationSourceAtop);
		}
		NSDictionary *attrs = @{NSFontAttributeName: font, NSForegroundColorAttributeName: ink(m.mono)};
		CGFloat stack = n * thick + (n - 1) * gap;
		for (int i = 0; i < n; i++) {
			BOOL busy = m.dots && m.active[i] > 0;
			CGFloat barAlpha = busy && !g && !bright ? 0.4 : 1;
			NSColor *mark = [glyphColor(m.glyph[i], m.mono) colorWithAlphaComponent:busy ? (bright ? 1 : 0.3) : 0.6];
			NSString *label = labels[i];
			NSSize ls = [label sizeWithAttributes:attrs];
			if (m.vertical) {
				CGFloat x = x0 + i * (group + groupGap);
				CGFloat barH = area - (g > 0 ? g + 1.5 : 0);
				drawBar(NSMakeRect(x + (col - thick) / 2, y0, thick, barH), m.pct[i], YES, m.mono, barAlpha);
				if (g > 0) drawGlyph(m.glyph[i], NSMakeRect(x + (col - g) / 2, y0 + area - g, g, g), mark);
				if (label.length) [label drawAtPoint:NSMakePoint(x + col + labelGap, y0 + area / 2 - ls.height / 2) withAttributes:attrs];
			} else {
				CGFloat y = y0 + (area + stack) / 2 - thick - i * (thick + gap), cy = y + thick / 2;
				CGFloat barX = x0 + (g > 0 ? g + 2 : 0);
				if (g > 0) drawGlyph(m.glyph[i], NSMakeRect(x0, cy - g / 2, g, g), mark);
				drawBar(NSMakeRect(barX, y, barW, thick), m.pct[i], NO, m.mono, barAlpha);
				if (label.length) [label drawAtPoint:NSMakePoint(barX + barW + labelGap, cy - ls.height / 2) withAttributes:attrs];
			}
		}
		return YES;
	}];
	img.template = m.mono;
	img.accessibilityDescription = @"AI Bridge usage";
	return img;
}

// SF Symbol "sparkles", with usage bars when there are any (never an empty item); falls back to text before macOS 11.
static void setIcon(NSStatusBarButton *button) {
	if (@available(macOS 11.0, *)) {
		button.image = meter.n > 0 ? meterIcon(meter.icon ? sparkles() : nil, meter, blinkOn) : sparkles();
		return;
	}
	button.title = @"AI";
}

// With activity shown on the bars, the number only counts requests of AI tools that have no bar.
// An empty title switches to image-only so the button gets no padding for it.
static void updateTitle(void) {
	int active = meter.n > 0 && meter.dots ? meter.other : lastActive;
	NSString *count = active > 0 ? [NSString stringWithFormat:@"%d", active] : @"";
	if (!item.button.image) {
		item.button.title = [@"AI " stringByAppendingString:count];
		return;
	}
	item.button.title = count;
	item.button.imagePosition = count.length ? NSImageLeft : NSImageOnly;
}

// Busy marks (or bars) pulse unless Reduce Motion is on.
static void updateBlink(void) {
	BOOL busy = NO;
	for (int i = 0; i < meter.n; i++) busy = busy || (meter.dots && meter.active[i] > 0);
	BOOL animate = busy && !NSWorkspace.sharedWorkspace.accessibilityDisplayShouldReduceMotion;
	if (animate && !blinkTimer) {
		blinkTimer = [[NSTimer scheduledTimerWithTimeInterval:0.6 repeats:YES block:^(NSTimer *t) {
			blinkOn = !blinkOn;
			setIcon(item.button);
		}] retain];
	} else if (!animate && blinkTimer) {
		[blinkTimer invalidate];
		[blinkTimer release];
		blinkTimer = nil;
		blinkOn = YES;
		setIcon(item.button);
	}
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
		usageItems = [NSMutableArray new];
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
		lastActive = active;
		updateTitle();
		statusLine.title = text;
		toggleItem.title = running ? @"Stop bridge" : @"Start bridge";
	});
}

void trayUsage(int flags, int count, const double *percents, const int *active, const int *glyphs, const char *resets, int other, const char *lines) {
	Meter m = {.n = count < MAX_BARS ? count : MAX_BARS, .other = other, .vertical = flags & 1, .icon = (flags >> 1) & 1,
		.dots = (flags >> 2) & 1, .mono = (flags >> 3) & 1, .tools = (flags >> 4) & 1, .reset = (flags >> 5) & 1};
	memcpy(m.pct, percents, m.n * sizeof(double));
	memcpy(m.active, active, m.n * sizeof(int));
	memcpy(m.glyph, glyphs, m.n * sizeof(int));
	// resets holds one "\n"-separated label per bar.
	const char *p = resets;
	for (int i = 0; i < m.n && p; i++) {
		const char *end = strchr(p, '\n');
		size_t len = end ? (size_t)(end - p) : strlen(p);
		strncpy(m.label[i], p, MIN(len, (size_t)LABEL_LEN - 1));
		p = end ? end + 1 : NULL;
	}
	NSString *text = [NSString stringWithUTF8String:lines];
	dispatch_async(dispatch_get_main_queue(), ^{
		if (!item) return;
		meter = m;
		setIcon(item.button);
		updateTitle();
		updateBlink();
		// Usage lines sit right under the status line.
		NSMenu *menu = item.menu;
		for (NSMenuItem *mi in usageItems) [menu removeItem:mi];
		[usageItems removeAllObjects];
		NSInteger at = [menu indexOfItem:statusLine] + 1;
		for (NSString *line in [text componentsSeparatedByString:@"\n"]) {
			if (!line.length) continue;
			NSMenuItem *mi = [[NSMenuItem alloc] initWithTitle:line action:nil keyEquivalent:@""];
			mi.enabled = NO;
			[menu insertItem:mi atIndex:at++];
			[usageItems addObject:mi];
		}
	});
}
