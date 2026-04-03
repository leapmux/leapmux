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
	export class ProxyResponse {
	    status: number;
	    headers: Record<string, string>;
	    body: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxyResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.headers = source["headers"];
	        this.body = source["body"];
	    }
	}
	export class TunnelConfig {
	    workerId: string;
	    type: string;
	    targetAddr: string;
	    targetPort: number;
	    bindAddr: string;
	    bindPort: number;
	    hubURL: string;
	    token: string;
	    userId: string;
	
	    static createFrom(source: any = {}) {
	        return new TunnelConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workerId = source["workerId"];
	        this.type = source["type"];
	        this.targetAddr = source["targetAddr"];
	        this.targetPort = source["targetPort"];
	        this.bindAddr = source["bindAddr"];
	        this.bindPort = source["bindPort"];
	        this.hubURL = source["hubURL"];
	        this.token = source["token"];
	        this.userId = source["userId"];
	    }
	}
	export class TunnelInfo {
	    id: string;
	    workerId: string;
	    type: string;
	    bindAddr: string;
	    bindPort: number;
	    targetAddr: string;
	    targetPort: number;
	
	    static createFrom(source: any = {}) {
	        return new TunnelInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.workerId = source["workerId"];
	        this.type = source["type"];
	        this.bindAddr = source["bindAddr"];
	        this.bindPort = source["bindPort"];
	        this.targetAddr = source["targetAddr"];
	        this.targetPort = source["targetPort"];
	    }
	}

}

