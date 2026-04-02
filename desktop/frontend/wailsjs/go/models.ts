export namespace main {
	
	export class BuildInfo {
	    version: string;
	    commit_hash: string;
	    commit_time: string;
	    build_time: string;

	    static createFrom(source: any = {}) {
	        return new BuildInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.commit_hash = source["commit_hash"];
	        this.commit_time = source["commit_time"];
	        this.build_time = source["build_time"];
	    }
	}
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

