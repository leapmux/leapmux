export namespace main {
	
	export class DesktopConfig {
	    mode: string;
	    hub_url: string;
	
	    static createFrom(source: any = {}) {
	        return new DesktopConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.hub_url = source["hub_url"];
	    }
	}

}

