#import <Cocoa/Cocoa.h>
#include "tray_darwin.h"
#include "_cgo_export.h"

@interface TrayTarget : NSObject <NSMenuDelegate>
@end

@implementation TrayTarget
- (void)show:(id)sender { trayAction(0); }
- (void)toggle:(id)sender { trayAction(1); }
- (void)quit:(id)sender { trayAction(2); }
- (void)refresh:(id)sender { trayAction(3); }
- (void)menuWillOpen:(NSMenu *)menu { trayAction(4); }
@end

@interface UsagePanel : NSView {
	NSDictionary *data;
}
- (void)setData:(NSDictionary *)d;
@end

static NSStatusItem *item;
static NSMenuItem *statusLine, *toggleItem;
static UsagePanel *panel;
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

#define PANEL_W 320
#define PANEL_PAD 16

static NSLayoutManager *typesetter;

static NSDictionary *textAttrs(NSFont *font, NSColor *color, NSLineBreakMode mode) {
	NSMutableParagraphStyle *ps = [[NSMutableParagraphStyle new] autorelease];
	ps.lineBreakMode = mode;
	return @{NSFontAttributeName: font, NSForegroundColorAttributeName: color, NSParagraphStyleAttributeName: ps};
}

// Draws one line on a baseline, left-aligned from x or right-aligned to x, and returns its width.
static CGFloat drawLine(NSString *s, NSDictionary *attrs, CGFloat x, CGFloat baseline, CGFloat maxW, BOOL alignRight, BOOL draw) {
	if (!s.length) return 0;
	NSFont *font = attrs[NSFontAttributeName];
	if (!typesetter) typesetter = [NSLayoutManager new];
	CGFloat w = MIN(ceil([s sizeWithAttributes:attrs].width), maxW);
	// The line box puts the baseline at the typesetter's offset, not at the font's ascender.
	CGFloat top = baseline - [typesetter defaultBaselineOffsetForFont:font];
	if (draw) [s drawInRect:NSMakeRect(alignRight ? x - w : x, top, w, ceil([typesetter defaultLineHeightForFont:font])) withAttributes:attrs];
	return w;
}

static NSColor *tone(CGFloat r, CGFloat g, CGFloat b, CGFloat dr, CGFloat dg, CGFloat db) {
	return [NSColor colorWithName:nil dynamicProvider:^NSColor *(NSAppearance *a) {
		BOOL dark = [[a bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]] isEqualToString:NSAppearanceNameDarkAqua];
		return dark ? [NSColor colorWithSRGBRed:dr green:dg blue:db alpha:1] : [NSColor colorWithSRGBRed:r green:g blue:b alpha:1];
	}];
}

// Softer than the menu bar icon: muted gray, amber from 75%, coral from 90%, on a faint track.
static void drawPanelBar(NSRect track, double pct) {
	CGFloat r = track.size.height / 2;
	NSBezierPath *pill = [NSBezierPath bezierPathWithRoundedRect:track xRadius:r yRadius:r];
	[[[NSColor labelColor] colorWithAlphaComponent:0.09] set];
	[pill fill];
	NSRect fill = track;
	fill.size.width = track.size.width * MAX(0, MIN(100, pct)) / 100;
	if (fill.size.width <= 0) return;
	NSColor *c = pct >= 90 ? tone(0.88, 0.45, 0.40, 0.93, 0.52, 0.47)
		: pct >= 75 ? tone(0.89, 0.66, 0.33, 0.91, 0.70, 0.40)
		: tone(0.45, 0.47, 0.51, 0.66, 0.68, 0.72);
	[NSGraphicsContext saveGraphicsState];
	[pill addClip];
	[c set];
	NSRectFillUsingOperation(fill, NSCompositingOperationSourceOver);
	[NSGraphicsContext restoreGraphicsState];
}

static NSString *str(id v) { return [v isKindOfClass:[NSString class]] ? v : @""; }
static NSArray *list(id v) { return [v isKindOfClass:[NSArray class]] ? v : @[]; }

static int glyphFor(NSString *provider) {
	NSArray *ids = @[@"claude", @"codex", @"gemini"];
	NSUInteger i = [ids indexOfObject:provider];
	return i == NSNotFound ? 0 : (int)i + 1;
}

// The usage overview at the top of the menu: each AI tool with its status and one bar per limit.
@implementation UsagePanel
- (BOOL)isFlipped { return YES; }

- (void)setData:(NSDictionary *)d {
	[data release];
	data = [d retain];
	[self setFrameSize:NSMakeSize(self.frame.size.width, [self layout:NO])];
	self.needsDisplay = YES;
	NSMutableArray *spoken = [NSMutableArray arrayWithObject:str(data[@"updated"])];
	for (NSDictionary *p in list(data[@"providers"])) {
		[spoken addObject:[NSString stringWithFormat:@"%@, %@", str(p[@"name"]), str(p[@"status"])]];
		for (NSDictionary *l in list(p[@"limits"]))
			[spoken addObject:[NSString stringWithFormat:@"%@ %.0f%% used %@", str(l[@"label"]), [l[@"percent"] doubleValue], str(l[@"reset"])]];
	}
	self.accessibilityLabel = [spoken componentsJoinedByString:@". "];
}

- (void)drawRect:(NSRect)dirty { [self layout:YES]; }

// Measures, and draws when asked, so the height always matches what is drawn.
- (CGFloat)layout:(BOOL)draw {
	CGFloat x = PANEL_PAD, right = self.bounds.size.width - PANEL_PAD, width = right - x, y = 12;
	NSColor *primary = [NSColor labelColor], *secondary = [NSColor secondaryLabelColor];
	NSDictionary *title = textAttrs([NSFont systemFontOfSize:13 weight:NSFontWeightBold], primary, NSLineBreakByTruncatingTail);
	NSDictionary *name = textAttrs([NSFont systemFontOfSize:13 weight:NSFontWeightSemibold], primary, NSLineBreakByTruncatingTail);
	NSDictionary *body = textAttrs([NSFont menuFontOfSize:13], primary, NSLineBreakByTruncatingTail);
	NSDictionary *num = textAttrs([NSFont monospacedDigitSystemFontOfSize:13 weight:NSFontWeightSemibold], primary, NSLineBreakByClipping);
	NSDictionary *small = textAttrs([NSFont monospacedDigitSystemFontOfSize:11.5 weight:NSFontWeightRegular], secondary, NSLineBreakByTruncatingTail);
	NSDictionary *note = textAttrs([NSFont systemFontOfSize:11.5], secondary, NSLineBreakByWordWrapping);
	CGFloat pctW = ceil([@"100%" sizeWithAttributes:num].width);

	CGFloat base = y + 13;
	CGFloat updatedW = drawLine(str(data[@"updated"]), small, right, base, width / 2, YES, draw);
	drawLine(@"Usage", title, x, base, width - updatedW - 8, NO, draw);
	y += 22;

	for (NSDictionary *p in list(data[@"providers"])) {
		if (draw) {
			[[NSColor separatorColor] set];
			NSRectFillUsingOperation(NSMakeRect(x, y + 10, width, 1), NSCompositingOperationSourceOver);
		}
		y += 24;
		// Name baseline sits so its capitals are centered on the 18pt icon box.
		base = round(y + 9 + ((NSFont *)name[NSFontAttributeName]).capHeight / 2);
		if (draw) {
			NSRect box = NSMakeRect(x + 0.5, y + 0.5, 17, 17);
			NSBezierPath *frame = [NSBezierPath bezierPathWithRoundedRect:box xRadius:4.5 yRadius:4.5];
			frame.lineWidth = 1;
			[[NSColor tertiaryLabelColor] set];
			[frame stroke];
			int glyph = glyphFor(str(p[@"id"]));
			drawGlyph(glyph, NSInsetRect(box, 4, 4), glyphColor(glyph, NO));
		}
		// Status on the right: a filled dot while running, a ring when idle.
		BOOL busy = [p[@"busy"] boolValue];
		CGFloat statusW = drawLine(str(p[@"status"]), small, right, base, width / 3, YES, draw);
		if (draw) {
			NSRect dot = NSMakeRect(right - statusW - 11, y + 6, 6, 6);
			if (busy) {
				[[NSColor systemGreenColor] set];
				[[NSBezierPath bezierPathWithOvalInRect:dot] fill];
			} else {
				[secondary set];
				NSBezierPath *ring = [NSBezierPath bezierPathWithOvalInRect:NSInsetRect(dot, 0.5, 0.5)];
				ring.lineWidth = 1;
				[ring stroke];
			}
		}
		CGFloat nameX = x + 26, nameMax = right - statusW - 18 - nameX;
		CGFloat nameW = drawLine(str(p[@"name"]), name, nameX, base, nameMax, NO, draw);
		drawLine(str(p[@"plan"]), small, nameX + nameW + 6, base, nameMax - nameW - 6, NO, draw);
		y += 30;

		NSString *why = str(p[@"note"]);
		if (why.length) {
			NSRect r = [why boundingRectWithSize:NSMakeSize(width, 46) options:NSStringDrawingUsesLineFragmentOrigin attributes:note];
			if (draw) [why drawWithRect:NSMakeRect(x, y, width, ceil(r.size.height)) options:NSStringDrawingUsesLineFragmentOrigin | NSStringDrawingTruncatesLastVisibleLine attributes:note];
			y += ceil(r.size.height) + 8;
		}
		for (NSDictionary *l in list(p[@"limits"])) {
			double pct = [l[@"percent"] doubleValue];
			base = y + 13;
			drawLine([NSString stringWithFormat:@"%.0f%%", pct], num, right, base, pctW, YES, draw);
			CGFloat resetW = drawLine(str(l[@"reset"]), small, right - pctW - 10, base, width / 2, YES, draw);
			drawLine(str(l[@"label"]), body, x, base, width - pctW - 10 - resetW - 8, NO, draw);
			y += 20;
			if (draw) drawPanelBar(NSMakeRect(x, y, width, 6), pct);
			y += 18;
		}
	}
	NSString *empty = str(data[@"empty"]);
	if (empty.length) {
		if (draw) {
			[[NSColor separatorColor] set];
			NSRectFillUsingOperation(NSMakeRect(x, y + 10, width, 1), NSCompositingOperationSourceOver);
		}
		y += 24;
		drawLine(empty, note, x, y + 13, width, NO, draw);
		y += 22;
	}
	return y + 2;
}
@end

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
		menu.delegate = target;
		panel = [[UsagePanel alloc] initWithFrame:NSMakeRect(0, 0, PANEL_W, 0)];
		panel.autoresizingMask = NSViewWidthSizable;
		panel.accessibilityElement = YES;
		panel.accessibilityRole = NSAccessibilityStaticTextRole;
		[panel setData:@{@"updated": @"Checking…"}];
		NSMenuItem *panelItem = [[NSMenuItem new] autorelease];
		panelItem.view = panel;
		[menu addItem:panelItem];
		[menu addItem:[NSMenuItem separatorItem]];
		statusLine = [menu addItemWithTitle:@"Starting…" action:nil keyEquivalent:@""];
		statusLine.enabled = NO;
		addItem(menu, @"Open AI Bridge", @selector(show:), @"o");
		toggleItem = addItem(menu, @"Stop bridge", @selector(toggle:), @"");
		addItem(menu, @"Refresh Now", @selector(refresh:), @"r");
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

void trayUsage(int flags, int count, const double *percents, const int *active, const int *glyphs, const char *resets, int other, const char *panelJSON) {
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
	// Copied here since Go frees the string; parsed on the main thread, which has an autorelease pool.
	NSData *json = [[NSData alloc] initWithBytes:panelJSON length:strlen(panelJSON)];
	dispatch_async(dispatch_get_main_queue(), ^{
		id d = [NSJSONSerialization JSONObjectWithData:json options:0 error:nil];
		[json release];
		if (!item) return;
		meter = m;
		setIcon(item.button);
		updateTitle();
		updateBlink();
		if ([d isKindOfClass:[NSDictionary class]]) [panel setData:d];
	});
}
