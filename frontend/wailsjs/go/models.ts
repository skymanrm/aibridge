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
	    origins: string[];
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
	        this.origins = source["origins"];
	        this.token = source["token"];
	        this.config_path = source["config_path"];
	    }
	}

}

