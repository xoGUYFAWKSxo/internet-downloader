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

