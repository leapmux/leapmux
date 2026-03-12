export namespace main {
	
	export class DesktopConfig {
	    mode: string;
	    hub_url: string;
	    window_width?: number;
	    window_height?: number;
	
	    static createFrom(source: any = {}) {
	        return new DesktopConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.hub_url = source["hub_url"];
	        this.window_width = source["window_width"];
	        this.window_height = source["window_height"];
	    }
	}

}

