export namespace bridge {
	
	export class Activity {
	    id: number;
	    origin: string;
	    provider: string;
	    model: string;
	    status: string;
	    error?: string;
	    // Go type: time
	    started_at: any;
	    duration_ms: number;
	    input_tokens: number;
	    output_tokens: number;
	
	    static createFrom(source: any = {}) {
	        return new Activity(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.origin = source["origin"];
	        this.provider = source["provider"];
	        this.model = source["model"];
	        this.status = source["status"];
	        this.error = source["error"];
	        this.started_at = this.convertValues(source["started_at"], null);
	        this.duration_ms = source["duration_ms"];
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Model {
	    id: string;
	    name: string;
	    efforts?: string[];
	
	    static createFrom(source: any = {}) {
	        return new Model(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.efforts = source["efforts"];
	    }
	}
	export class ProviderInfo {
	    id: string;
	    name: string;
	    available: boolean;
	    version: string;
	    path: string;
	    default_model: string;
	    models: Model[];
	    efforts: string[];
	    capabilities: string[];
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new ProviderInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.available = source["available"];
	        this.version = source["version"];
	        this.path = source["path"];
	        this.default_model = source["default_model"];
	        this.models = this.convertValues(source["models"], Model);
	        this.efforts = source["efforts"];
	        this.capabilities = source["capabilities"];
	        this.error = source["error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TestResult {
	    ok: boolean;
	    text: string;
	    model: string;
	    duration_ms: number;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new TestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.text = source["text"];
	        this.model = source["model"];
	        this.duration_ms = source["duration_ms"];
	        this.error = source["error"];
	    }
	}
	export class TrayUsage {
	    style: string;
	    window: string;
	    hidden?: string[];
	    hide_icon?: boolean;
	    hide_activity?: boolean;
	    monochrome?: boolean;
	    hide_tool_icons?: boolean;
	    show_reset?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new TrayUsage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.style = source["style"];
	        this.window = source["window"];
	        this.hidden = source["hidden"];
	        this.hide_icon = source["hide_icon"];
	        this.hide_activity = source["hide_activity"];
	        this.monochrome = source["monochrome"];
	        this.hide_tool_icons = source["hide_tool_icons"];
	        this.show_reset = source["show_reset"];
	    }
	}
	export class UsageWindow {
	    id: string;
	    label: string;
	    used_percent: number;
	    window_minutes?: number;
	    // Go type: time
	    resets_at?: any;
	
	    static createFrom(source: any = {}) {
	        return new UsageWindow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.used_percent = source["used_percent"];
	        this.window_minutes = source["window_minutes"];
	        this.resets_at = this.convertValues(source["resets_at"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class UsageLimits {
	    provider: string;
	    name: string;
	    supported: boolean;
	    plan?: string;
	    windows: UsageWindow[];
	    // Go type: time
	    fetched_at?: any;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new UsageLimits(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.name = source["name"];
	        this.supported = source["supported"];
	        this.plan = source["plan"];
	        this.windows = this.convertValues(source["windows"], UsageWindow);
	        this.fetched_at = this.convertValues(source["fetched_at"], null);
	        this.error = source["error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class ActivityDetail {
	    request: number[];
	    response: number[];
	
	    static createFrom(source: any = {}) {
	        return new ActivityDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.request = source["request"];
	        this.response = source["response"];
	    }
	}
	export class State {
	    running: boolean;
	    addr: string;
	    version: string;
	    error: string;
	    active: number;
	    max_concurrent: number;
	    provider_limits: Record<string, number>;
	    timeout_sec: number;
	    default_models: Record<string, string>;
	    origins: string[];
	    tray_usage: bridge.TrayUsage;
	    token: string;
	    config_path: string;
	
	    static createFrom(source: any = {}) {
	        return new State(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.addr = source["addr"];
	        this.version = source["version"];
	        this.error = source["error"];
	        this.active = source["active"];
	        this.max_concurrent = source["max_concurrent"];
	        this.provider_limits = source["provider_limits"];
	        this.timeout_sec = source["timeout_sec"];
	        this.default_models = source["default_models"];
	        this.origins = source["origins"];
	        this.tray_usage = this.convertValues(source["tray_usage"], bridge.TrayUsage);
	        this.token = source["token"];
	        this.config_path = source["config_path"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

