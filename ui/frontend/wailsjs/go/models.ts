export namespace main {
	
	export class EngineStatus {
	    running: boolean;
	    version?: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new EngineStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.version = source["version"];
	        this.message = source["message"];
	    }
	}

}

export namespace media {
	
	export class Summary {
	    id: number;
	    // Go type: time
	    received: any;
	    browser: string;
	    kind: string;
	    url: string;
	    pageTitle?: string;
	    manifestBytes: number;
	    cookies: number;
	
	    static createFrom(source: any = {}) {
	        return new Summary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.received = this.convertValues(source["received"], null);
	        this.browser = source["browser"];
	        this.kind = source["kind"];
	        this.url = source["url"];
	        this.pageTitle = source["pageTitle"];
	        this.manifestBytes = source["manifestBytes"];
	        this.cookies = source["cookies"];
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

